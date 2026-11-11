package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

// fakeGreetLLM scripts the answers of the greeting LLM.
type fakeGreetLLM struct {
	answers []string
	calls   int
}

func (f *fakeGreetLLM) Complete(ctx context.Context, prompt []llmMessage) (string, error) {
	if f.calls < len(f.answers) {
		out := f.answers[f.calls]
		f.calls++
		return out, nil
	}
	return "", nil
}

func TestGenerateGreetingNoQuestions(t *testing.T) {
	// The first answer carries a question: its statement part survives
	// (the question sentence is dropped), so the greeting is salvaged
	// without the question.
	g := &Greeter{llm: &fakeGreetLLM{answers: []string{
		"Hi, I'm Mark - antique clocks. What brings you here?",
		"Hi, I'm Mark - I repair antique clocks on the market square.",
	}}}
	got := g.GenerateGreeting("Mark", "repairs clocks", "", "en")
	if strings.ContainsRune(got, '?') {
		t.Fatalf("greeting contains a question: %q", got)
	}
	if got != "Hi, I'm Mark - antique clocks." {
		t.Fatalf("greeting = %q, want the salvaged statement", got)
	}
}

func TestGenerateGreetingFallsBackWhenAllAnswersAsk(t *testing.T) {
	// Every draft keeps a question: the bio-based template takes over and
	// stays question-free.
	g := &Greeter{llm: &fakeGreetLLM{answers: []string{
		"Hi, I'm Mark - how are you?",
		"Hello! What's up?",
		"Hey there, who's asking?",
	}}}
	got := g.GenerateGreeting("Mark", "Mark repairs antique clocks.", "", "en")
	if strings.ContainsRune(got, '?') || got == "" {
		t.Fatalf("fallback greeting = %q, want non-empty without questions", got)
	}
	if !strings.Contains(got, "Mark") {
		t.Fatalf("fallback greeting = %q, want the user name", got)
	}
}

func TestGenerateGreetingRussian(t *testing.T) {
	g := &Greeter{llm: &fakeGreetLLM{answers: []string{
		"Привет, я Марк — ремонтирую антикварные часы. Как дела?",
		"Привет, я Марк — ремонтирую антикварные часы на рыночной площади.",
	}}}
	got := g.GenerateGreeting("Марк", "ремонтирует часы", "", "ru")
	if strings.ContainsRune(got, '?') {
		t.Fatalf("greeting contains a question: %q", got)
	}
}

func TestFallbackGreeting(t *testing.T) {
	en := FallbackGreeting("Mark", "Mark repairs antique clocks.", false)
	if strings.ContainsRune(en, '?') || !strings.Contains(en, "Mark") {
		t.Fatalf("en fallback = %q", en)
	}
	ru := FallbackGreeting("Марк", "", true)
	if ru != "Привет, я Марк." {
		t.Fatalf("ru fallback = %q", ru)
	}
}

func TestGreetingOrFallback(t *testing.T) {
	legacy := model.User{Name: "Sofia", PublicBio: "Sofia is a calligrapher."}
	got := greetingOrFallback(legacy, "en")
	if got == "" || strings.ContainsRune(got, '?') {
		t.Fatalf("legacy user greeting = %q", got)
	}
	stored := model.User{Name: "Sofia", Greeting: "Hello there."}
	if got := greetingOrFallback(stored, "ru"); got != "Hello there." {
		t.Fatalf("stored greeting = %q, want the stored text", got)
	}
}

func TestBuildPromptFirstGeneratedTurn(t *testing.T) {
	var theme Theme
	speaker := model.User{Address: "0xa", Name: "Sofia", Greeting: "Hi, I'm Sofia — calligrapher."}
	listener := model.User{Address: "0xb", Name: "Mark", Greeting: "Hi, I'm Mark — clock repairer."}
	msgs := []model.Message{
		{From: "0xa", Content: speaker.Greeting},
		{From: "0xb", Content: listener.Greeting},
	}
	prompt := buildPrompt(theme, "networking", speaker, listener, msgs, 1, false, "", "en")
	body := prompt[1].Content
	if !strings.Contains(body, "greeting messages have been exchanged") {
		t.Fatalf("first generated turn prompt misses the greeting note:\n%s", body)
	}
	if strings.Contains(body, "INTRO") || strings.Contains(body, "introduce yourself to") {
		t.Fatalf("first generated turn prompt must not re-introduce:\n%s", body)
	}
	// A later turn (3 messages) goes back to the normal networking prompt.
	prompt = buildPrompt(theme, "networking", listener, speaker, append(msgs, model.Message{From: "0xa", Content: "So, what's new?"}), 1, false, "", "en")
	if strings.Contains(prompt[1].Content, "greeting messages have been exchanged") {
		t.Fatal("later turn must use the normal networking prompt")
	}
}

func TestFitSentences(t *testing.T) {
	// A long greeting truncated by the 255-symbol cap loses its dangling
	// last words: only complete sentences survive.
	long := "Привет! Я Кирилл, пекарь-кондитер. Держу маленькую пекарню с авторскими пирогами — заходи, у нас всегда пахнет ванилью и корицей, а по выходным печём что-то особенное, например медовик с заварным кремом или пирог с лесными ягодами, который особенно нравится постоянным гостям пекарни."
	got := fitSentences(sanitizeContent(long))
	if got == "" || strings.ContainsRune(got, '?') {
		t.Fatalf("fit = %q", got)
	}
	last := []rune(got)[len([]rune(got))-1]
	if !strings.ContainsRune(".!", last) {
		t.Fatalf("fit does not end with a sentence ender: %q", got)
	}
	if len([]rune(got)) > model.MaxMessageContentLen {
		t.Fatalf("fit exceeds the message limit: %d", len([]rune(got)))
	}
	// Short greetings pass through untouched.
	if got := fitSentences("Привет! Я Кирилл."); got != "Привет! Я Кирилл." {
		t.Fatalf("short fit = %q", got)
	}
}
