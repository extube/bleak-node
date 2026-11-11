package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// Config tunes the agent worker.
type Config struct {
	// LLMBaseURL is the OpenAI-compatible endpoint, e.g. http://localhost:8000/v1.
	LLMBaseURL string
	// LLMModel is the served chat model id, e.g. gemma-4-e2b-agent.
	LLMModel string
	// APIKey is optional; required by hosted gateways like OpenRouter.
	APIKey string
	// DisableThinking silences the reasoning channel of Qwen-style thinking
	// models (chat_template_kwargs enable_thinking=false).
	DisableThinking bool
	// MaxUserMessages caps the messages ONE agent writes in a single chat,
	// its farewell included: once the speaker is about to spend the quota,
	// the prompts ask for a farewell and the chain stops (pattern-reply
	// goodbye of the listener follows).
	MaxUserMessages int
	// Debate enables the second, themed part of the chat (the transition
	// message and the debate phase). When false the whole interaction
	// stays in the networking phase and ends with the farewell.
	Debate bool
	// Poll is the queue polling interval.
	Poll time.Duration
	// Timeout bounds one queued event end-to-end (queue reads + LLM call).
	Timeout time.Duration
	// LLMCallTimeout bounds ONE LLM call inside an event; the retries of
	// completeWithRetry only happen when a single call cannot eat the whole
	// event timeout. Must stay smaller than Timeout.
	LLMCallTimeout time.Duration
	// MaxAttempts before an event is marked failed. Attempts requeue with
	// exponential backoff (see backoffDelay), so a gateway outage rides out
	// in the queue instead of burning all attempts in minutes.
	MaxAttempts int
	// EchoRetries is how many times a reply that verbatim-repeats one of the
	// agent's earlier lines is regenerated before it is saved anyway.
	EchoRetries int
}

// Worker consumes the event queue and generates the agent messages.
type Worker struct {
	db  *db.DB
	llm *Client
	cfg Config
	log *slog.Logger
}

// New creates a worker with sane defaults for unset options.
func New(database *db.DB, cfg Config, logger *slog.Logger) *Worker {
	if cfg.Poll <= 0 {
		cfg.Poll = 300 * time.Millisecond
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.LLMCallTimeout <= 0 {
		// Three calls plus the 2s+4s retry waits must fit into the default
		// 90s event timeout with headroom.
		cfg.LLMCallTimeout = 25 * time.Second
	}
	if cfg.EchoRetries <= 0 {
		cfg.EchoRetries = 2
	}
	if cfg.MaxAttempts <= 0 {
		// With exponential backoff the six attempts spread over ~1h of
		// gateway trouble before an event is given up on.
		cfg.MaxAttempts = 6
	}
	if cfg.MaxUserMessages <= 0 {
		cfg.MaxUserMessages = model.DefaultUserMessageLimit
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		db:  database,
		llm: NewClient(cfg.LLMBaseURL, cfg.LLMModel, cfg.APIKey, cfg.Timeout, cfg.DisableThinking),
		cfg: cfg,
		log: logger,
	}
}

// completeWithRetry tolerates a flaky gateway: empty answers and answers
// in the wrong room language come in short bursts, so a couple of brief
// retries inside ONE event attempt stop a whole chat turn from burning its
// queue-level attempts (three bad responses otherwise kill the event and
// hang the chat forever).
func (w *Worker) completeWithRetry(ctx context.Context, prompt []llmMessage, wantLanguage string) (string, error) {
	const tries = 3
	var lastErr error
	for i := 0; i < tries; i++ {
		// Per-call budget: without it one slow gateway call eats the whole
		// event timeout and the two retries never get their chance.
		callCtx, callCancel := context.WithTimeout(ctx, w.cfg.LLMCallTimeout)
		out, err := w.llm.Complete(callCtx, prompt)
		callCancel()
		if err == nil && strings.TrimSpace(out) != "" {
			if model.TextMatchesLanguage(out, wantLanguage) {
				return out, nil
			}
			err = errors.New("llm answered in the wrong language")
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("llm returned an empty message")
		}
		if ctx.Err() != nil {
			return "", lastErr
		}
		select {
		case <-ctx.Done():
			return "", lastErr
		case <-time.After(time.Duration(i+1) * 2 * time.Second): // 2s, 4s
		}
	}
	return "", lastErr
}

// Dialog-recovery tuning: the revival sweep runs about twice a minute and
// only touches chats whose silence no live queue activity explains.
const (
	revivalSweepEvery = 30 * time.Second
	// revivalMsgSilence: the last message must be older than this.
	revivalMsgSilence = 3 * time.Minute
	// revivalQueueQuiet: no event of the chat may have finished (or failed)
	// within this window, so backed-off retries are not second-guessed.
	revivalQueueQuiet = 10 * time.Minute
)

// Run processes the queue until ctx is cancelled. Processing events of a
// previous run are reclaimed first: this worker is the single queue consumer,
// so any "processing" left by a crashed or restarted process is an orphan.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.db.ReclaimProcessing(ctx, 0); err != nil {
		w.log.Error("reclaim processing events", "err", err)
	} else if n > 0 {
		w.log.Warn("reclaimed processing events of a previous run", "count", n)
	}
	ticker := time.NewTicker(w.cfg.Poll)
	defer ticker.Stop()
	nextSweep := time.Now().Add(revivalSweepEvery)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
			if time.Now().After(nextSweep) {
				nextSweep = time.Now().Add(revivalSweepEvery)
				w.revive(ctx)
			}
		}
	}
}

// RunOnce processes at most one queued event; used by tests and callers that
// poll manually. It reports whether an event was present.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	eventCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	ev, found, err := w.db.NextEvent(eventCtx)
	if err != nil || !found {
		return found, err
	}
	if err := w.db.StartEvent(eventCtx, ev.ID); err != nil {
		// Someone else claimed it; leave it to its owner.
		return true, nil
	}

	if err := w.process(eventCtx, ev); err != nil {
		w.log.Error("agent event failed",
			"id", ev.ID, "type", ev.Type, "chat", ev.Chat,
			"speaker", ev.Speaker, "listener", ev.Listener, "err", err)
		// Finalize on a FRESH context: eventCtx is often exactly what
		// expired (LLM timeout), and reusing it fails the requeue too,
		// leaving the event stuck in "processing" until the orphan
		// sweep reclaims it minutes later.
		endCtx, endCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer endCancel()
		if ev.Attempts+1 >= w.cfg.MaxAttempts {
			return true, w.db.CompleteEvent(endCtx, ev.ID, err.Error())
		}
		// Exponential backoff: the next attempt waits 1, 2, 4, ... minutes,
		// so a flaky-gateway burst (empty answers, timeouts) waits out in
		// the queue instead of burning all attempts in five minutes and
		// hanging the chat forever.
		if rerr := w.db.RequeueEvent(endCtx, ev.ID, backoffDelay(ev.Attempts+1)); rerr != nil {
			return true, rerr
		}
		return true, nil
	}
	// Success: the turn (message bubbles, phase stamp, next event) committed
	// atomically together with the event completion inside process().
	return true, nil
}

func (w *Worker) tick(ctx context.Context) {
	// Defensive sweep: a processing row older than a few full run windows can
	// only be an orphan of a dead consumer (one completes within cfg.Timeout).
	if n, err := w.db.ReclaimProcessing(ctx, 5*w.cfg.Timeout); err != nil {
		w.log.Error("reclaim stale processing events", "err", err)
	} else if n > 0 {
		w.log.Warn("reclaimed stale processing events", "count", n)
	}
	if _, err := w.RunOnce(ctx); err != nil {
		w.log.Error("agent queue error", "err", err)
	}
}

// turnGroup is one message group of a turn: bubbles from one author with a
// log label (e.g. "greeting 1/2", "goodbye reply").
type turnGroup struct {
	label  string
	bubble db.TurnBubble
}

// turnResult collects everything ONE processed event changes. It all
// commits in ONE transaction (db.CompleteTurn): message bubbles, an
// optional phase stamp, an optional next-turn event and the completion of
// the event itself.
type turnResult struct {
	groups []turnGroup
	phase  *db.TurnPhase
	next   *db.TurnNext
}

// commitTurn applies the turn atomically and logs the saved bubbles group
// by group. Either everything commits or nothing does: a crash or reload
// mid-turn leaves the event reclaimable and the transcript untouched, so
// the retry regenerates the turn cleanly - no duplicated bubbles, no lost
// hand-off, no stalled dialog.
func (w *Worker) commitTurn(ctx context.Context, ev model.Event, names map[string]string, r turnResult) error {
	bubbles := make([]db.TurnBubble, len(r.groups))
	for i, g := range r.groups {
		bubbles[i] = g.bubble
	}
	saved, err := w.db.CompleteTurn(ctx, ev.ID, bubbles, r.phase, r.next)
	if err != nil {
		return err
	}
	i := 0
	for _, g := range r.groups {
		for range g.bubble.Contents {
			m := saved[i]
			i++
			from, to := m.From, m.To
			if n, ok := names[m.From]; ok {
				from = n
			}
			if n, ok := names[m.To]; ok {
				to = n
			}
			w.log.Info("agent message",
				"chat", ev.Chat, "from", from, "to", to,
				"part", g.label, "content", m.Content)
		}
	}
	return nil
}

// backoffDelay grows exponentially with the attempt number - 1, 2, 4, 8, 16,
// 32 minutes, capped at an hour - so a longer gateway outage waits out in
// the queue instead of exhausting the attempts in minutes and hanging the
// chat forever.
func backoffDelay(attempt int) time.Duration {
	d := time.Minute
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= time.Hour {
			return time.Hour
		}
	}
	return d
}

// revive re-kickoffs chats the revival sweep considers stalled: they carry
// messages, have no pending or processing event, their last message is not
// a farewell (the conversation did not end) and their silence is not
// explained by any recent queue activity. The participant who did not write
// the last message speaks next - the same hand-off the live chain performs
// after every turn. This is the last line of defense: whatever broke the
// chain (a crash between two writes of an older build, a lost enqueue, a
// manually failed event), the dialog resumes on its own.
func (w *Worker) revive(ctx context.Context) {
	markers := append(append([]string{}, goodbyeReplies...), goodbyeRepliesRU...)
	stalled, err := w.db.StalledChats(ctx, revivalMsgSilence, revivalQueueQuiet, markers)
	if err != nil {
		w.log.Error("revival sweep", "err", err)
		return
	}
	for _, s := range stalled {
		if err := w.db.EnqueueEvent(ctx, model.EventChatInteract, s.Chat, s.Other, s.LastFrom); err != nil {
			w.log.Error("chat revival", "chat", s.Chat, "err", err)
			continue
		}
		w.log.Warn("stalled chat revived",
			"chat", s.Chat, "speaker", s.Other, "listener", s.LastFrom,
			"last_message_at", s.LastAt.Format(time.RFC3339))
	}
}

// process generates the next turn of the event speaker inside the chat and,
// unless the speaker has spent its message quota, queues the turn of the
// listener (the other agent) after saving the response. Every turn generates
// exactly ONE next-turn event: a reply is saved as one natural message bubble.
// Structure of a chat:
//   - the chat opens with the two participants' stored greeting messages
//     (plain hello bubbles, no questions, no LLM turn);
//   - the chat opener then continues with the first question turn;
//   - the middle part is the conversation;
//   - the end is a warm farewell followed by a short localized reply, without LLM.
func (w *Worker) process(ctx context.Context, ev model.Event) error {
	if ev.Type != model.EventChatInteract {
		// Unknown type: nothing to change, just complete the event (its own
		// one-transaction commit keeps it out of the queue forever).
		w.log.Warn("agent worker ignores unknown event type", "type", ev.Type, "id", ev.ID)
		_, err := w.db.CompleteTurn(ctx, ev.ID, nil, nil, nil)
		return err
	}

	speaker, ok, err := w.db.UserByAddress(ctx, ev.Speaker)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("speaker user does not exist")
	}
	listener, ok, err := w.db.UserByAddress(ctx, ev.Listener)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("listener user does not exist")
	}
	// Display names for the turn log lines.
	names := map[string]string{speaker.Address: speaker.Name, listener.Address: listener.Name}

	chat, found, err := w.db.ChatByAddress(ctx, ev.Chat)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("chat of the event does not exist")
	}
	phase := chat.Phase
	if phase == "" {
		phase = model.PhaseNetworking
	}
	if !w.cfg.Debate && phase == model.PhaseDebate {
		// Part 2 removed: chats run networking-only, so a stale
		// debate stamp from an older build is ignored.
		phase = model.PhaseNetworking
	}
	theme, err := w.resolveScenario(ctx, chat)
	if err != nil {
		return err
	}

	msgs, err := w.db.ListChatMessages(ctx, ev.Chat)
	if err != nil {
		return err
	}
	// Chat start: both users open the dialog with their stored greeting
	// messages - plain hello bubbles, no LLM turn and no questions. Each
	// greeting is posted by its OWN author; the starter then gets the next
	// event to continue with the first question of the conversation.
	if phase == model.PhaseNetworking && len(msgs) == 0 {
		return w.commitTurn(ctx, ev, names, turnResult{
			groups: []turnGroup{
				{label: "greeting 1/2", bubble: db.TurnBubble{
					From: ev.Speaker, To: ev.Listener,
					Contents: []string{greetingOrFallback(speaker, chat.Language)},
				}},
				{label: "greeting 2/2", bubble: db.TurnBubble{
					From: ev.Listener, To: ev.Speaker,
					Contents: []string{greetingOrFallback(listener, chat.Language)},
				}},
			},
			next: &db.TurnNext{Speaker: ev.Speaker, Listener: ev.Listener},
		})
	}

	// Part-1 messages stay saved in the database, but the debate phase
	// replays only the messages created since phase_started_at onward.
	if phase == model.PhaseDebate && chat.PhaseStartedAt != nil {
		filtered := make([]model.Message, 0, len(msgs))
		for _, m := range msgs {
			if !m.CreatedAt.Before(*chat.PhaseStartedAt) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	// Quota of ONE participant, the closing message included: when the
	// speaker is about to spend it, this turn either ends the part with a
	// theme-change offer (part 1) or becomes the farewell (part 2).
	speakerTurns := 0
	for _, m := range msgs {
		if m.From == ev.Speaker {
			speakerTurns++
		}
	}
	overLimit := speakerTurns+1 >= w.cfg.MaxUserMessages
	transition := w.cfg.Debate && phase == model.PhaseNetworking && overLimit

	var content string
	// batch holds the sentence-split, echo-filtered messages of a middle
	// turn; farewells, introductions and the theme-change offer ignore it.
	var batch []string
	if overLimit && transition {
		// The networking part closes with ONE message: an AFTER-step follow-up
		// personal chat plus an offer to switch to the abstract theme.
		out, err := w.completeWithRetry(ctx, buildTransitionPrompt(theme, speaker, listener, msgs, chat.Language), chat.Language)
		if err != nil {
			return err
		}
		content = sanitizeContent(out)
		if content == "" {
			return errors.New("llm returned an empty message")
		}
	} else if overLimit {
		// The farewell is generated WITHOUT the transcript: a warm, casual
		// goodbye with no new topic. When the listener left an open
		// question, its text rides along so the farewell can answer it in
		// the first sentence.
		last, _ := lastListenerMessage(listener, msgs)
		out, err := w.completeWithRetry(ctx, buildGoodbyePrompt(theme, speaker, last, chat.Language), chat.Language)
		if err != nil {
			return err
		}
		content = sanitizeContent(out)
		if content == "" {
			return errors.New("llm returned an empty message")
		}
	} else {
		// Anti-echo: regenerate up to cfg.EchoRetries times when the model
		// parrots one of its earlier lines verbatim.
		priorWords := make(map[string]struct{}, len(msgs))
		for _, m := range msgs {
			priorWords[normalize(m.Content)] = struct{}{}
		}
		repeated := false
		for attempt := 0; attempt <= w.cfg.EchoRetries; attempt++ {
			nudge := ""
			if speakerTurns > 0 {
				nudge = topicSeed(ev, phase, speakerTurns, attempt, overLimit, chat.Language)
				// Answer-first rule: when the partner's last message asks an
				// open question, the turn belongs to the answer, not to the
				// rotating topic seed.
				if last, _ := lastListenerMessage(listener, msgs); last != "" {
					if direct := questionDirect(listener, last, chat.Language); direct != "" {
						nudge = direct
					}
				}
			}
			if attempt > 0 {
				if isRussian(chat.Language) {
					nudge += " Предыдущая реплика повторила уже сказанное; напиши по-новому и продолжай разговор."
				} else {
					nudge += " Your previous reply repeated an earlier line; write something new and continue the conversation."
				}
			}
			prompt := buildPrompt(theme, phase, speaker, listener, msgs, speakerTurns, overLimit, nudge, chat.Language)
			out, err := w.completeWithRetry(ctx, prompt, chat.Language)
			if err != nil {
				return err
			}
			candidate := sanitizeContent(out)
			if candidate == "" {
				return errors.New("llm returned an empty message")
			}
			content = candidate
			// Dialog clarity: the reply is delivered one bubble per sentence,
			// so echo detection runs per bubble. A batch whose every bubble
			// repeats an earlier line is regenerated; a batch with SOME old
			// bubbles shrinks to the new thoughts only - echoed sentences are
			// dropped, never saved.
			parts := splitIntoMessages(candidate, model.MaxMessageContentLen)
			echoParts := 0
			for _, p := range parts {
				if isEcho(normalize(p), priorWords) {
					echoParts++
				}
			}
			if len(parts) > 0 && echoParts == len(parts) {
				repeated = true
				continue
			}
			batch = parts
			if echoParts > 0 {
				kept := make([]string, 0, len(parts))
				dropped := 0
				for _, p := range parts {
					if isEcho(normalize(p), priorWords) {
						dropped++
						continue
					}
					kept = append(kept, p)
				}
				if dropped > 0 {
					w.log.Warn("agent reply bubbles dropped as echoes",
						"chat", ev.Chat, "speaker", ev.Speaker, "dropped", dropped)
				}
				batch = kept
			}
			repeated = false
			break
		}
		if repeated {
			// Even the retries echoed fully; save the last batch instead of
			// blocking the conversation, but leave a trace.
			batch = []string{content}
			w.log.Warn("agent message repeats an earlier line",
				"chat", ev.Chat, "speaker", ev.Speaker)
		}
	}

	// Introductions, the theme-change offer and the farewell are never
	// split; ordinary replies use the echo-free sentence batch.
	var parts []string
	switch {
	case overLimit, speakerTurns == 0, transition:
		parts = []string{content}
	default:
		parts = batch
	}

	switch {
	case transition:
		// Part 1 ends HERE: stamp the moment the debate begins (the hobby
		// messages stay saved for the API) and let the OTHER agent open the
		// themed part of the chat.
		err := w.commitTurn(ctx, ev, names, turnResult{
			groups: []turnGroup{{label: "1/1", bubble: db.TurnBubble{
				From: ev.Speaker, To: ev.Listener, Contents: parts,
			}}},
			phase: &db.TurnPhase{Phase: model.PhaseDebate, StartedAt: time.Now()},
			next:  &db.TurnNext{Speaker: ev.Listener, Listener: ev.Speaker},
		})
		if err != nil {
			return err
		}
		w.log.Info("chat phase changed",
			"chat", ev.Chat, "phase", model.PhaseDebate, "theme", theme.Name)
		return nil
	case overLimit:
		// Reply goodbye: a pattern message of the listener, generated
		// locally without the LLM and without the transcript. The chat ends
		// here: no further event is queued.
		return w.commitTurn(ctx, ev, names, turnResult{
			groups: []turnGroup{
				{label: "1/1", bubble: db.TurnBubble{
					From: ev.Speaker, To: ev.Listener, Contents: parts,
				}},
				{label: "goodbye reply", bubble: db.TurnBubble{
					From: ev.Listener, To: ev.Speaker,
					Contents: []string{goodbyeReply(chat.Language)},
				}},
			},
		})
	}

	// Hand the turn to the other agent.
	return w.commitTurn(ctx, ev, names, turnResult{
		groups: []turnGroup{{label: fmt.Sprintf("1/%d", len(parts)), bubble: db.TurnBubble{
			From: ev.Speaker, To: ev.Listener, Contents: parts,
		}}},
		next: &db.TurnNext{Speaker: ev.Listener, Listener: ev.Speaker},
	})
}

// resolveScenario returns the theme of the chat. When the chat has no
// scenario yet, one is picked from the catalog and persisted; chats keep
// their scenario across kickoffs.
func (w *Worker) resolveScenario(ctx context.Context, chat model.Chat) (Theme, error) {
	if chat.Scenario != "" {
		if theme := themeByKey(chat.Scenario); theme != nil {
			return *theme, nil
		}
		// Stale key from an older catalog: use the default theme.
		return Themes[0], nil
	}
	picked := Themes[randInt(len(Themes))]
	if err := w.db.SetChatScenario(ctx, chat.Address, picked.Key); err != nil {
		return Theme{}, err
	}
	return picked, nil
}
