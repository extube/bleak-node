package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

// fakeSummaryLLM scripts the summarization answers and records the prompt.
type fakeSummaryLLM struct {
	answer string
	err    error
	prompt string
}

func (f *fakeSummaryLLM) Complete(ctx context.Context, prompt []llmMessage) (string, error) {
	if len(prompt) > 0 {
		f.prompt = prompt[0].Content
	}
	return f.answer, f.err
}

// summaryChat builds a two-participant chat fixture with a transcript.
func summaryChat() (model.Chat, []model.Message) {
	chat := model.Chat{Address: "0xchat", Room: "0xroom", Language: "en"}
	msgs := []model.Message{
		{From: "0xa", To: "0xb", Content: "Hello, I'm Alice."},
		{From: "0xb", To: "0xa", Content: "Hi Alice! What do you do for work?"},
		{From: "0xa", To: "0xb", Content: "I paint watercolor postcards."},
		{From: "0xb", To: "0xa", Content: "A shared hobby of mine too."},
	}
	return chat, msgs
}

func TestSummarizeChatParsesLLM(t *testing.T) {
	llm := &fakeSummaryLLM{answer: "```json\n" +
		`{"summary": "Alice learned about Bob's studio.", "closed": ["concrete_followup", "expertise_exchange"]}` +
		"\n```"}
	s := NewSummarizer(nil)
	s.llm = llm
	chat, msgs := summaryChat()

	in, err := s.SummarizeChat(context.Background(), chat, msgs, "0xa")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if in.Description != "Alice learned about Bob's studio." {
		t.Fatalf("description: %q", in.Description)
	}
	// The closed keys map onto the canonical order; the rest stay false.
	want := []bool{true, true, false}
	if len(in.Closed) != len(want) {
		t.Fatalf("closed flags: want %d, got %d", len(want), len(in.Closed))
	}
	for i, w := range want {
		if in.Closed[i] != w {
			t.Fatalf("closed[%d] = %t, want %t", i, in.Closed[i], w)
		}
	}
	// The prompt grades from the subject's perspective and marks their lines.
	if !strings.Contains(llm.prompt, "FROM THE PERSPECTIVE of the user 0xa") ||
		!strings.Contains(llm.prompt, "0xa ★") {
		t.Fatalf("prompt must be subject-centric and mark the subject's lines: %q", llm.prompt)
	}
}

func TestSummarizeChatFallsBackToHeuristic(t *testing.T) {
	// LLM unreachable: the deterministic heuristic grades instead.
	s := NewSummarizer(nil)
	s.llm = &fakeSummaryLLM{err: errors.New("gateway down")}
	chat, msgs := summaryChat()

	in, err := s.SummarizeChat(context.Background(), chat, msgs, "0xa")
	if err != nil {
		t.Fatalf("summarize must not fail: %v", err)
	}
	if in.Description == "" {
		t.Fatal("heuristic description missing")
	}
	if len(in.Closed) != 3 {
		t.Fatalf("heuristic must grade the 3 criteria, got %d", len(in.Closed))
	}
	// The subject asked what the partner does (expertise), the partner
	// answered with work details, and the exchange was balanced — the
	// expertise and reciprocal criteria close; no contact markers in the
	// transcript keep the follow-up open.
	if !in.Closed[1] || !in.Closed[2] {
		t.Fatalf("heuristic flags wrong: %v", in.Closed)
	}
	if in.Closed[0] {
		t.Fatalf("follow-up must stay open without contact evidence: %v", in.Closed)
	}
}

func TestHeuristicClosesFollowup(t *testing.T) {
	chat, msgs := summaryChat()
	msgs = append(msgs,
		model.Message{From: "0xa", To: "0xb", Content: "Great meeting you! Here's my telegram: @alice, I'll send the sketch tomorrow."},
	)
	in := HeuristicSummary(chat, msgs, "0xa")
	if !in.Closed[0] {
		t.Fatalf("contact details must close the follow-up criterion: %v", in.Closed)
	}
}
