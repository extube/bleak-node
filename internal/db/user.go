package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// CreateUser inserts a user linked to the given room.
func (d *DB) CreateUser(ctx context.Context, roomAddress string, user *model.User) error {
	ok, err := d.RoomExists(ctx, roomAddress)
	if err != nil {
		return err
	}
	if !ok {
		return errors.Join(ErrNotFound, fmt.Errorf("room %q not found", roomAddress))
	}

	now := time.Now().UTC()
	user.Room = roomAddress
	user.CreatedAt = now
	user.UpdatedAt = now

	_, err = d.ExecContext(ctx,
		`INSERT INTO users (address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		user.Address, user.Name, roomAddress, user.PublicBio, user.PrivateBio,
		user.CreatedAt.Format(time.RFC3339), user.UpdatedAt.Format(time.RFC3339),
	)
	if isUnique(err) {
		return errors.Join(ErrConflict, err)
	}
	return err
}

// ListRoomUsers returns all non-deleted users of a room.
func (d *DB) ListRoomUsers(ctx context.Context, roomAddress string) ([]model.User, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at
		 FROM users WHERE room = ? AND deleted_at IS NULL ORDER BY address`, roomAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetUser returns a single non-deleted user inside the given room.
func (d *DB) GetUser(ctx context.Context, roomAddress, address string) (model.User, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at
		 FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL`, address, roomAddress)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, errors.Join(ErrNotFound, err)
	}
	return user, err
}

// UserExists reports whether a non-deleted user with the given address exists.
func (d *DB) UserExists(ctx context.Context, address string) (bool, error) {
	return d.exists(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND deleted_at IS NULL)`, address)
}

// UpdateUser updates name/bios of a non-deleted user inside the given room.
func (d *DB) UpdateUser(ctx context.Context, roomAddress, address string, name, publicBio, privateBio *string) (model.User, error) {
	cur, err := d.GetUser(ctx, roomAddress, address)
	if err != nil {
		return model.User{}, err
	}
	if name != nil {
		cur.Name = *name
	}
	if publicBio != nil {
		cur.PublicBio = *publicBio
	}
	if privateBio != nil {
		cur.PrivateBio = *privateBio
	}
	cur.UpdatedAt = time.Now().UTC()

	_, err = d.ExecContext(ctx,
		`UPDATE users SET name = ?, public_bio = ?, private_bio = ?, updated_at = ?
		 WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		cur.Name, cur.PublicBio, cur.PrivateBio, cur.UpdatedAt.Format(time.RFC3339), address, roomAddress)
	if err != nil {
		return model.User{}, err
	}
	return cur, nil
}

// DeleteUser soft-deletes a user inside the given room.
func (d *DB) DeleteUser(ctx context.Context, roomAddress, address string) (model.User, error) {
	user, err := d.GetUser(ctx, roomAddress, address)
	if err != nil {
		return model.User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = d.ExecContext(ctx,
		`UPDATE users SET deleted_at = ?, updated_at = ? WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		now, now, address, roomAddress)
	if err != nil {
		return model.User{}, err
	}
	deletedAt := time.Now().UTC()
	user.UpdatedAt = deletedAt
	user.DeletedAt = &deletedAt
	return user, nil
}

func scanUser(s scanner) (model.User, error) {
	var (
		u         model.User
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := s.Scan(&u.Address, &u.Name, &u.Room, &u.PublicBio, &u.PrivateBio, &createdAt, &updatedAt, &deletedAt); err != nil {
		return model.User{}, err
	}
	u.CreatedAt = parseTS(createdAt)
	u.UpdatedAt = parseTS(updatedAt)
	u.DeletedAt = parseNullTS(deletedAt)
	return u, nil
}
