package db

import (
	"context"
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

// CreateHandshake registers a handshake between two existing users with status
// in [0..4] and a server-generated tx_id.
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

	// tx_id is generated server-side; retry on the unlikely collision.
	for attempt := 0; attempt < 3; attempt++ {
		hs.TxID = newTxID()
		_, err := d.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, status, created_at) VALUES (?, ?, ?, ?, ?)`,
			hs.TxID, hs.FromAddress, hs.ToAddress, hs.Status, hs.CreatedAt.Format(time.RFC3339))
		if isUnique(err) {
			continue
		}
		return err
	}
	return errors.Join(ErrConflict, errors.New("could not generate unique handshake tx_id"))
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
