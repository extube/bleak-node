// Package matcher runs the auto-matchmaking worker (feature #36): once
// every 10 minutes it ranks the active personas of every live room and
// automatically sends the top handshakes.
//
// One job:
//
//  1. fetch every room with at least one active invite code (not revoked,
//     not expired) — rooms host live events, the rest is idle;
//  2. fetch the room's ACTIVE users (active = true, not deleted);
//  3. build the matching-point matrix: for every active user X the score
//     of X against every other active user Y, computed by the match-point
//     function of POST /api/match — 100 * (1 - 1/(C * 2^t)) with t the
//     hours since X's creation;
//  4. for every X: skip the users X already shook hands with, order the
//     rest by points (desc, address asc for determinism) and send the
//     handshake X → Y for the top 3. Sending goes through the regular
//     handshake path (idempotent per directed pair, balance rule intact,
//     auto-chat on mutuality).
package matcher

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// Job tuning.
const (
	// Interval is how often the worker performs one matching job.
	Interval = 10 * time.Minute
	// TopK is how many handshakes one user receives per job at most.
	TopK = 3
)

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

// candidate is one row of a user's matrix: a possible handshake target
// with its match points.
type candidate struct {
	address string
	points  float64
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

// matchRoom builds the matching-point matrix of one room's active users
// and sends the top-3 handshakes per user.
func (w *Worker) matchRoom(ctx context.Context, room model.Room) error {
	users, err := w.db.ListActiveRoomUsers(ctx, room.Address)
	if err != nil {
		return err
	}
	if len(users) < 2 {
		return nil
	}

	sent := 0
	for i := range users {
		from := users[i]

		// Matrix row of `from`: the match score against every other active
		// user of the room (the score is computed AS `from` — the formula's
		// t is the hours since their creation).
		row := make([]candidate, 0, len(users)-1)
		for j := range users {
			to := users[j]
			if to.Address == from.Address {
				continue
			}
			res, err := w.db.MatchPoints(ctx, room.Address, from.Address, to.Address)
			if err != nil {
				return err
			}
			row = append(row, candidate{address: to.Address, points: res.Points})
		}

		// Highest score first; the address breaks ties deterministically.
		sort.Slice(row, func(a, b int) bool {
			if row[a].points != row[b].points {
				return row[a].points > row[b].points
			}
			return row[a].address < row[b].address
		})

		// Top-K among the users `from` has no handshake with yet: re-sending
		// an existing directed pair is a no-op, so the K slots go to the best
		// NEW acquaintances.
		targets := make([]candidate, 0, TopK)
		for _, c := range row {
			if len(targets) == TopK {
				break
			}
			exists, err := w.db.HandshakeExists(ctx, from.Address, c.address)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			targets = append(targets, c)
		}

		for _, c := range targets {
			created, err := w.db.CreateHandshake(ctx, &model.Handshake{
				FromAddress: from.Address,
				ToAddress:   c.address,
				Status:      model.DefaultStatus,
			})
			if err != nil {
				if errors.Is(err, db.ErrBalance) {
					// The income>outcome rule blocks the sender: skip (they
					// can send again once they receive).
					w.log.Warn("auto handshake blocked by balance",
						"room", room.Address, "from", from.Address, "to", c.address)
					continue
				}
				return err
			}
			if created {
				sent++
				w.log.Info("auto handshake sent",
					"room", room.Address, "from", from.Address, "to", c.address,
					"points", c.points)
			}
		}
	}
	if sent > 0 {
		w.log.Info("match worker room done", "room", room.Address, "users", len(users), "handshakes", sent)
	}
	return nil
}
