package agent

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/extube/bleak-node/internal/model"
)

// transcriptLimit keeps prompts small: only the most recent messages are
// replayed to the LLM.
const transcriptLimit = 12

// buildPrompt assembles the system and user prompt for the next message of
// the speaker inside the chat. Agent behaviour depends on the public and
// private bios of the user. The nudge (may be empty) adds an extra warning,
// e.g. when the model must avoid repeating its earlier lines.
func buildPrompt(chat string, speaker, listener model.User, msgs []model.Message, myTurns int, overLimit bool, nudge string) []llmMessage {
	sys := fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio (what everyone knows about you): %s
Private bio (facts only you know; your hidden feelings and goals): %s
Stay strictly in character and let both bios shape your tone, vocabulary and goals.
Never copy or paraphrase lines from the transcript: every message you write must bring something new.
Always answer with exactly ONE plain-text chat message of at most %d symbols, addressed to the other chat participant. Never add quotes, prefixes like "Agent:", markdown or explanations.`,
		speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio), model.MaxMessageContentLen)

	var b strings.Builder
	if overLimit {
		b.WriteString(fmt.Sprintf(`IMPORTANT: this is the END of the chat. Your next message MUST clearly say goodbye (mention "%s" by name, thank them and say goodbye).
Send ONLY a farewell message, keep it under %d symbols, and never write raw addresses.`, listener.Name, model.MaxMessageContentLen))
	} else if myTurns == 0 {
		b.WriteString(fmt.Sprintf(`This is your first message in this chat with %s: send your hello.
Open with a short greeting and introduce yourself in character, using your public and private bio lines.`, listener.Name))
	} else {
		b.WriteString(fmt.Sprintf(`You already spoke before: continue this conversation with %s.
The last message above was written by %s - answer THAT message, subject first. Do not introduce yourself again and do not repeat what you already said.
One short plain-text message.`, listener.Name, listener.Name))
	}
	b.WriteString("\n\nNever write raw addresses (0x...) in your message; call the other participant by name.\n\n")
	if nudge != "" {
		b.WriteString(nudge + "\n\n")
	}
	b.WriteString(transcript(chat, speaker, listener, msgs))
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// transcript renders the most recent messages oldest-first.
func transcript(chat string, speaker, listener model.User, msgs []model.Message) string {
	if len(msgs) > transcriptLimit {
		msgs = msgs[len(msgs)-transcriptLimit:]
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Chat %s transcript, oldest first:\n", chat))
	for _, m := range msgs {
		name := fromName(m.From, speaker, listener)
		fmt.Fprintf(&b, "%s: %s\n", name, m.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}

func fromName(address string, speaker, listener model.User) string {
	switch address {
	case speaker.Address:
		return speaker.Name
	case listener.Address:
		return listener.Name
	default:
		return address
	}
}

func bioOrNone(bio string) string {
	if strings.TrimSpace(bio) == "" {
		return "(none)"
	}
	return strings.TrimSpace(bio)
}

// normalize maps message text to a comparable key (lowercase, no punctuation,
// single spaces) so anti-echo checks ignore surface formatting.
func normalize(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// sanitizeContent cleans the LLM output before saving it as a message.
func sanitizeContent(s string) string {
	// Drop markdown code fences if the model wrapped its answer.
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var kept []string
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				continue
			}
			kept = append(kept, line)
		}
		s = strings.TrimSpace(strings.Join(kept, "\n"))
	}
	// Remove one wrapping pair of quotes.
	s = strings.TrimSpace(s)
	for _, q := range []string{`"`, "'", "`", "“", "“”"} {
		if len(s) >= 2 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			s = strings.TrimSpace(s[1 : len(s)-len(q)])
		}
	}
	// Enforce the message length limit.
	r := []rune(s)
	if len(r) > model.MaxMessageContentLen {
		r = r[:model.MaxMessageContentLen]
	}
	return strings.TrimSpace(string(r))
}
