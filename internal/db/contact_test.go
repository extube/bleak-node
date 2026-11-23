package db

import (
	"context"
	"errors"
	"testing"
)

func TestShareContact(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	contact, err := d.ShareContact(ctx, "0xa", "0xb", "telegram: @alice")
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if contact.From != "0xa" || contact.To != "0xb" || contact.Content != "telegram: @alice" {
		t.Fatalf("contact wrong: %+v", contact)
	}
	if contact.CreatedAt.IsZero() {
		t.Fatal("created_at missing")
	}

	// The (from, to) pair is unique: a repeated share is ErrConflict.
	if _, err := d.ShareContact(ctx, "0xa", "0xb", "another detail"); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated share: want ErrConflict, got %v", err)
	}
	// The reverse direction is a distinct pair and still allowed.
	if _, err := d.ShareContact(ctx, "0xb", "0xa", "telegram: @bob"); err != nil {
		t.Fatalf("reverse share: %v", err)
	}

	// Validation: same user, empty content, unknown user.
	if _, err := d.ShareContact(ctx, "0xa", "0xa", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same user: want ErrInvalid, got %v", err)
	}
	if _, err := d.ShareContact(ctx, "0xa", "0xb", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty content: want ErrInvalid, got %v", err)
	}
	if _, err := d.ShareContact(ctx, "0xa", "0xunknown", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: want ErrNotFound, got %v", err)
	}

	// Only the recipient reads: 0xb sees exactly the a -> b row.
	toB, err := d.ListContactsTo(ctx, "0xb")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(toB) != 1 || toB[0].From != "0xa" || toB[0].To != "0xb" {
		t.Fatalf("0xb contacts wrong: %+v", toB)
	}
	// 0xa sees exactly its own incoming row (from 0xb), never the outgoing.
	toA, err := d.ListContactsTo(ctx, "0xa")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(toA) != 1 || toA[0].From != "0xb" {
		t.Fatalf("0xa contacts wrong: %+v", toA)
	}
	if _, err := d.ListContactsTo(ctx, "0xunknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown recipient: want ErrNotFound, got %v", err)
	}
}

func TestCompleteTurnMarksChatDone(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ev := queuedEvent(t, d, "0xchat", "0xa", "0xb")
	ctx := context.Background()

	// The farewell turn (no next event, done=true) stamps the chat done.
	if _, err := d.CompleteTurn(ctx, ev.ID,
		[]TurnBubble{
			{From: "0xa", To: "0xb", Contents: []string{"Goodbye, it was nice."}},
			{From: "0xb", To: "0xa", Contents: []string{"Bye, take care!"}},
		},
		nil, nil, true); err != nil {
		t.Fatalf("farewell turn: %v", err)
	}

	chat, found, err := d.ChatByAddress(ctx, "0xchat")
	if err != nil || !found {
		t.Fatalf("chat: found=%t err=%v", found, err)
	}
	if !chat.Done {
		t.Fatal("farewell turn must stamp chats.done = true")
	}

	// A mid-chat turn (next event queued, done=false) leaves it untouched.
	ev2 := queuedEvent(t, d, "0xchat", "0xb", "0xa")
	if _, err := d.CompleteTurn(ctx, ev2.ID,
		[]TurnBubble{{From: "0xb", To: "0xa", Contents: []string{"A new thought."}}},
		nil, &TurnNext{Speaker: "0xa", Listener: "0xb"}, false); err != nil {
		t.Fatalf("mid turn: %v", err)
	}
	if chat, _, _ := d.ChatByAddress(ctx, "0xchat"); !chat.Done {
		t.Fatal("done flag must survive unrelated turns")
	}
}
