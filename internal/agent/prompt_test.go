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
	direct := questionDirect(model.User{Name: "Bob"}, "How do you price a bulk run?", "en")
	if direct == "" {
		t.Fatal("question must produce a directive")
	}
	if !strings.Contains(direct, "Bob") {
		t.Fatal("directive must name the listener")
	}
	if s := questionDirect(model.User{Name: "Bob"}, "No question here.", "en"); s != "" {
		t.Fatalf("statement must not produce a directive, got %q", s)
	}
	if directRU := questionDirect(model.User{Name: "Борис"}, "Как ты оцениваешь свою работу?", "ru"); !strings.Contains(directRU, "ответь") {
		t.Fatalf("Russian directive should be localized, got %q", directRU)
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

func TestRussianPromptsStayLocalized(t *testing.T) {
	speaker := model.User{Name: "Саша", Address: "0xa", PublicBio: "Гончар", PrivateBio: "Любит тишину"}
	listener := model.User{Name: "Глеб", Address: "0xb", PublicBio: "Водитель трамвая"}
	theme := Themes[0]

	main := buildPrompt(theme, model.PhaseNetworking, speaker, listener, nil, 1, false, topicSeed(model.Event{Chat: "0xchat"}, model.PhaseNetworking, 1, 0, false, "ru"), "ru")
	for _, msg := range main {
		if strings.Contains(msg.Content, "You are role-playing") || strings.Contains(msg.Content, "Chat transcript") {
			t.Fatalf("English prompt text leaked into Russian prompt: %q", msg.Content)
		}
	}
	if !strings.Contains(main[0].Content, "Весь текст реплики должен быть на русском языке") ||
		!strings.Contains(main[1].Content, "Необязательная идея") {
		t.Fatalf("Russian prompt missing localized instructions: %#v", main)
	}

	goodbye := buildGoodbyePrompt(theme, speaker, "Что тебе больше всего нравится?", "ru")
	if !strings.Contains(goodbye[1].Content, "Последнее сообщение собеседника") || strings.Contains(goodbye[1].Content, "Farewell direction") {
		t.Fatalf("farewell prompt was not localized: %#v", goodbye)
	}

	transition := buildTransitionPrompt(theme, speaker, listener, nil, "ru")
	if strings.Contains(transition[1].Content, "Chat transcript") || !strings.Contains(transition[1].Content, "Знакомство заканчивается") {
		t.Fatalf("transition prompt was not localized: %#v", transition)
	}
}
