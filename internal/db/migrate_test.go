package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

// TestMigrateBackfillsActiveColumn simulates a pre-feature-#36 database:
// the users table without the active column, holding existing rows. The
// next Open must add the column back and default every existing user to
// active (the feature's default), keeping creation working.
func TestMigrateBackfillsActiveColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a current database with two users, then strip the column to
	// reproduce the legacy schema.
	d := openTestDB(t)
	_ = d
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	room := model.Room{Name: "Legacy Room"}
	if err := database.CreateRoom(ctx, &room, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	for _, name := range []string{"Alice", "Bob"} {
		u := model.User{Name: name}
		if err := database.CreateUser(ctx, room.Address, &u, ""); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE users DROP COLUMN active`); err != nil {
		t.Fatalf("strip active column: %v", err)
	}
	database.Close()

	// Reopen: the migration re-adds the column with the active default.
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	users, err := reopened.ListRoomUsers(ctx, room.Address)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	for _, u := range users {
		if !u.Active {
			t.Fatalf("migrated user %s must be active by default", u.Address)
		}
	}

	// And a fresh user keeps getting the default too.
	u := model.User{Name: "Carol"}
	if err := reopened.CreateUser(ctx, room.Address, &u, ""); err != nil {
		t.Fatalf("create user after migration: %v", err)
	}
	if !u.Active {
		t.Fatalf("new user must be active by default")
	}
}
