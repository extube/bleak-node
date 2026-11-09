package agent

import (
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

func TestLastListenerMessage(t *testing.T) {
	listener := model.User{Address: "0xb", Name: "Bob"}
	speaker := model.User{Address: "0xa", Name: "Alice"}
	msgs := []model.Message{
		{From: "0xb", Content: "first from listener"},
		{From: "0xa", Content: "reply from speaker"},
		{From: "0xb", Content: "second from listener"},
	}
	last, ok := lastListenerMessage(listener, msgs)
	if !ok || last != "second from listener" {
		t.Fatalf("got %q ok=%t", last, ok)
	}
	// When another address is passed, its own most recent message wins.
	other, ok := lastListenerMessage(speaker, msgs)
	if !ok || other != "reply from speaker" {
		t.Fatalf("got %q ok=%t", other, ok)
	}
	if _, ok := lastListenerMessage(listener, nil); ok {
		t.Fatal("empty transcript must report false")
	}
}

func TestQuestionDirect(t *testing.T) {
	direct := questionDirect(model.User{Name: "Bob"}, "How do you price a bulk run?")
	if direct == "" {
		t.Fatal("question must produce a directive")
	}
	if !strings.Contains(direct, "Bob") {
		t.Fatal("directive must name the listener")
	}
	if s := questionDirect(model.User{Name: "Bob"}, "No question here."); s != "" {
		t.Fatalf("statement must not produce a directive, got %q", s)
	}
}

func TestQuestionForFarewell(t *testing.T) {
	if got := questionForFarewell("What do you do for fun?"); got == "" {
		t.Fatal("open question must ride along to the farewell prompt")
	}
	if got := questionForFarewell("It was fun."); got != "" {
		t.Fatalf("statement must stay out, got %q", got)
	}
	if got := questionForFarewell("  "); got != "" {
		t.Fatalf("empty message must stay out, got %q", got)
	}
}
