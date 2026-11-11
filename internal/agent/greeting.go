package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/extube/bleak-node/internal/model"
)

// Greeter generates the fixed greeting (hello) message of a user from
// their bios. The greeting opens every new chat of the user instead of a
// generated introduction turn, so it never contains questions.
type Greeter struct {
	llm greetingLLM
}

// greetingLLM is the LLM surface the greeter needs; a *Client satisfies it.
type greetingLLM interface {
	Complete(ctx context.Context, prompt []llmMessage) (string, error)
}

// NewGreeter wraps an LLM client for greeting generation.
func NewGreeter(llm *Client) *Greeter {
	return &Greeter{llm: llm}
}

// GenerateGreeting returns the greeting text for the user, generated from
// the bios in the given language (en or ru). It is always non-empty and
// question-free: when the LLM is unreachable or keeps asking questions,
// a plain bio-based template is used instead.
func (g *Greeter) GenerateGreeting(name, publicBio, privateBio, language string) string {
	russian := isRussian(language)
	prompt := greetingPrompt(name, publicBio, privateBio, russian)
	good := func(c string) bool {
		return c != "" && !strings.ContainsRune(c, '?') &&
			model.TextMatchesLanguage(c, language) &&
			strings.Contains(strings.ToLower(c), strings.ToLower(name))
	}
	var candidate string
	for i := 0; i < 3; i++ {
		out, err := g.llm.Complete(context.Background(), prompt)
		if err != nil {
			break // gateway unreachable: the template takes over
		}
		candidate = fitSentences(sanitizeContent(strings.TrimSpace(out)))
		if good(candidate) {
			return candidate
		}
		if i < 2 {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}
	// The best candidate may only need its question sentence dropped; the
	// salvage still has to name the user and speak the room language.
	if clean := fitSentences(withoutQuestions(candidate)); good(clean) {
		return clean
	}
	return FallbackGreeting(name, publicBio, russian)
}

// fitSentences keeps the longest run of complete sentences that fits the
// message limit: a greeting cut mid-word ("... пахнет ванилью и") reads
// broken in the chat. The sentence carrying a question mark is dropped
// entirely - greetings are statements only. A tail after the last sentence
// ender that carries no letters (emoji, punctuation) is decoration and
// stays: "... до связи 👋" is a complete, living greeting.
func fitSentences(s string) string {
	if q := strings.IndexRune(s, '?'); q >= 0 {
		head := ""
		for i := q; i >= 0; i-- {
			if strings.ContainsRune(".!", rune(s[i])) {
				head = s[:i+1]
				break
			}
		}
		s = strings.TrimSpace(head)
		if s == "" {
			return s
		}
	}
	runes := []rune(s)
	lastEnder := -1
	for i, r := range runes {
		if r == '.' || r == '!' || r == '…' {
			lastEnder = i
		}
	}
	if lastEnder < 0 {
		// No sentence ender at all: the text was cut mid-sentence.
		return trimAndLimit(runes, model.MaxMessageContentLen)
	}
	// Letters after the last sentence ender mean a truncated tail; emoji
	// and punctuation after it are just decoration.
	for _, r := range runes[lastEnder+1:] {
		if unicode.IsLetter(r) {
			return strings.TrimSpace(string(runes[:lastEnder+1]))
		}
	}
	return strings.TrimSpace(s)
}

// greetingPrompt builds the one-message generation prompt for a greeting:
// an introduction from the bios, first person, no questions.
func greetingPrompt(name, publicBio, privateBio string, russian bool) []llmMessage {
	pub, priv := bioOrNone(publicBio), bioOrNone(privateBio)
	if russian {
		return []llmMessage{{Role: "user", Content: fmt.Sprintf(`Напиши фиксированное приветственное сообщение, которое участник чата %s отправляет самым первым, когда открывает разговор с незнакомцем.
Открытая биография (что о нём знают): %s
Личная биография (только для тона, не раскрывай): %s
Пиши от первого лица от имени %s: одно-два коротких разговорных предложения — имя и чем занимается. До 200 символов. В приветствии НЕ должно быть вопроса и знака вопроса — только утверждения. Тепло и по-человечески, как настоящее приветствие в мессенджере. Один эмодзи к месту (например 👋 или 😊) оживит приветствие, но можно и совсем без эмодзи. Выведи только текст приветствия: без кавычек, подписей вроде «Пользователь:», markdown и адресов.`, name, pub, priv, name)}}
	}
	return []llmMessage{{Role: "user", Content: fmt.Sprintf(`Write the fixed greeting message that the chat user %s sends first when opening a conversation with a stranger.
Public bio (what everyone knows about them): %s
Private bio (tone only, do not disclose): %s
Write in first person as %s: one or two short casual sentences introducing the name and what they do, under 200 characters. The greeting must NOT contain a question or a question mark - statements only. Warm and human, like a real messenger hello. One fitting emoji (like 👋 or 😊) makes it feel alive, but a greeting without any emoji is fine too. Output the greeting text only: no quotes, no labels like "User:", no markdown, no addresses.`, name, pub, priv, name)}}
}

// withoutQuestions drops the sentence that carries the first question mark
// (usually the closing question of a draft greeting) and keeps the rest.
func withoutQuestions(s string) string {
	q := strings.IndexRune(s, '?')
	if q < 0 {
		return s
	}
	cut := 0
	for i := q; i >= 0; i-- {
		if strings.ContainsRune(".!", rune(s[i])) {
			cut = i + 1 // keep the sentence ender
			break
		}
	}
	return strings.TrimSpace(s[:cut])
}

// FallbackGreeting builds a plain greeting from the public bio when the
// LLM is unavailable: still non-empty, still question-free.
func FallbackGreeting(name, publicBio string, russian bool) string {
	bio := firstSentence(publicBio)
	bio = strings.TrimPrefix(bio, name)
	bio = strings.TrimSpace(bio)
	if russian {
		if bio == "" {
			return "Привет, я " + name + ". 👋"
		}
		return "Привет, я " + name + " — " + bio + ". 👋"
	}
	if bio == "" {
		return "Hi, I'm " + name + ". 👋"
	}
	return "Hi, I'm " + name + " — " + bio + ". 👋"
}

// firstSentence returns the opening sentence of the bio (cut at the first
// sentence ender or newline) without any question part, capped for a
// one-line greeting.
func firstSentence(bio string) string {
	bio = strings.TrimSpace(strings.SplitN(bio, "\n", 2)[0])
	bio = withoutQuestions(bio)
	cut := len([]rune(bio))
	for i, r := range bio {
		if strings.ContainsRune(".!", r) {
			cut = i + 1
			break
		}
	}
	runes := []rune(bio)
	if cut > len(runes) {
		cut = len(runes)
	}
	if cut > 120 {
		trimmed := []rune(bio)[:120]
		if sp := lastIndexSpace(trimmed, len(trimmed)-1); sp > 0 {
			trimmed = trimmed[:sp]
		}
		bio = strings.TrimSpace(string(trimmed))
		return bio
	}
	return strings.TrimSpace(string(runes[:cut]))
}

// greetingOrFallback returns the greeting a user posts at the start of a
// chat: the stored greeting when present, otherwise the chat-language
// template (legacy users created before the greeting field existed).
func greetingOrFallback(u model.User, language string) string {
	if g := strings.TrimSpace(u.Greeting); g != "" {
		return g
	}
	return FallbackGreeting(u.Name, u.PublicBio, isRussian(language))
}
