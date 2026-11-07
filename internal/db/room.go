package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// CreateRoom inserts a new room with a server-generated address.
func (d *DB) CreateRoom(ctx context.Context, room *model.Room) error {
	now := time.Now().UTC()
	room.CreatedAt = now
	room.UpdatedAt = now

	// Address is generated server-side; retry on the unlikely collision.
	for attempt := 0; attempt < 3; attempt++ {
		room.Address = newAddress(16)
		_, err := d.ExecContext(ctx,
			`INSERT INTO rooms (address, name, description, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, NULL)`,
			room.Address, room.Name, room.Description,
			room.CreatedAt.Format(time.RFC3339), room.UpdatedAt.Format(time.RFC3339),
		)
		if isUnique(err) {
			continue
		}
		return err
	}
	return errors.Join(ErrConflict, errors.New("could not generate unique room address"))
}

// ListRooms returns all non-deleted rooms ordered by address.
func (d *DB) ListRooms(ctx context.Context) ([]model.Room, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT address, name, description, created_at, updated_at, deleted_at
		 FROM rooms WHERE deleted_at IS NULL ORDER BY address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rooms := []model.Room{}
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, r)
	}
	return rooms, rows.Err()
}

// GetRoom returns a single non-deleted room by address.
func (d *DB) GetRoom(ctx context.Context, address string) (model.Room, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, name, description, created_at, updated_at, deleted_at
		 FROM rooms WHERE address = ? AND deleted_at IS NULL`, address)
	room, err := scanRoom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Room{}, errors.Join(ErrNotFound, err)
	}
	return room, err
}

// RoomExists reports whether a non-deleted room with the given address exists.
func (d *DB) RoomExists(ctx context.Context, address string) (bool, error) {
	return d.exists(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE address = ? AND deleted_at IS NULL)`, address)
}

// UpdateRoom updates name and/or description of a non-deleted room.
func (d *DB) UpdateRoom(ctx context.Context, address string, name, description *string) (model.Room, error) {
	n, desc, err := d.currentRoomFields(ctx, address)
	if err != nil {
		return model.Room{}, err
	}
	if name != nil {
		n = *name
	}
	if description != nil {
		desc = *description
	}
	updated := time.Now().UTC()
	res, err := d.ExecContext(ctx,
		`UPDATE rooms SET name = ?, description = ?, updated_at = ? WHERE address = ? AND deleted_at IS NULL`,
		n, desc, updated.Format(time.RFC3339), address)
	if err != nil {
		return model.Room{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return model.Room{}, errors.Join(ErrNotFound, sql.ErrNoRows)
	}
	return d.GetRoom(ctx, address)
}

// DeleteRoom soft-deletes a room and soft-deletes its linked users,
// returning the soft-deleted room.
func (d *DB) DeleteRoom(ctx context.Context, address string) (model.Room, error) {
	now := time.Now().UTC()
	deletedAt := now.Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return model.Room{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE rooms SET deleted_at = ?, updated_at = ? WHERE address = ? AND deleted_at IS NULL`,
		deletedAt, deletedAt, address)
	if err != nil {
		return model.Room{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return model.Room{}, errors.Join(ErrNotFound, sql.ErrNoRows)
	}
	// Soft-delete users linked to the room.
	if _, err = tx.ExecContext(ctx,
		`UPDATE users SET deleted_at = ?, updated_at = ? WHERE room = ? AND deleted_at IS NULL`,
		deletedAt, deletedAt, address); err != nil {
		return model.Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Room{}, err
	}

	room, err := fetchAnyRoom(ctx, d.DB, address)
	if err != nil {
		return model.Room{}, err
	}
	return room, nil
}

// fetchAnyRoom reads a room regardless of its deleted_at state.
func fetchAnyRoom(ctx context.Context, d *sql.DB, address string) (model.Room, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, name, description, created_at, updated_at, deleted_at
		 FROM rooms WHERE address = ?`, address)
	room, err := scanRoom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Room{}, errors.Join(ErrNotFound, err)
	}
	return room, err
}

// currentRoomFields reads the current name/description for partial update.
func (d *DB) currentRoomFields(ctx context.Context, address string) (name, description string, err error) {
	err = d.QueryRowContext(ctx,
		`SELECT name, description FROM rooms WHERE address = ? AND deleted_at IS NULL`, address).
		Scan(&name, &description)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.Join(ErrNotFound, err)
	}
	return name, description, err
}

type scanner interface{ Scan(dest ...any) error }

func scanRoom(s scanner) (model.Room, error) {
	var (
		r         model.Room
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := s.Scan(&r.Address, &r.Name, &r.Description, &createdAt, &updatedAt, &deletedAt); err != nil {
		return model.Room{}, err
	}
	r.CreatedAt = parseTS(createdAt)
	r.UpdatedAt = parseTS(updatedAt)
	r.DeletedAt = parseNullTS(deletedAt)
	return r, nil
}
