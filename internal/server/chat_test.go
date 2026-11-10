package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

func TestSetChatLanguage(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO chats (address, created_at) VALUES (?, ?)`, "0xchat", now); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/chat/0xchat/language", strings.NewReader(`{"language":"ru"}`))
	response := httptest.NewRecorder()
	New(database, nil).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var chat model.Chat
	if err := json.Unmarshal(response.Body.Bytes(), &chat); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if chat.Language != "ru" {
		t.Fatalf("language = %q, want ru", chat.Language)
	}
}
