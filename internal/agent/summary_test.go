package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/extube/bleak-node/internal/model"
)

// fakeSummaryLLM scripts the summarization answers.
type fakeSummaryLLM struct {
	answer string
	err    error
}

func (f *fakeSummaryLLM) Complete(ctx context.Context, prompt []llmMessage) (string, error) {
	return f.answer, f.err
}

// summaryChat builds a two-participant chat fixture with a transcript.
func summaryChat() (model.Chat, []model.Message) {
	chat := model.Chat{Address: "0xchat", Room: "0xroom", Language: "en"}
	msgs := []model.Message{
		{From: "0xa", To: "0xb", Content: "Hello, I'm Alice."},
		{From: "0xb", To: "0xa", Content: "Hi Alice! What do you do?"},
		{From: "0xa", To: "0xb", Content: "I paint watercolor postcards."},
		{From: "0xb", To: "0xa", Content: "A shared hobby of mine too."},
	}
	return chat, msgs
}

func TestSummarizeChatParsesLLM(t *testing.T) {
	s := NewSummarizer(nil)
	s.llm = &fakeSummaryLLM{answer: "```json\n" +
		`{"summary": "Alice and Bob found a shared hobby.", "closed": ["introductions", "shared_topics"]}` +
		"\n```"}
	chat, msgs := summaryChat()

	in, err := s.SummarizeChat(context.Background(), chat, msgs)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if in.Description != "Alice and Bob found a shared hobby." {
		t.Fatalf("description: %q", in.Description)
	}
	// The closed keys map onto the canonical order; the rest stay false.
	want := []bool{true, true, false, false, false}
	if len(in.Closed) != len(want) {
		t.Fatalf("closed flags: want %d, got %d", len(want), len(in.Closed))
	}
	for i, w := range want {
		if in.Closed[i] != w {
			t.Fatalf("closed[%d] = %t, want %t", i, in.Closed[i], w)
		}
	}
}

func TestSummarizeChatFallsBackToHeuristic(t *testing.T) {
	// LLM unreachable: the deterministic heuristic grades instead.
	s := NewSummarizer(nil)
	s.llm = &fakeSummaryLLM{err: errors.New("gateway down")}
	chat, msgs := summaryChat()

	in, err := s.SummarizeChat(context.Background(), chat, msgs)
	if err != nil {
		t.Fatalf("summarize must not fail: %v", err)
	}
	if in.Description == "" {
		t.Fatal("heuristic description missing")
	}
	// Both sides spoke and a question was answered, but the transcript
	// has no farewell marker yet.
	if !in.Closed[0] || !in.Closed[2] || !in.Closed[3] {
		t.Fatalf("heuristic flags wrong: %v", in.Closed)
	}
	if in.Closed[4] {
		t.Fatalf("farewell must stay open: %v", in.Closed)
	}
}

func TestHeuristicSummaryClosesFarewell(t *testing.T) {
	chat, msgs := summaryChat()
	msgs = append(msgs,
		model.Message{From: "0xa", To: "0xb", Content: goodbyeReply("en")},
	)
	in := HeuristicSummary(chat, msgs)
	if !in.Closed[4] {
		t.Fatalf("farewell marker must close the checkpoint: %v", in.Closed)
	}
}
