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

// SummarizeChat grades the chat transcript from the SUBJECT's perspective
// (the user the summary is FOR) against the networking checkpoints and
// returns the summary description plus the closed flags aligned with
// model.NetworkingCheckpoints. It always succeeds: an LLM failure falls
// back to the heuristic grading (logged, so gateway trouble stays visible).
func (s *Summarizer) SummarizeChat(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (db.SummaryInput, error) {
	out, ok, err := s.llmSummary(ctx, chat, msgs, subject)
	if err != nil {
		slog.Default().Warn("hard-match llm summarization failed, heuristic fallback",
			"chat", chat.Address, "subject", subject, "err", err)
	} else if ok {
		return out, nil
	}
	return HeuristicSummary(chat, msgs, subject), nil
}

// llmSummary asks the LLM to summarize the chat from the subject's
// perspective and to pick the closed checkpoint keys; it parses the JSON
// answer leniently. Empty and unparseable answers are retried briefly (the
// gateway sends them in short bursts - the same tolerance the worker and
// the greeter use).
func (s *Summarizer) llmSummary(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (db.SummaryInput, bool, error) {
	if s == nil || s.llm == nil {
		return db.SummaryInput{}, false, nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}

	checkpoints := model.NetworkingCheckpoints(chat.Language)
	prompt := summaryPrompt(chat, msgs, checkpoints, subject)

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

// summaryPrompt builds the one-message summarization prompt: the graded
// perspective is the SUBJECT's — how good the networking experience was
// for them and how useful the learned information is for their future
// aims and interests growth. The transcript marks the subject's lines, the
// criteria are listed with their labels, JSON answer only.
func summaryPrompt(chat model.Chat, msgs []model.Message, checkpoints []model.Checkpoint, subject string) []llmMessage {
	criteria := make([]string, 0, len(checkpoints))
	for _, cp := range checkpoints {
		criteria = append(criteria, fmt.Sprintf("%s — %s", cp.Key, cp.Label))
	}

	var b strings.Builder
	if isRussian(chat.Language) {
		b.WriteString(fmt.Sprintf(`Оцени переписку двух участников С ТОЧКИ ЗРЕНИЯ пользователя %s (его реплики отмечены «★»).

Два вопроса для оценки:
1. Насколько хорошим было это знакомство для %s: получил ли он/она приятный опыт общения?
2. Насколько ПОЛЕЗНА полученная в разговоре информация для дальнейших целей и развития интересов %s?

Критерии (оценивай строго, закрывай ключ ТОЛЬКО при явных доказательствах В ПОЛЬЗУ %s, а не только второй стороны):
%s

Ответь ТОЛЬКО валидным JSON без markdown: {"summary": "<2-4 предложения: что %s получил от разговора и что выносит для своих целей и интересов>", "closed": [<ключи критериев, закрытых для %s>]}.`,
			subject, subject, subject, subject, strings.Join(criteria, "\n"), subject, subject))
	} else {
		b.WriteString(fmt.Sprintf(`Grade the conversation between two participants FROM THE PERSPECTIVE of the user %s (their lines are marked with "★").

Two questions to answer:
1. How good was this networking experience for %s: did they come away with a genuinely good interaction?
2. How USEFUL is the information gained in this conversation for %s's future aims and interests growth?

Criteria (grade STRICTLY; close a key ONLY with explicit evidence in favor of %s, not merely of the other side):
%s

Answer with valid JSON only, no markdown: {"summary": "<2-4 sentences: what %s got out of the conversation and takes away for their aims and interests>", "closed": [<keys of the criteria closed FOR %s>]}.`,
			subject, subject, subject, subject, strings.Join(criteria, "\n"), subject, subject))
	}
	b.WriteString("\n\n" + plainTranscript(msgs, subject))
	return []llmMessage{{Role: "user", Content: b.String()}}
}

// plainTranscript renders the chat messages as an ordered "Name: text"
// list for the summarization prompt; the subject's lines carry a "★" mark.
func plainTranscript(msgs []model.Message, subject string) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		marker := ""
		if m.From == subject {
			marker = " ★"
		}
		lines = append(lines, m.From+marker+": "+m.Content)
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

// HeuristicSummary grades the chat without any LLM, still from the
// SUBJECT's perspective: the checkpoints close on plain transcript
// properties favoring the subject. It is the deterministic fallback of the
// hard match.
func HeuristicSummary(chat model.Chat, msgs []model.Message, subject string) db.SummaryInput {
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
	// The conversation partner: the transcript author who is not the subject.
	partner := subject
	for who := range perAuthor {
		if who != subject {
			partner = who
			break
		}
	}

	// concrete_followup: contact channels or agreements to continue, in
	// the subject's own lines or the partner's offer to them.
	followupMarkers := []string{
		"@", "telegram", "t.me/", "whatsapp", "linkedin", "instagram",
		"mail", "email", "напишу", "напишите", "свяжусь", "связаться",
		"обменяемся", "телефон", "телеграм", "до связи", "буду на связи",
		"i'll send", "i'll reach out", "reach out", "let's exchange",
		"hit me up", "my number", "here's my", "follow up", "next week",
	}
	followup := false
	for _, m := range msgs {
		if m.From != subject && m.From != partner {
			continue
		}
		low := strings.ToLower(m.Content)
		for _, mk := range followupMarkers {
			if strings.Contains(low, mk) {
				followup = true
				break
			}
		}
		if followup {
			break
		}
	}

	// expertise_exchange: the partner shared work-related specifics, or
	// the subject explicitly asked about them.
	workMarkers := []string{
		"work", "job", "company", "startup", "project", "freelance",
		"studio", "team", "developer", "designer", "teach", "business",
		"работ", "проект", "компан", "стартап", "фриланс", "студ",
		"команд", "разраб", "дизайн", "препода", "учител", "бизнес",
		"чем занимаешься", "what do you do", "what do you make",
	}
	partnerShared := false
	subjectAsked := false
	for _, m := range msgs {
		low := strings.ToLower(m.Content)
		hit := false
		for _, mk := range workMarkers {
			if strings.Contains(low, mk) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if m.From == partner {
			partnerShared = true
		}
		if m.From == subject && strings.ContainsRune(low, '?') {
			subjectAsked = true
		}
	}
	expertise := partnerShared || subjectAsked

	// reciprocal_dialog: the subject drove their line of the dialog —
	// asked questions that were answered, without being overwhelmed by
	// the partner's volume.
	reciprocal := (subjectAsked || (questions > 0 && questionAnswered)) &&
		perAuthor[subject]*2 >= perAuthor[partner] && perAuthor[subject] >= 1

	return db.SummaryInput{
		Description: heuristicDescription(chat, msgs, subject, countA, countB, questions),
		Closed: []bool{
			followup,   // concrete follow-up for the subject
			expertise,  // the subject learned about the partner's work
			reciprocal, // the subject's questions were picked up
		},
	}
}

// heuristicDescription builds the fallback summarization line from plain
// transcript statistics, phrased for the subject.
func heuristicDescription(chat model.Chat, msgs []model.Message, subject string, countA, countB, questions int) string {
	if isRussian(chat.Language) {
		return fmt.Sprintf("Разговор содержит %d сообщений: одна сторона написала %d, другая %d, вопросов задано %d. Оценка с точки зрения %s.",
			len(msgs), countA, countB, questions, subject)
	}
	return fmt.Sprintf("The conversation holds %d messages: one side wrote %d, the other %d, %d questions were asked. Graded from %s's perspective.",
		len(msgs), countA, countB, questions, subject)
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
