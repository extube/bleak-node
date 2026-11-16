// tokenctl is the operator CLI for the ownership-credential pool and the
// legacy migration (feature #33, CONCEPT.md §7.1/§7.4).
//
// Subcommands:
//
//	tokenctl mint     — mint a one-time room-creation token: the raw
//	                    secret is printed ONCE, only its SHA-256 hash is
//	                    stored in the create_tokens table.
//	tokenctl migrate  — legacy migration of existing test data: every room
//	                    and persona gets a random token (hash stored, raw
//	                    appended to a gitignored file), every persona an
//	                    initial live mutual-code hash, every room one
//	                    default invite code. After the run, gating is
//	                    universal.
//
// The database path comes from --db, $BLEAK_NODE_DB, or bleak-node.db.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/extube/bleak-node/internal/db"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()

	switch os.Args[1] {
	case "mint":
		fs := flag.NewFlagSet("mint", flag.ExitOnError)
		runMint(ctx, fs)
	case "migrate":
		fs := flag.NewFlagSet("migrate", flag.ExitOnError)
		runMigrate(ctx, fs)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tokenctl mint|migrate [--db path] [flags]")
	os.Exit(2)
}

// openFor opens the database from the flagset's -db flag (or the env /
// default) and registers a cleanup.
func openFor(ctx context.Context, fs *flag.FlagSet) *db.DB {
	dbPath := fs.String("db", envOr("BLEAK_NODE_DB", "bleak-node.db"), "SQLite database path")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	database, err := db.Open(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	return database
}

// runMint prints the raw creation token once and stores only its hash.
func runMint(ctx context.Context, fs *flag.FlagSet) {
	database := openFor(ctx, fs)
	defer database.Close()

	raw := newSecret()
	if err := database.MintCreateToken(ctx, db.HashToken(raw)); err != nil {
		fmt.Fprintf(os.Stderr, "mint creation token: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("One-time room-creation token (print once, store safely; NOT recoverable):")
	fmt.Println(raw)
}

// runMigrate migrates every existing room and persona to the ownership
// token world: tokens, initial live mutual codes, one invite per room.
func runMigrate(ctx context.Context, fs *flag.FlagSet) {
	outPath := fs.String("out", "data/legacy-tokens.txt", "gitignored file for the raw secrets")
	database := openFor(ctx, fs)
	defer database.Close()

	if dir := filepath.Dir(*outPath); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
			os.Exit(1)
		}
	}
	f, err := os.OpenFile(*outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	defer f.Close()
	line := func(format string, args ...any) {
		fmt.Fprintf(f, format+"\n", args...)
	}

	rooms, err := database.ListRooms(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list rooms: %v\n", err)
		os.Exit(1)
	}

	for _, room := range rooms {
		// Room token.
		raw := newSecret()
		if err := database.SetRoomTokenHash(ctx, room.Address, db.HashToken(raw)); err != nil {
			fmt.Fprintf(os.Stderr, "room %s token: %v\n", room.Address, err)
			os.Exit(1)
		}
		line("room %s\t%s", room.Address, raw)

		// One default invite code per room (24 h).
		inv, err := database.CreateRoomInvite(ctx, room.Address,
			time.Now().UTC().Add(db.DefaultInviteExpiry))
		if err != nil {
			fmt.Fprintf(os.Stderr, "room %s invite: %v\n", room.Address, err)
			os.Exit(1)
		}
		line("invite %s\t%s", room.Address, inv.Code)

		// Persona tokens + initial live mutual codes.
		users, err := database.ListRoomUsers(ctx, room.Address)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list users of %s: %v\n", room.Address, err)
			os.Exit(1)
		}
		for _, user := range users {
			raw := newSecret()
			if err := database.SetUserTokenHashByAddress(ctx, user.Address, db.HashToken(raw)); err != nil {
				fmt.Fprintf(os.Stderr, "user %s token: %v\n", user.Address, err)
				os.Exit(1)
			}
			line("user %s\t%s", user.Address, raw)

			code := newSecret()
			if err := database.SetUserMutualCodeHashByAddress(ctx, user.Address, db.HashToken(code)); err != nil {
				fmt.Fprintf(os.Stderr, "user %s mutual code: %v\n", user.Address, err)
				os.Exit(1)
			}
			line("mutual-code %s\t%s", user.Address, code)
		}
	}
	fmt.Printf("migration done: %d rooms; raw secrets appended to %s (gitignored)\n",
		len(rooms), *outPath)
}

// newSecret generates a fresh raw secret: 32 random bytes, base64url
// (CONCEPT.md §7.4 recommended token shape).
func newSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "rand: %v\n", err)
		os.Exit(1)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
