package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/extube/bleak-node/internal/model"
)

// ValidHandshakeStatuses is the allowed set for handshake.status.
var ValidHandshakeStatuses = map[int]struct{}{0: {}, 1: {}, 2: {}, 3: {}, 4: {}}

// IsValidStatus reports whether status belongs to {0,1,2,3,4}.
func IsValidStatus(s int) bool {
	_, ok := ValidHandshakeStatuses[s]
	return ok
}

// CreateHandshake registers a handshake between two existing users with status in [0..4].
func (d *DB) CreateHandshake(ctx context.Context, hs *model.Handshake) error {
	if _, ok := ValidHandshakeStatuses[hs.Status]; !ok {
		return errors.Join(ErrInvalid, fmt.Errorf("status %d is not in {0,1,2,3,4}", hs.Status))
	}

	for _, addr := range []string{hs.FromAddress, hs.ToAddress} {
		ok, err := d.UserExists(ctx, addr)
		if err != nil {
			return err
		}
		if !ok {
			return errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}

	_, err := d.ExecContext(ctx,
		`INSERT INTO handshakes (tx_id, from_address, to_address, status) VALUES (?, ?, ?, ?)`,
		hs.TxID, hs.FromAddress, hs.ToAddress, hs.Status)
	if isUnique(err) {
		return errors.Join(ErrConflict, err)
	}
	return err
}

// GetHandshake returns a handshake by tx_id.
func (d *DB) GetHandshake(ctx context.Context, txID string) (model.Handshake, error) {
	row := d.QueryRowContext(ctx,
		`SELECT tx_id, from_address, to_address, status FROM handshakes WHERE tx_id = ?`, txID)
	var hs model.Handshake
	err := row.Scan(&hs.TxID, &hs.FromAddress, &hs.ToAddress, &hs.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Handshake{}, errors.Join(ErrNotFound, err)
	}
	return hs, err
}

// UpdateHandshakeStatus updates the status of an existing handshake.
func (d *DB) UpdateHandshakeStatus(ctx context.Context, txID string, newStatus int) (model.Handshake, error) {
	if _, ok := ValidHandshakeStatuses[newStatus]; !ok {
		return model.Handshake{}, errors.Join(ErrInvalid, fmt.Errorf("status %d is not in {0,1,2,3,4}", newStatus))
	}
	res, err := d.ExecContext(ctx, `UPDATE handshakes SET status = ? WHERE tx_id = ?`, newStatus, txID)
	if err != nil {
		return model.Handshake{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return model.Handshake{}, errors.Join(ErrNotFound, sql.ErrNoRows)
	}
	return d.GetHandshake(ctx, txID)
}
