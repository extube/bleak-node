package agent

import (
	"context"
	"errors"
	"log/slog"
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
	// MaxMessages caps the conversation: once a chat has that many messages
	// the prompts ask for a farewell (greeting + goodbye) and the chain stops.
	MaxMessages int
	// Poll is the queue polling interval.
	Poll time.Duration
	// Timeout bounds one queued event end-to-end (queue reads + LLM call).
	Timeout time.Duration
	// MaxAttempts before an event is marked failed.
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
	if cfg.EchoRetries <= 0 {
		cfg.EchoRetries = 2
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
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

// Run processes the queue until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
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
		if ev.Attempts+1 >= w.cfg.MaxAttempts {
			return true, w.db.CompleteEvent(eventCtx, ev.ID, err.Error())
		}
		if rerr := w.db.RequeueEvent(eventCtx, ev.ID); rerr != nil {
			return true, rerr
		}
		return true, nil
	}
	return true, w.db.CompleteEvent(eventCtx, ev.ID, "")
}

func (w *Worker) tick(ctx context.Context) {
	if _, err := w.RunOnce(ctx); err != nil {
		w.log.Error("agent queue error", "err", err)
	}
}

// process generates the next message of the event speaker inside the chat
// and, unless the chat is over the message limit, queues the turn of the
// listener (the other agent) after saving the response.
func (w *Worker) process(ctx context.Context, ev model.Event) error {
	if ev.Type != model.EventChatInteract {
		// Unknown type: nothing to do, completing with the reason.
		w.log.Warn("agent worker ignores unknown event type", "type", ev.Type, "id", ev.ID)
		return nil
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

	msgs, err := w.db.ListChatMessages(ctx, ev.Chat)
	if err != nil {
		return err
	}
	overLimit := len(msgs) >= w.cfg.MaxMessages

	// Is this reply the first message of this agent in the chat?
	myTurns := 0
	for _, m := range msgs {
		if m.From == ev.Speaker {
			myTurns++
		}
	}
	// Normalize replies already said (by ANY of the participants: agents
	// also mimic each other, which reads as repetition).
	priorWords := make(map[string]struct{}, len(msgs))
	for _, m := range msgs {
		priorWords[normalize(m.Content)] = struct{}{}
	}

	// Anti-echo: regenerate up to cfg.EchoRetries times when the model
	// parrots one of its earlier lines verbatim.
	var (
		content  string
		repeated bool
	)
	for attempt := 0; attempt <= w.cfg.EchoRetries; attempt++ {
		nudge := topicSeed(ev, len(msgs), attempt, overLimit, listener.Name)
		if attempt > 0 {
			nudge += " Your previous reply repeated an earlier line; write something NEW."
		}
		prompt := buildPrompt(ev.Chat, speaker, listener, msgs, myTurns, overLimit, nudge)
		out, err := w.llm.Complete(ctx, prompt)
		if err != nil {
			return err
		}
		candidate := sanitizeContent(out)
		if candidate == "" {
			return errors.New("llm returned an empty message")
		}
		content = candidate
		if isEcho(normalize(candidate), priorWords) {
			repeated = true
			continue
		}
		repeated = false
		break
	}
	if repeated {
		// Even the retries parroted; save the last one instead of blocking
		// the conversation, but leave a trace.
		w.log.Warn("agent message repeats an earlier line",
			"chat", ev.Chat, "speaker", ev.Speaker)
	}

	m := model.Message{From: ev.Speaker, To: ev.Listener, Content: content}
	if err := w.db.CreateMessage(ctx, &m); err != nil {
		return err
	}
	w.log.Info("agent message",
		"chat", ev.Chat, "from", speaker.Name, "to", listener.Name, "content", content)

	// Hand the turn to the other agent; over the limit the farewell above
	// closes the conversation and no further event is queued.
	if !overLimit {
		return w.db.EnqueueEvent(ctx, model.EventChatInteract, ev.Chat, ev.Listener, ev.Speaker)
	}
	return nil
}
