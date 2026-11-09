package agent

import (
	"crypto/rand"
	"encoding/binary"
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
// private bios of the user plus the chat theme scenario. The nudge (may be
// empty) adds an extra warning, e.g. when the model must avoid repeating
// its earlier lines.
func buildPrompt(theme Theme, phase string, speaker, listener model.User, msgs []model.Message, speakerTurns int, overLimit bool, nudge string) []llmMessage {
	sys := fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio (what everyone knows about you): %s
Private bio (facts only you know; your hidden feelings and goals): %s
Stay strictly in character and let both bios shape your tone and goals.
HARD FACT RULE: speak ONLY from what your bios state, the current transcript and plain common sense; NEVER invent specific personal facts (family, kids, pets, trips, won awards, employers, possessions) that the bios do not mention; when a topic falls outside the bios, answer in abstract, general terms instead of making things up.
Write like a REAL person texting in a messenger: short, casual lines, contractions, everyday words.
BREVITY IS THE RULE: the WHOLE reply is TWO short sentences at most (about 25 words total, under 200 characters), ONE thought per message.
Your reply is split into chat messages sentence by sentence: put ONE question in the reply at most, and make it the LAST sentence: everything after the first question mark is discarded.
Never copy or paraphrase lines from the transcript: every message you write must bring something new.
Never add quotes, prefixes like "Agent:", markdown or explanations; never write raw addresses (0x...); call the other participant by name.`,
		speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))

	var b strings.Builder
	// The theme belongs to the debate phase only; the networking phase runs on
	// the agents' own bios.
	if phase == model.PhaseDebate {
		b.WriteString(themeBrief(theme) + "\n\n")
	}
	b.WriteString(fmt.Sprintf(`This chat is between %s and %s, both agents; you are %s.`, speaker.Name, listener.Name, speaker.Name) + "\n\n")
	switch {
	case overLimit:
		// Unreachable through buildPrompt: farewells use buildGoodbyePrompt.
	case phase == model.PhaseDebate && speakerTurns == 0:
		b.WriteString(fmt.Sprintf(`This is your opening message of the themed part of this chat: share your first view of the theme through your own work or hobby experience and ask the partner their view.
Theme opening: %s
Answer in ONE short opening message (not split), casual and natural.`, theme.Scenario.Hello))
	case speakerTurns == 0:
		b.WriteString(fmt.Sprintf(`NETWORKING - step BEFORE. You are reaching out to know %s through their work or hobby; people come first, contacts never.
Introduce yourself in ONE short sentence: name plus what you actually do, and finish by asking what they are focused on these days.
Answer in ONE intro message, two short sentences at most.`, listener.Name))
	case phase == model.PhaseDebate:
		b.WriteString(fmt.Sprintf(`Continue this themed conversation with %s.
Theme directive: %s
The last message above was written by %s - answer THAT message, subject first. Do not repeat what you already said; keep the debate friendly and personal.`, listener.Name, theme.Scenario.Context, listener.Name))
	case phase == model.PhaseNetworking:
		b.WriteString(fmt.Sprintf(`NETWORKING - an even two-way talk between people curious about crafts: YOU share, THEY share.
The last message above was written by %s - answer THAT message, subject first:
- give one NEW concrete detail about your own WORK, STARTUP or HOBBY, taken only from your bios (something the transcript has not covered yet);
- then ask ONE short open question about %s's work, business or hobby - your curiosity, not an interview.
No re-introductions, no repeating yourself, never invent facts about your life, no contact details or concrete appointments.`, listener.Name, listener.Name))
	}
	if nudge != "" {
		b.WriteString("\n\n" + nudge)
	}
	b.WriteString("\n\n" + transcript(speaker, listener, msgs))
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// buildGoodbyePrompt assembles the farewell prompt WITHOUT the transcript
// and without most context: the goodbye stays vague and polite, like the
// common ending of an everyday conversation. When the listener left an open
// question, its text rides along so the answer-first rule can reference it.
func buildGoodbyePrompt(theme Theme, speaker model.User, last string) []llmMessage {
	sys := fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio (what everyone knows about you): %s
Private bio (facts only you know; your hidden feelings and goals): %s
Stay strictly in character but keep it light: write like a REAL person texting in a messenger such as WhatsApp or Telegram.
Never add quotes, prefixes like "Agent:", markdown or explanations; never write raw addresses (0x...).`,
		speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))

	b := strings.Builder{}
	b.WriteString(`IMPORTANT: this is the END of the chat. Send ONLY a farewell message.
Rules for the farewell:
- exactly ONE short plain-text message (ONE or TWO short sentences, under 200 characters);
- if the partner left an OPEN QUESTION in their last message, answer it in the FIRST sentence, briefly and honestly — real people do that before leaving;
- after the answer (or from the start if there was no question): START with a VAGUE abstract impression of the whole conversation, like everyday small talk wrap-ups: "It's all been cool, but I have to head out", "Well, it was nice talking to you, but I need to run", "This chat was a pleasure, though it's getting late";
- add a light, CASUAL reason to leave (errands, a shift, it's late, an appointment) and finish with a polite common goodbye ("take care", "see you around");
- apart from that first answer, do NOT name the listener or use addresses; do NOT mention any concrete topic, fact, opinion or phrase from the conversation: the impression must stay vague and generic ("it was cool"), never analytical;
- forget everything else that was written in this chat before your farewell.
Farewell direction from the scenario: ` + theme.Scenario.Goodbye)
	if answer := questionForFarewell(last); answer != "" {
		b.WriteString("\nThe partner's last message: " + answer)
	}
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// questionForFarewell returns the partner's last message for the farewell
// prompt, only when it asks an open question; empty otherwise.
func questionForFarewell(last string) string {
	s := strings.TrimSpace(last)
	if s == "" || !strings.Contains(s, "?") {
		return ""
	}
	return s
}

// themeBrief describes the shared situation of the chat for the prompt.
func themeBrief(theme Theme) string {
	return fmt.Sprintf("Chat theme: %s - %s", theme.Name, theme.Description)
}

// transcript renders each distinct message at its most recent position,
// oldest first. De-duplicated lines remove the repetition walls the small
// models tend to amplify (echo loops).
func transcript(speaker, listener model.User, msgs []model.Message) string {
	if len(msgs) > transcriptLimit {
		msgs = msgs[len(msgs)-transcriptLimit:]
	}
	seen := make(map[string]struct{}, len(msgs))
	kept := make([]model.Message, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		key := normalize(msgs[i].Content)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, msgs[i])
	}
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	var b strings.Builder
	b.WriteString("Chat transcript, distinct lines, oldest first:\n")
	for _, m := range kept {
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

// isEcho reports whether the candidate reply repeats one of the agent's
// earlier lines, either verbatim (after normalization) or overlapping them
// heavily (Jaccard >= echoSimThreshold).
func isEcho(candidate string, prior map[string]struct{}) bool {
	if _, dup := prior[candidate]; dup {
		return true
	}
	cand := strings.Fields(candidate)
	if len(cand) == 0 {
		return false
	}
	for line := range prior {
		if jaccard(cand, strings.Fields(line)) >= echoSimThreshold {
			return true
		}
	}
	return false
}

// echoSimThreshold is the token overlap above which two lines are considered
// the same thought re-parroted.
const echoSimThreshold = 0.75

// jaccard returns the intersection/union ratio of two token sets.
func jaccard(a, b []string) float64 {
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	inter, union := 0, len(setA)
	for _, t := range b {
		if _, ok := setA[t]; ok {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// buildTransitionPrompt assembles the message that closes the networking
// part of the chat. It plays the checklist AFTER step in messenger form:
// the speaker mentions something specific from the conversation, offers a
// useful resource or a thoughtful introduction, proposes the abstract theme
// as the next stage and promises to keep in touch without pressure.
func buildTransitionPrompt(theme Theme, speaker, listener model.User, msgs []model.Message) []llmMessage {
	sys := fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio (what everyone knows about you): %s
Private bio (facts only you know; your hidden feelings and goals): %s
Stay in character; write like a REAL person texting in a messenger: short, casual, friendly, no pressure.
Never add quotes, prefixes like "Agent:", markdown or explanations; never write raw addresses (0x...); call the other participant by name.`,
		speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))

	b := strings.Builder{}
	b.WriteString(fmt.Sprintf(`NETWORKING - step AFTER. Your networking stage of this chat ends now; send EXACTLY ONE personal message that
- mentions something SPECIFIC you learned about %s's work, business or hobby here (one concrete detail, one sentence);
- offers something useful for their project: a resource, an example, or a thoughtful idea - not your contact details;
- proposes moving on together for a while to the theme "%s" (the themed stage of this chat);
- names a light, low-pressure next step in words only, if you both would want it: how you might prefer to stay in touch or what could be discussed next time.
ONE short plain-text message, no question mark (their answer follows separately). NEVER share phone numbers, handles, e-mails or addresses and never form concrete appointments.`, listener.Name, theme.Name))
	b.WriteString("\n\n" + transcript(speaker, listener, msgs))
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// networkingSeeds are turn directives for the networking phase; they rotate
// through the curiosity lists so every turn discovers or presents a new
// work/business/hobby angle instead of drifting elsewhere.
var networkingSeeds = []string{
	"SUBJECT: only your craft. Ask what the partner is focused on these days in their work, business or hobby - nothing else.",
	"SUBJECT: only your craft. Ask what they enjoy or care about OUTSIDE their main work, and what it looks like.",
	"SUBJECT: only your craft. Ask what they are working toward right now; add one line about your own current goal.",
	"SUBJECT: only your craft. Ask what challenge or opportunity they are navigating lately in their project.",
	"SUBJECT: only your craft. Ask what strength, skill or experience they are proud of; say one of yours.",
	"SUBJECT: only your craft. Ask what kind of people, ideas or opportunities are useful to them now.",
	"SUBJECT: only your craft. Give ONE short, concrete story that makes your work or hobby memorable.",
	"SUBJECT: only your craft. Name one way you could be genuinely helpful in the partner's field.",
	"SUBJECT: only your craft. Compare the two of you: what is more rewarding - your day job or the hobby craft, and why.",
	"SUBJECT: only your craft. Tell what would make this conversation valuable for you and ask the partner the same.",
}

// topicSeeds are concrete turn directives for themed debates; small chat
// models follow them far better than abstract "discuss further" warnings.
var topicSeeds = []string{
	"Give one concrete everyday example of the theme from your own work or hobby.",
	"Ask the partner for their blunt opinion on one controversial side of the theme.",
	"Tell how the theme showed up in your life this week...",
	"Play the opposite view for one turn, then find the common ground.",
	"Ask what the partner expects the theme to look like in ten years.",
	"Point to one everyday detail of life that the theme quietly changes.",
	"Agree with the partner's last thought and add your own small twist.",
	"Ask what the partner would change first about how the world handles this theme.",
	"Admit one doubt or fear about the theme and ask the partner to soften it.",
	"Compare the theme with something small you both met in daily life.",
}

// topicSeed picks a deterministic directive for this turn: the same turn of
// the same chat always gets the same topic, neighbouring turns rotate
// through the list. The first turn keeps its scenario hello prompt.
func topicSeed(ev model.Event, phase string, turnNumber, attempt int, overLimit bool) string {
	if overLimit {
		// The farewell prompt already fully specifies the content.
		return ""
	}
	if turnNumber == 0 {
		// Keep the hello prompt of buildPrompt untouched.
		return ""
	}
	seed := ""
	if phase == model.PhaseDebate {
		seed = topicSeeds[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(topicSeeds)]
	} else {
		seed = networkingSeeds[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(networkingSeeds)]
	}
	return "This turn must cover: " + seed + " KEEP THE REPLY SHORT: two chat-short sentences and no more."
}

// lastListenerMessage returns the most recent message the listener sent
// before this turn, so the prompts can point the speaker at it.
func lastListenerMessage(listener model.User, msgs []model.Message) (string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].From == listener.Address {
			return msgs[i].Content, true
		}
	}
	return "", false
}

// questionDirect builds the answer-first nudge for a turn when the listener
// left an open question in their last message; such a turn owns the answer
// instead of the rotating topic seed.
func questionDirect(listener model.User, lastMsg string) string {
	if !strings.Contains(lastMsg, "?") {
		return ""
	}
	return fmt.Sprintf(`%s asked you a question above (their last message). START your reply by answering it directly in one plain sentence, then add your one craft detail; the rotating topic of this chat waits until the next turn.`, listener.Name)
}

// goodbyeReplies are pattern acknowledgements a listener may answer a
// farewell with: short, polite, generic — no conversation content. The
// worker picks one of them locally without calling the LLM.
var goodbyeReplies = []string{
	"Ha, sure thing! It was really nice talking with you too - take care!",
	"Thanks, you too! Have a good one and see you around.",
	"Likewise! Was a pleasure chatting. Good luck out there!",
	"Take care too! Let's chat again some other day.",
	"Sounds good, thanks! Have a great day and stay well.",
	"Thanks a lot, same here! Keep in touch and be safe.",
}

// goodbyeReply returns one greeting-like reply to a farewell.
func goodbyeReply() string {
	return sanitizeContent(goodbyeReplies[randInt(len(goodbyeReplies))])
}

// themeByKey returns the theme with the given catalog key, or nil.
func themeByKey(key string) *Theme {
	for i := range Themes {
		if Themes[i].Key == key {
			return &Themes[i]
		}
	}
	return nil
}

// randInt returns a uniform pseudo-random value in [0, n).
func randInt(n int) int {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return int(binary.BigEndian.Uint64(buf[:]) % uint64(n))
}

// fnv32a is a tiny deterministic string hasher (standalone, no imports).
func fnv32a(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

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
	// Enforce the message length limit, cutting on the last word boundary
	// so a bubble never ends mid-word ("keep up wi").
	r := []rune(s)
	if len(r) > model.MaxMessageContentLen {
		if sp := lastIndexSpace(r, model.MaxMessageContentLen); sp > 0 {
			r = r[:sp]
		} else {
			r = r[:model.MaxMessageContentLen]
		}
	}
	return strings.TrimSpace(string(r))
}

// sentenceEnders are the terminal punctuation marks that close a sentence.
const sentenceEnders = ".!?\u2026"

// noSplitAfter lists abbreviations whose dot must not end a message.
var noSplitAfter = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "jr": true, "sr": true,
	"st": true, "no": true, "vs": true, "etc": true, "eg": true, "ie": true,
}

// maxTurnMessages caps how many sentence messages ONE interaction may
// produce, keeping the dialog max clear for both readers. Two bubbles is the
// texting norm: one statement plus the question.
const maxTurnMessages = 2

// splitIntoMessages cuts a model reply into separate chat messages, one per
// sentence (like people texting in messengers). Two dialog-clarity rules
// ride on top of the sentence split:
//   - everything after the FIRST question mark is discarded, so one turn
//     carries at most one question and the listener can answer it;
//   - the number of messages per turn is capped at maxTurnMessages.
//
// The very first sentence of the conversation (hello) and the farewell stay
// undivided: the caller passes them through a splitter that returns a
// single part.
func splitIntoMessages(reply string, limit int) []string {
	s := strings.TrimSpace(reply)
	if s == "" {
		return nil
	}
	// Dialog clarity: drop everything after the first question mark; the
	// question itself stays and closes the answer.
	if q := strings.IndexRune(s, '?'); q >= 0 {
		s = strings.TrimSpace(s[:q+1])
	}
	runes := []rune(s)
	var (
		parts []string
		start = 0
	)
	for i := 0; i < len(runes); i++ {
		if !strings.ContainsRune(sentenceEnders, runes[i]) {
			continue
		}
		if !sentenceBoundary(runes, i) {
			continue
		}
		if part := trimAndLimit(runes[start:i+1], limit); part != "" {
			parts = append(parts, part)
		}
		start = i + 1
	}
	if rest := trimAndLimit(runes[start:], limit); rest != "" {
		parts = append(parts, rest)
	}
	if len(parts) == 0 {
		parts = append(parts, trimAndLimit(runes, limit))
	}
	// At most maxTurnMessages bubbles per interaction; drop the overflow.
	if len(parts) > maxTurnMessages {
		parts = parts[:maxTurnMessages]
	}
	return parts
}

// sentenceBoundary reports whether the ender at i really closes a sentence:
// it must be followed by a space/newline or the end of text, the next word
// must not look like an abbreviation continuation, and the symbol right
// after the space must be upper-case (a letter/digit is enough) or nothing.
func sentenceBoundary(r []rune, i int) bool {
	// Swallow consecutive enders ("!!", "?!") - split only at the last one.
	if i+1 < len(r) && strings.ContainsRune(sentenceEnders, r[i+1]) {
		return false
	}
	if i+1 == len(r) {
		return true
	}
	if r[i+1] != ' ' && r[i+1] != '\n' && r[i+1] != '\t' {
		return false
	}
	// Find the first meaningful rune after the whitespace.
	j := i + 1
	for j < len(r) && (r[j] == ' ' || r[j] == '\n' || r[j] == '\t') {
		j++
	}
	if j >= len(r) {
		return true
	}
	if unicode.IsLower(r[j]) {
		return false
	}
	// Abbreviation guard: the word before the dot ("Mr.", "e.g.") keeps the
	// sentence together.
	return !noSplitAfter[trailingWordLower(r, i)]
}

// trailingWordLower returns the last word before position end in lowercase.
func trailingWordLower(r []rune, end int) string {
	start := end
	for start > 0 && !unicode.IsSpace(r[start-1]) && !strings.ContainsRune(sentenceEnders, r[start-1]) {
		start--
	}
	w := r[start:end]
	for len(w) > 0 && w[len(w)-1] == '.' {
		w = w[:len(w)-1]
	}
	return strings.ToLower(string(w))
}

// whitespaceRe collapses newlines and tabs into spaces so every part reads
// as one chat bubble.
var whitespaceRe = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")

// trimAndLimit cleans a sentence slice (whitespace, wrapping quotes) and
// caps it at the message limit, cutting on the last word boundary when it
// can.
func trimAndLimit(r []rune, limit int) string {
	s := strings.TrimSpace(whitespaceRe.Replace(string(r)))
	s = stripWrappingQuotes(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	cut := limit
	if sp := lastIndexSpace(runes, limit); sp > 0 {
		cut = sp
	}
	return strings.TrimSpace(string(runes[:cut]))
}

// stripWrappingQuotes removes one pair of wrapping quotes. Both split parts
// and whole replies can end up quoted when the model answers its own
// simulated transcript.
func stripWrappingQuotes(s string) string {
	for _, q := range []string{`"`, "'", "`", "“", "”"} {
		if len(s) >= 2 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			s = strings.TrimSpace(s[1 : len(s)-len(q)])
		}
	}
	return s
}

// lastIndexSpace returns the last index of a space at or before from.
func lastIndexSpace(r []rune, from int) int {
	for i := from; i > 0; i-- {
		if r[i] == ' ' {
			return i
		}
	}
	return -1
}
