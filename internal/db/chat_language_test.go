package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestChatLanguagePersists(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "language.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(ctx,
		`INSERT INTO chats (address, created_at) VALUES (?, ?)`, "0xchat", now); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	chat, found, err := d.ChatByAddress(ctx, "0xchat")
	if err != nil || !found {
		t.Fatalf("load chat: found=%t err=%v", found, err)
	}
	if chat.Language != "en" {
		t.Fatalf("new chat language = %q, want en", chat.Language)
	}

	if err := d.SetChatLanguage(ctx, "0xchat", " RU "); err != nil {
		t.Fatalf("set language: %v", err)
	}
	chat, found, err = d.ChatByAddress(ctx, "0xchat")
	if err != nil || !found || chat.Language != "ru" {
		t.Fatalf("language did not persist: chat=%+v found=%t err=%v", chat, found, err)
	}

	if err := d.SetChatLanguage(ctx, "0xchat", "fr"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported language error = %v, want ErrInvalid", err)
	}
	if err := d.SetChatLanguage(ctx, "missing", "ru"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat error = %v, want ErrNotFound", err)
	}
}
