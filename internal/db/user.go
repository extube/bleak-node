package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// CreateUser inserts a user linked to the given room and atomically creates
// the system "welcome" handshake (room -> user, value 10) so the user starts
// with a positive balance. Rooms have a max_user capacity.
func (d *DB) CreateUser(ctx context.Context, roomAddress string, user *model.User) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var roomMax int
	err = tx.QueryRowContext(ctx,
		`SELECT max_user FROM rooms WHERE address = ? AND deleted_at IS NULL`, roomAddress).Scan(&roomMax)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrNotFound, fmt.Errorf("room %q not found", roomAddress))
	}
	if err != nil {
		return err
	}

	// Enforce room capacity.
	var cnt int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE room = ? AND deleted_at IS NULL`, roomAddress).Scan(&cnt); err != nil {
		return err
	}
	if cnt >= roomMax {
		return errors.Join(ErrConflict, fmt.Errorf(
			"room %q is full: %d users of max_user %d", roomAddress, cnt, roomMax))
	}

	now := time.Now().UTC()
	user.Room = roomAddress
	user.CreatedAt = now
	user.UpdatedAt = now

	// Address is generated server-side; retry on the unlikely collision.
	inserted := false
	for attempt := 0; attempt < 3; attempt++ {
		user.Address = newAddress(16)
		_, err = tx.ExecContext(ctx,
			`INSERT INTO users (address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
			user.Address, user.Name, roomAddress, user.PublicBio, user.PrivateBio,
			user.CreatedAt.Format(time.RFC3339), user.UpdatedAt.Format(time.RFC3339),
		)
		if isUnique(err) {
			continue
		}
		if err != nil {
			return err
		}
		inserted = true
		break
	}
	if !inserted {
		return errors.Join(ErrConflict, errors.New("could not generate unique user address"))
	}

	// Welcome handshake: from the room to the new user, value 10.
	if _, err := createSystemHandshake(ctx, tx, roomAddress, user.Address); err != nil {
		return err
	}
	return tx.Commit()
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

// UserByAddress returns a user by address regardless of its room (also
// soft-deleted rows: the agent worker needs the bios even then).
func (d *DB) UserByAddress(ctx context.Context, address string) (model.User, bool, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at
		 FROM users WHERE address = ?`, address)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, false, nil
	}
	if err != nil {
		return model.User{}, false, err
	}
	return user, true, nil
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
