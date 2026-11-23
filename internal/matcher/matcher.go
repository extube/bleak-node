// Package matcher runs the auto-matchmaking worker (feature #41): once
// every 5 minutes it ranks the active personas of every live room and
// automatically sends the best handshakes.
//
// One job:
//
//  1. fetch every room with at least one active invite code (not revoked,
//     not expired) — rooms host live events, the rest is idle;
//  2. fetch the room's ACTIVE users (active = true, not deleted) and
//     check the room handshake cap: at N^2/10 (N = active users) the room
//     is saturated and skipped (manual handshake sends stay open);
//  3. build the PAIR POOL: every directed pair X -> Y of the active users
//     with its match points from POST /api/match —
//     100 * (1 - 1/2^(sqrt(t) * (1 + 3.522 * C))), the score computed AS
//     the sender X (t = hours since X's last sent handshake, or since
//     their creation when they never sent one);
//  4. sort the whole pool by points (desc; sender address, then receiver
//     address break ties), drop the pairs that already carry a directed
//     handshake (the slots go to NEW acquaintances) and send the handshake
//     X -> Y for the top floor(sqrt(active users)) pairs. Sending goes
//     through the regular handshake path (idempotent per directed pair,
//     balance rule intact, auto-chat on mutuality).
package matcher

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// Job tuning.
const (
	// Interval is how often the worker performs one matching job.
	Interval = 5 * time.Minute
)

// pairBudget caps the handshakes one job sends per room: the square root
// of the room's ACTIVE user count, floored (three active users budget one
// pair, nine budget three, sixteen four).
func pairBudget(activeUsers int) int {
	return int(math.Sqrt(float64(activeUsers)))
}

// roomHandshakeCap caps the handshakes a room may hold in total at
// N^2 / 10 of its active user count N, floored (five users cap at 2,
// sixteen at 25, thirty-eight at 144). At or above the cap the
// auto-matcher skips the room — users can still send handshakes by hand.
func roomHandshakeCap(activeUsers int) int {
	return activeUsers * activeUsers / 10
}

// Worker runs the auto-matchmaking job on its interval until ctx is done.
type Worker struct {
	db  *db.DB
	log *slog.Logger
}

// New creates the matcher worker.
func New(database *db.DB, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{db: database, log: logger}
}

// Run performs one job immediately and then on every Interval tick until
// ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if err := w.RunOnce(ctx); err != nil {
		w.log.Error("match worker job failed", "err", err)
	}
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil {
				w.log.Error("match worker job failed", "err", err)
			}
		}
	}
}

// matchPair is one entry of the room's pair pool: a possible handshake
// X -> Y with its match points (scored as the sender X).
type matchPair struct {
	from, to string
	points   float64
}

// RunOnce performs one full matching job over all live rooms.
func (w *Worker) RunOnce(ctx context.Context) error {
	rooms, err := w.db.ListRoomsWithActiveInvites(ctx)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		if err := w.matchRoom(ctx, room); err != nil {
			// One broken room must not stop the others.
			w.log.Error("match worker room failed", "room", room.Address, "err", err)
		}
	}
	return nil
}

// matchRoom ranks every directed pair of one room's active users and sends
// the top floor(sqrt(users)) handshakes — unless the room already holds
// its handshake cap (2/3 of the active user count).
func (w *Worker) matchRoom(ctx context.Context, room model.Room) error {
	users, err := w.db.ListActiveRoomUsers(ctx, room.Address)
	if err != nil {
		return err
	}
	if len(users) < 2 {
		return nil
	}

	// Room handshake cap (feature #45): at N^2/10 (N = active users) the
	// room is saturated — the auto-matcher skips it entirely (manual
	// sends through the API stay available).
	count, err := w.db.CountRoomHandshakes(ctx, room.Address)
	if err != nil {
		return err
	}
	if cap := roomHandshakeCap(len(users)); count >= cap {
		w.log.Info("match worker room skipped: handshake cap",
			"room", room.Address, "handshakes", count, "cap", cap)
		return nil
	}

	// The pair pool: every directed pair of active users with its match
	// score, computed AS the sender (the formula's t is the sender's
	// clock — hours since their last sent handshake, or since their
	// creation when they never sent one). All scores are taken BEFORE the
	// first send of the job, so the ranking is one consistent snapshot.
	pool := make([]matchPair, 0, len(users)*(len(users)-1))
	for i := range users {
		for j := range users {
			if i == j {
				continue
			}
			from, to := users[i], users[j]
			res, err := w.db.MatchPoints(ctx, room.Address, from.Address, to.Address)
			if err != nil {
				return err
			}
			pool = append(pool, matchPair{from: from.Address, to: to.Address, points: res.Points})
		}
	}

	// Highest score first; sender then receiver address break ties.
	sort.Slice(pool, func(a, b int) bool {
		if pool[a].points != pool[b].points {
			return pool[a].points > pool[b].points
		}
		if pool[a].from != pool[b].from {
			return pool[a].from < pool[b].from
		}
		return pool[a].to < pool[b].to
	})

	// Send the best pairs within the budget. An existing directed pair
	// would make the send a no-op, so those are dropped BEFORE the budget
	// cut — the slots go to the best NEW acquaintances.
	budget := pairBudget(len(users))
	sent := 0
	for _, p := range pool {
		if sent == budget {
			break
		}
		exists, err := w.db.HandshakeExists(ctx, p.from, p.to)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		created, err := w.db.CreateHandshake(ctx, &model.Handshake{
			FromAddress: p.from,
			ToAddress:   p.to,
			Status:      model.DefaultStatus,
		})
		if err != nil {
			if errors.Is(err, db.ErrBalance) {
				// The income>outcome rule blocks the sender: skip (they
				// can send again once they receive).
				w.log.Warn("auto handshake blocked by balance",
					"room", room.Address, "from", p.from, "to", p.to)
				continue
			}
			return err
		}
		if created {
			sent++
			w.log.Info("auto handshake sent",
				"room", room.Address, "from", p.from, "to", p.to,
				"points", p.points)
		}
	}
	if sent > 0 {
		w.log.Info("match worker room done", "room", room.Address, "users", len(users), "handshakes", sent)
	}
	return nil
}
