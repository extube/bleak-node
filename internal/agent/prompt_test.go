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

// TestRoomLanguageDominatesPersonaLanguage: the ROOM language rules the
// dialog no matter what language the persona data is written in — a
// "Foreigner" (English name/greeting/bios) in a Russian room must be
// instructed to write Russian, and the mirror case for English rooms
// (issue #52).
func TestRoomLanguageDominatesPersonaLanguage(t *testing.T) {
	foreigner := model.User{Name: "Foreigner", Address: "0xa", PublicBio: "Loves chess", PrivateBio: "New in town"}
	local := model.User{Name: "Аня", Address: "0xb", PublicBio: "Гончар"}
	theme := Themes[0]

	ruPrompt := buildPrompt(theme, model.PhaseNetworking, foreigner, local, nil, 1, false, "", "ru")
	if !strings.Contains(ruPrompt[0].Content, "независимо от языка имени, приветствия или биографий") {
		t.Fatalf("Russian prompt missing the language-dominance rule: %q", ruPrompt[0].Content)
	}
	if !strings.Contains(ruPrompt[0].Content, "Весь текст реплики должен быть на русском языке") {
		t.Fatalf("Russian prompt lost its base language instruction: %q", ruPrompt[0].Content)
	}

	enPrompt := buildPrompt(theme, model.PhaseNetworking, local, foreigner, nil, 1, false, "", "en")
	if !strings.Contains(enPrompt[0].Content, "regardless of the language of the name, greeting, or bios") {
		t.Fatalf("English prompt missing the language-dominance rule: %q", enPrompt[0].Content)
	}

	cases := []struct{ lang, want string }{
		{"ru", "независимо от языка имени, приветствия или биографий"},
		{"en", "regardless of the language of the name, greeting, or bios"},
	}
	for _, c := range cases {
		goodbye := buildGoodbyePrompt(theme, foreigner, "Как дела?", c.lang)
		if !strings.Contains(goodbye[0].Content, c.want) {
			t.Fatalf("%s farewell prompt missing the language-dominance rule: %q", c.lang, goodbye[0].Content)
		}
		transition := buildTransitionPrompt(theme, foreigner, local, nil, c.lang)
		if !strings.Contains(transition[0].Content, c.want) {
			t.Fatalf("%s transition prompt missing the language-dominance rule: %q", c.lang, transition[0].Content)
		}
	}
}
