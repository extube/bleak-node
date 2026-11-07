package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// ValidHandshakeStatuses is the allowed set for handshake.status (0 is restricted).
var ValidHandshakeStatuses = map[int]struct{}{1: {}, 2: {}, 3: {}, 4: {}}

// IsValidStatus reports whether status belongs to {1,2,3,4}.
func IsValidStatus(s int) bool {
	_, ok := ValidHandshakeStatuses[s]
	return ok
}

// CreateHandshake registers a user-to-user handshake with status in [1..4]
// and a server-generated tx_id. A handshake pair (from, to) is unique:
// when it already exists nothing is created and the live handshake is
// returned without error (also without balance checks). Otherwise the
// handshake is only accepted when the sender's balance (income - outcome)
// is positive; the value is fixed at 1.
//
// The bool result reports whether a new handshake was created.
func (d *DB) CreateHandshake(ctx context.Context, hs *model.Handshake) (bool, error) {
	if _, ok := ValidHandshakeStatuses[hs.Status]; !ok {
		return false, errors.Join(ErrInvalid, fmt.Errorf("status %d is not in {1,2,3,4}", hs.Status))
	}

	for _, addr := range []string{hs.FromAddress, hs.ToAddress} {
		ok, err := d.UserExists(ctx, addr)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}

	// Everything below runs in one transaction: the handshake insert and the
	// automatic chat creation are atomic (single-writer pool makes the tx safe).
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Idempotent: an existing directed pair is returned as-is, no error,
	// no balance impact.
	existing, found, err := findHandshakeOn(ctx, tx, hs.FromAddress, hs.ToAddress)
	if err != nil {
		return false, err
	}
	if found {
		*hs = existing
		return false, nil
	}

	// Income > outcome required to send.
	balance, err := handshakeBalanceOn(ctx, tx, hs.FromAddress)
	if err != nil {
		return false, err
	}
	if balance <= 0 {
		return false, errors.Join(ErrBalance, fmt.Errorf(
			"user %q balance is %d: income must exceed outcome to send a handshake",
			hs.FromAddress, balance))
	}

	hs.Value = model.HandshakeValue
	hs.CreatedAt = time.Now().UTC()

	// tx_id is generated server-side; retry on the unlikely collision.
	created := false
	for attempt := 0; attempt < 3; attempt++ {
		hs.TxID = newTxID()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, status, value, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			hs.TxID, hs.FromAddress, hs.ToAddress, hs.Status, hs.Value, hs.CreatedAt.Format(time.RFC3339))
		if isUnique(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		created = true
		break
	}
	if !created {
		return false, errors.Join(ErrConflict, errors.New("could not generate unique handshake tx_id"))
	}

	// Chat contract: once both directions (A -> B and B -> A) exist, the chat
	// with its participants is created automatically.
	both, err := bothHandshakeDirections(ctx, tx, hs.FromAddress, hs.ToAddress)
	if err != nil {
		return false, err
	}
	if both {
		if _, err := ensureChat(ctx, tx, hs.FromAddress, hs.ToAddress); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return created, nil
}

// findHandshakeOn runs the pair lookup on the given executor.
func findHandshakeOn(ctx context.Context, q rowQuerier, from, to string) (model.Handshake, bool, error) {
	row := q.QueryRowContext(ctx,
		`SELECT tx_id, from_address, to_address, status, value, created_at
		 FROM handshakes WHERE from_address = ? AND to_address = ?`, from, to)
	hs, err := scanHandshake(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Handshake{}, false, nil
	}
	if err != nil {
		return model.Handshake{}, false, err
	}
	return hs, true, nil
}

// handshakeBalanceOn computes income - outcome on the given executor.
func handshakeBalanceOn(ctx context.Context, q rowQuerier, address string) (int64, error) {
	const q2 = `
SELECT
	(SELECT COALESCE(SUM(value), 0) FROM handshakes WHERE to_address = ?) -
	(SELECT COALESCE(SUM(value), 0) FROM handshakes WHERE from_address = ?)`
	var balance int64
	if err := q.QueryRowContext(ctx, q2, address, address).Scan(&balance); err != nil {
		return 0, err
	}
	return balance, nil
}

// createSystemHandshake inserts the welcome handshake from a room to a new
// user on the given executor (tx or db), bypassing the balance rule
// (the room acts as the faucet).
func createSystemHandshake(ctx context.Context, q execContext, fromRoom, toUser string) (model.Handshake, error) {
	hs := model.Handshake{
		FromAddress: fromRoom,
		ToAddress:   toUser,
		Status:      model.DefaultStatus,
		Value:       model.FirstHandshakeValue,
		CreatedAt:   time.Now().UTC(),
	}
	for attempt := 0; attempt < 3; attempt++ {
		hs.TxID = newTxID()
		_, err := q.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, status, value, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			hs.TxID, hs.FromAddress, hs.ToAddress, hs.Status, hs.Value, hs.CreatedAt.Format(time.RFC3339))
		if isUnique(err) {
			continue
		}
		return hs, err
	}
	return model.Handshake{}, errors.Join(ErrConflict, errors.New("could not generate unique handshake tx_id"))
}

// ListUserHandshakes returns every handshake where the user is the sender or
// the receiver, newest first.
func (d *DB) ListUserHandshakes(ctx context.Context, address string) ([]model.Handshake, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT tx_id, from_address, to_address, status, value, created_at FROM handshakes
		 WHERE from_address = ? OR to_address = ?
		 ORDER BY created_at DESC, tx_id ASC`, address, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Handshake{}
	for rows.Next() {
		hs, err := scanHandshake(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, hs)
	}
	return out, rows.Err()
}

// execContext is the subset of *sql.DB / *sql.Tx used by helpers.
type execContext interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func scanHandshake(s scanner) (model.Handshake, error) {
	var (
		hs        model.Handshake
		createdAt string
	)
	if err := s.Scan(&hs.TxID, &hs.FromAddress, &hs.ToAddress, &hs.Status, &hs.Value, &createdAt); err != nil {
		return model.Handshake{}, err
	}
	hs.CreatedAt = parseTS(createdAt)
	return hs, nil
}
