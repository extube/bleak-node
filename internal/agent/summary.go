// Chat summarization for the hard match (feature #35): the transcript of a
// pair's chat is graded against the networking checklist, the closed
// checkpoints are returned together with a plain-text summarization. The
// LLM does the reading; when it is unreachable or answers unparseable, a
// deterministic heuristic takes over (the greeter's fallback pattern).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// Summarizer produces the hard-match summary input of one chat.
type Summarizer struct {
	llm summaryLLM
}

// summaryLLM is the LLM surface the summarizer needs; a *Client satisfies it.
type summaryLLM interface {
	Complete(ctx context.Context, prompt []llmMessage) (string, error)
}

// NewSummarizer wraps an LLM client for chat summarization.
func NewSummarizer(llm *Client) *Summarizer {
	return &Summarizer{llm: llm}
}

// SummarizeChat grades the chat transcript against the networking checkpoints
// and returns the summary description plus the closed flags aligned with
// model.NetworkingCheckpoints. It always succeeds: an LLM failure falls
// back to the heuristic grading (logged, so gateway trouble stays visible).
func (s *Summarizer) SummarizeChat(ctx context.Context, chat model.Chat, msgs []model.Message) (db.SummaryInput, error) {
	out, ok, err := s.llmSummary(ctx, chat, msgs)
	if err != nil {
		slog.Default().Warn("hard-match llm summarization failed, heuristic fallback",
			"chat", chat.Address, "err", err)
	} else if ok {
		return out, nil
	}
	return HeuristicSummary(chat, msgs), nil
}

// llmSummary asks the LLM to summarize the chat and to pick the closed
// checkpoint keys; it parses the JSON answer leniently. Empty and
// unparseable answers are retried briefly (the gateway sends them in
// short bursts - the same tolerance the worker and the greeter use).
func (s *Summarizer) llmSummary(ctx context.Context, chat model.Chat, msgs []model.Message) (db.SummaryInput, bool, error) {
	if s == nil || s.llm == nil {
		return db.SummaryInput{}, false, nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}

	checkpoints := model.NetworkingCheckpoints(chat.Language)
	prompt := summaryPrompt(chat, msgs, checkpoints)

	const tries = 3
	var lastErr error
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return db.SummaryInput{}, false, ctx.Err()
			case <-time.After(time.Duration(i) * 2 * time.Second): // 2s, 4s
			}
		}
		out, err := s.llm.Complete(ctx, prompt)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(out) == "" {
			lastErr = errors.New("llm returned an empty answer")
			continue
		}

		var parsed struct {
			Summary string   `json:"summary"`
			Closed  []string `json:"closed"`
		}
		if err := json.Unmarshal([]byte(stripJSONFences(out)), &parsed); err != nil {
			lastErr = fmt.Errorf("unparseable answer %q: %w", truncateForLog(out), err)
			continue
		}
		if strings.TrimSpace(parsed.Summary) == "" {
			lastErr = fmt.Errorf("empty summary in answer %q", truncateForLog(out))
			continue
		}

		closedSet := make(map[string]struct{}, len(parsed.Closed))
		for _, k := range parsed.Closed {
			closedSet[strings.TrimSpace(k)] = struct{}{}
		}
		closed := make([]bool, len(checkpoints))
		for i, cp := range checkpoints {
			if _, ok := closedSet[cp.Key]; ok {
				closed[i] = true
			}
		}
		return db.SummaryInput{
			Description: sanitizeContent(strings.TrimSpace(parsed.Summary)),
			Closed:      closed,
		}, true, nil
	}
	return db.SummaryInput{}, false, lastErr
}

// truncateForLog keeps LLM answers readable in log lines.
func truncateForLog(s string) string {
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// summaryPrompt builds the one-message summarization prompt: the ordered
// transcript plus the checklist keys, JSON answer only.
func summaryPrompt(chat model.Chat, msgs []model.Message, checkpoints []model.Checkpoint) []llmMessage {
	keys := make([]string, 0, len(checkpoints))
	for _, cp := range checkpoints {
		keys = append(keys, cp.Key)
	}

	var b strings.Builder
	if isRussian(chat.Language) {
		b.WriteString(fmt.Sprintf(`Просуммируй переписку двух участников и оцени, насколько сетевое знакомство удалось. Доступные критерии (ключи): %s.

Ответь ТОЛЬКО валидным JSON без markdown: {"summary": "<2-4 предложения о том, как прошёл разговор>", "closed": [<ключи критериев, которые переписка закрывает>]}.`, strings.Join(keys, ", ")))
	} else {
		b.WriteString(fmt.Sprintf(`Summarize the conversation between two participants and grade how well the networking went. Available criteria (keys): %s.

Answer with valid JSON only, no markdown: {"summary": "<2-4 sentences about how the conversation went>", "closed": [<keys of the criteria the conversation satisfied>]}.`, strings.Join(keys, ", ")))
	}
	b.WriteString("\n\n" + plainTranscript(msgs))
	return []llmMessage{{Role: "user", Content: b.String()}}
}

// plainTranscript renders the chat messages as an ordered "Name: text"
// list for the summarization prompt.
func plainTranscript(msgs []model.Message) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, m.From+": "+m.Content)
	}
	return strings.Join(lines, "\n")
}

// stripJSONFences removes a wrapping markdown code fence if present.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// HeuristicSummary grades the chat without any LLM: the checkpoints close
// on plain transcript properties. It is the deterministic fallback of the
// hard match.
func HeuristicSummary(chat model.Chat, msgs []model.Message) db.SummaryInput {
	perAuthor := make(map[string]int, 2)
	questions := 0
	lastQuestionFrom := ""
	questionAnswered := false
	for _, m := range msgs {
		perAuthor[m.From]++
	}
	for _, m := range msgs {
		if strings.ContainsRune(m.Content, '?') {
			questions++
			lastQuestionFrom = m.From
			continue
		}
		if lastQuestionFrom != "" && lastQuestionFrom != m.From {
			// A reply arrived after the open question.
			questionAnswered = true
			lastQuestionFrom = ""
		}
	}
	a, b := chatParticipants(msgs)
	countA, countB := perAuthor[a], perAuthor[b]
	min, max := countA, countB
	if min > max {
		min, max = max, min
	}

	farewell := false
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1].Content
		markers := append(append([]string{}, goodbyeReplies...), goodbyeRepliesRU...)
		for _, m := range markers {
			if last == m {
				farewell = true
				break
			}
		}
	}

	return db.SummaryInput{
		Description: heuristicDescription(chat, msgs, countA, countB, questions),
		Closed: []bool{
			min >= 1,                      // introductions: both sides spoke
			countA >= 2 && countB >= 2,    // shared topics: a real exchange unfolded
			min*3 >= max && min >= 1,      // balanced turns: neither side dominated
			questions > 0 && questionAnswered, // questions asked and answered
			farewell,                      // warm farewell marker
		},
	}
}

// heuristicDescription builds the fallback summarization line from plain
// transcript statistics.
func heuristicDescription(chat model.Chat, msgs []model.Message, countA, countB, questions int) string {
	if isRussian(chat.Language) {
		return fmt.Sprintf("Разговор содержит %d сообщений: одна сторона написала %d, другая %d, вопросов задано %d.",
			len(msgs), countA, countB, questions)
	}
	return fmt.Sprintf("The conversation holds %d messages: one side wrote %d, the other %d, %d questions were asked.",
		len(msgs), countA, countB, questions)
}

// chatParticipants returns the two distinct authors of the transcript
// (first-seen order); unknown authors fall back to empty strings.
func chatParticipants(msgs []model.Message) (string, string) {
	var a, b string
	for _, m := range msgs {
		if a == "" {
			a = m.From
			continue
		}
		if m.From != a {
			b = m.From
			break
		}
	}
	return a, b
}
