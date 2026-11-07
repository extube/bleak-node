// Package agent runs the automatic interaction between the two participants
// of a chat by consuming chat.interact events from the queue and turning
// them into LLM-generated messages.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal OpenAI-compatible chat-completions client. It speaks
// to local servers (Ollama, llama.cpp, OVMS, Qwen) as well as hosted
// gateways when an API key is set.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	// disableThinking sends chat_template_kwargs {"enable_thinking": false}
	// for servers of thinking models (Qwen-style); ignored when false.
	disableThinking bool
	http            *http.Client
}

// NewClient creates a client for an OpenAI-compatible endpoint. The apiKey
// is optional; when non-empty it is sent as a Bearer token.
func NewClient(baseURL, model, apiKey string, timeout time.Duration, disableThinking bool) *Client {
	return &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		model:           model,
		apiKey:          apiKey,
		disableThinking: disableThinking,
		http:            &http.Client{Timeout: timeout},
	}
}

// llmMessage is one prompt entry.
type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []llmMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature float64      `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
	// ChatTemplateArgs silences the sandbox thinking channel of Qwen-style
	// models so the answer lands directly in `content`.
	ChatTemplateArgs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ternary returns a when ok, else b.
func ternary[T any](ok bool, a, b T) T {
	if ok {
		return a
	}
	return b
}

// Complete sends the prompt and returns the plain assistant message.
func (c *Client) Complete(ctx context.Context, prompt []llmMessage) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:            c.model,
		Messages:         prompt,
		MaxTokens:        512,
		Temperature:      0.7,
		Stream:           false,
		ChatTemplateArgs: ternary(c.disableThinking, map[string]any{"enable_thinking": false}, nil),
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("llm read body: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, truncateErr(raw))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm decode: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", errors.New("llm: " + out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("llm: empty completion")
	}
	return out.Choices[0].Message.Content, nil
}

func truncateErr(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
