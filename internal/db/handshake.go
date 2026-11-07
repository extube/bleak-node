package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

	hs.CreatedAt = time.Now().UTC()
	_, err := d.ExecContext(ctx,
		`INSERT INTO handshakes (tx_id, from_address, to_address, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		hs.TxID, hs.FromAddress, hs.ToAddress, hs.Status, hs.CreatedAt.Format(time.RFC3339))
	if isUnique(err) {
		return errors.Join(ErrConflict, err)
	}
	return err
}

// GetHandshake returns a handshake by tx_id.
func (d *DB) GetHandshake(ctx context.Context, txID string) (model.Handshake, error) {
	row := d.QueryRowContext(ctx,
		`SELECT tx_id, from_address, to_address, status, created_at FROM handshakes WHERE tx_id = ?`, txID)
	hs, err := scanHandshake(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Handshake{}, errors.Join(ErrNotFound, err)
	}
	return hs, err
}

// ListUserHandshakes returns every handshake where the user is the sender or
// the receiver, newest first.
func (d *DB) ListUserHandshakes(ctx context.Context, address string) ([]model.Handshake, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT tx_id, from_address, to_address, status, created_at FROM handshakes
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

func scanHandshake(s scanner) (model.Handshake, error) {
	var (
		hs        model.Handshake
		createdAt string
	)
	if err := s.Scan(&hs.TxID, &hs.FromAddress, &hs.ToAddress, &hs.Status, &createdAt); err != nil {
		return model.Handshake{}, err
	}
	hs.CreatedAt = parseTS(createdAt)
	return hs, nil
}
