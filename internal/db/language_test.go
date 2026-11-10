package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

// TestRoomLanguageFlowsDown checks the single-language room rule end to
// end: users and chats inherit the room language, and messages in any
// other language are declined.
func TestRoomLanguageFlowsDown(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "lang.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// A Russian room: everything inside it must be Russian.
	room := model.Room{Name: "Русная комната", Language: "ru"}
	if err := database.CreateRoom(ctx, &room); err != nil {
		t.Fatalf("create room: %v", err)
	}

	userA := model.User{Name: "Аня"}
	if err := database.CreateUser(ctx, room.Address, &userA); err != nil {
		t.Fatalf("create user A: %v", err)
	}
	if userA.Language != "ru" {
		t.Fatalf("user language = %q, want ru (inherited from the room)", userA.Language)
	}
	userB := model.User{Name: "Борис"}
	if err := database.CreateUser(ctx, room.Address, &userB); err != nil {
		t.Fatalf("create user B: %v", err)
	}

	// Mutually complete the handshakes: the chat appears automatically.
	if _, err := database.CreateHandshake(ctx, &model.Handshake{
		FromAddress: userA.Address, ToAddress: userB.Address, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake A->B: %v", err)
	}
	if _, err := database.CreateHandshake(ctx, &model.Handshake{
		FromAddress: userB.Address, ToAddress: userA.Address, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake B->A: %v", err)
	}

	chats, err := database.ListUserChats(ctx, userA.Address)
	if err != nil || len(chats) == 0 {
		t.Fatalf("list chats: %v (n=%d)", err, len(chats))
	}
	if chats[0].Language != "ru" {
		t.Fatalf("chat language = %q, want ru (inherited from the room)", chats[0].Language)
	}

	// A Russian message is accepted...
	ruMsg := &model.Message{From: userA.Address, To: userB.Address, Content: "Привет, Борис!"}
	if err := database.CreateMessage(ctx, ruMsg); err != nil {
		t.Fatalf("create ru message: %v", err)
	}
	// ...an English one is declined.
	enMsg := &model.Message{From: userB.Address, To: userA.Address, Content: "Hello there!"}
	if err := database.CreateMessage(ctx, enMsg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("en message error = %v, want ErrInvalid", err)
	}

	// Greeting regeneration keeps the room language through the user row.
	updated, err := database.SetUserGreeting(ctx, room.Address, userA.Address, "Привет, я Аня.")
	if err != nil {
		t.Fatalf("set greeting: %v", err)
	}
	if updated.Language != "ru" {
		t.Fatalf("updated user language = %q, want ru", updated.Language)
	}
}
