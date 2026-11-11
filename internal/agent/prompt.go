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

// buildPrompt assembles a language-specific system and user prompt for the
// next message of the speaker inside the chat.
func buildPrompt(theme Theme, phase string, speaker, listener model.User, msgs []model.Message, speakerTurns int, overLimit bool, nudge, language string) []llmMessage {
	russian := isRussian(language)
	if russian {
		theme = localizedTheme(theme, language)
	}
	var sys string
	if russian {
		sys = fmt.Sprintf(`Ты отыгрываешь участника чата "%s" (адрес %s) в небольшой социальной сети.
Открытая биография (то, что знают другие): %s
Личная биография (то, что знаешь только ты; скрытые чувства и цели): %s
Оставайся в образе. Открытые факты можно упоминать свободно; личные раскрывай только если это естественно для разговора, не выкладывай их списком.
ФАКТЫ: опирайся только на биографии, видимую переписку и обычный здравый смысл. Не выдумывай конкретные факты о себе. Если нового факта нет, просто отреагируй на разговор — не придумывай его.
ЖИВОЙ ДИАЛОГ: отвечай на смысл последнего сообщения собеседника. Можно коротко отозваться на его мысль своими словами, не повторяя дословно. Если человек делится проблемой или чувствами, сначала отреагируй на это. Продолжай текущую тему, если нет естественного повода перейти дальше.
Пиши как обычный человек в мессенджере: коротко, непринуждённо и по-человечески, без делового или рекламного тона. Меняй ритм реплик; не задавай вопрос в каждом сообщении, не подводи итоги и не используй одни и те же заготовки. Сленг и эмодзи — только если это действительно к месту.
Одна естественная реплика из одного-двух коротких предложений, примерно до 25 слов и 200 символов. Не больше одного вопроса, он может быть только в конце и только если продолжает разговор. Иногда отвечай без вопроса.
Не добавляй кавычки, подписи вроде «Агент:», Markdown или объяснения. Не пиши адреса 0x...; называй собеседника по имени, когда это звучит естественно. Весь текст реплики должен быть на русском языке.`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
	} else {
		sys = fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio (what everyone knows about you): %s
Private bio (facts only you know; your hidden feelings and goals): %s
Stay in character. Public facts may be shared freely; reveal private details only when they fit naturally, never as a list.
FACTS: Use only the bios, visible transcript, and plain common sense for personal facts. Never invent a specific fact about yourself. If you have no fresh detail, react to the conversation instead of making one up.
NATURAL CHAT: Respond to the meaning of the partner's latest message. You may acknowledge their point in your own words without echoing it. If they share a problem or feeling, respond to that before changing the subject. Stay with the current thread unless there is a natural reason to move on.
Write like a real person texting one person: casual, concise, and warm, not like a service bot or an interview. Vary the rhythm; don't ask a question every turn, summarize the conversation, or reuse stock phrases. Use slang or emoji only when it feels natural.
Write one or two short natural sentences, about 25 words and under 200 characters. At most one question, at the end, and only if it continues the conversation. Sometimes reply without a question.
Never add quotes, labels like "Agent:", markdown, or explanations; never write raw addresses (0x...). Use the other person's name only when it sounds natural.`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
	}

	var b strings.Builder
	// The theme belongs to the debate phase only; the networking phase runs on
	// the agents' own bios.
	if phase == model.PhaseDebate {
		b.WriteString(themeBrief(theme, language) + "\n\n")
	}
	if russian {
		b.WriteString(fmt.Sprintf("В этом чате %s и %s; ты — %s.\n\n", speaker.Name, listener.Name, speaker.Name))
	} else {
		b.WriteString(fmt.Sprintf("This chat is between %s and %s; you are %s.\n\n", speaker.Name, listener.Name, speaker.Name))
	}
	switch {
	case overLimit:
		// Unreachable through buildPrompt: farewells use buildGoodbyePrompt.
	case phase == model.PhaseDebate && speakerTurns == 0:
		if russian {
			b.WriteString(fmt.Sprintf("Это начало тематической части. Коротко и непринуждённо выскажи своё первое мнение, опираясь на работу или увлечение, и спроси мнение собеседника.\nТема: %s\n", theme.Scenario.Hello))
		} else {
			b.WriteString(fmt.Sprintf("This is the opening of the themed part. Share your first view briefly through your own work or hobby, and ask the partner's view.\nTheme opening: %s\n", theme.Scenario.Hello))
		}
	case phase == model.PhaseNetworking && speakerTurns == 1 && len(msgs) == 2 && msgs[0].From == speaker.Address:
		// The greeting messages are exchanged (both are stored user
		// greetings, no questions): the chat opener continues with the
		// first question of the conversation instead of re-introducing.
		if russian {
			b.WriteString(fmt.Sprintf("Приветствия уже обменяны: вы уже представились в своём приветствии — не представляйтесь снова.\nПродолжите разговор: естественно отреагируйте на приветствие %s и задайте один короткий открытый вопрос о его занятии или увлечении.\n", listener.Name))
		} else {
			b.WriteString(fmt.Sprintf("The greeting messages have been exchanged: you already introduced yourself in your greeting - do not introduce yourself again.\nContinue the conversation: respond naturally to %s's greeting, then ask ONE short open question about their work, business or hobby.\n", listener.Name))
		}
	case phase == model.PhaseDebate:
		if russian {
			b.WriteString(fmt.Sprintf("Продолжай тематический разговор с %s.\nНаправление темы: %s\nСначала отреагируй на последнее сообщение %s, затем продолжай дружелюбно и лично. Не повторяй уже сказанное.\n", listener.Name, theme.Scenario.Context, listener.Name))
		} else {
			b.WriteString(fmt.Sprintf("Continue this themed conversation with %s.\nTheme direction: %s\nRespond to %s's latest message first; keep it friendly and personal, and don't repeat what you already said.\n", listener.Name, theme.Scenario.Context, listener.Name))
		}
	case phase == model.PhaseNetworking:
		if russian {
			b.WriteString(fmt.Sprintf("Знакомьтесь в ходе живого равного разговора. Сначала ответь на последнее сообщение %s и на его вопрос, если он есть; отзовись на важную мысль или чувство. Если уместно, добавь один новый факт из своей биографии, которого ещё не было в видимой переписке. Задавай короткий открытый вопрос, только если он естественно продолжает тему и ответ ещё не прозвучал. Не превращай разговор в интервью. Не выдумывай факты, не перескакивай на случайную тему, не предлагай конкретных встреч и не делись контактами.\n", listener.Name))
		} else {
			b.WriteString(fmt.Sprintf("Get to know each other through a natural, balanced conversation. First respond to %s's latest message and answer any question; acknowledge an important detail or feeling. Add a fresh detail from your bio only if it fits and has not appeared in the visible transcript. Ask a short open question only when it naturally continues an unanswered thread. Don't turn this into an interview. Never invent facts, force a topic, suggest an appointment, or share contact details.\n", listener.Name))
		}
	}
	if nudge != "" {
		b.WriteString("\n\n" + nudge)
	}
	b.WriteString("\n\n" + transcript(speaker, listener, msgs, language))
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// buildGoodbyePrompt assembles the farewell prompt WITHOUT the transcript
// and without most context: the goodbye stays warm, casual and free of new
// topics, like the common ending of an everyday conversation. When the
// listener left an open question, its text rides along so the answer-first
// rule can reference it.
func buildGoodbyePrompt(theme Theme, speaker model.User, last, language string) []llmMessage {
	russian := isRussian(language)
	if russian {
		theme = localizedTheme(theme, language)
	}
	var sys string
	var instructions string
	if russian {
		sys = fmt.Sprintf(`Ты — %s (адрес %s), участник небольшого чата.
Открытая биография: %s
Личная биография: %s
Оставайся в образе и пиши естественно, как человек в мессенджере. Не выдумывай личные факты. Весь текст — на русском. Не добавляй подписи, Markdown или адреса 0x... .`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
		instructions = `Чат заканчивается. Напиши одно короткое прощальное сообщение из одного-двух предложений.
Если в последнем сообщении собеседник задал вопрос и ещё не получил ответ, сначала коротко и честно ответь на него.
Затем просто и тепло попрощайся. Не задавай новый вопрос и не начинай новую тему. Не выдумывай причину ухода — достаточно сказать, что тебе пора. Не упоминай детали разговора, кроме тех, что нужны для ответа. Имя собеседника и адреса не нужны. Закончи обычным «пока», «береги себя» или «до связи».
` + "Пожелание сценария: " + localizedTheme(theme, language).Scenario.Goodbye
	} else {
		sys = fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio: %s
Private bio: %s
Stay in character and write naturally, like a person texting. Do not invent personal facts. Never add labels, markdown, explanations, or raw addresses (0x...).`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
		instructions = `The chat is ending. Send one short farewell message of one or two sentences.
If the partner's latest message contains an unanswered question, answer it briefly and honestly first.
Then close warmly and casually. Do not ask another question or introduce a new topic. Do not invent a reason for leaving; “I should get going” is enough. Do not mention conversation details except what is needed to answer. Do not name the listener or write raw addresses. Finish with a common goodbye.
Farewell direction: ` + theme.Scenario.Goodbye
	}

	b := strings.Builder{}
	b.WriteString(instructions)
	if answer := questionForFarewell(last); answer != "" {
		if russian {
			b.WriteString("\nПоследнее сообщение собеседника: " + answer)
		} else {
			b.WriteString("\nThe partner's last message: " + answer)
		}
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
func themeBrief(theme Theme, language string) string {
	if isRussian(language) {
		theme = localizedTheme(theme, language)
		return fmt.Sprintf("Тема чата: %s — %s", theme.Name, theme.Description)
	}
	return fmt.Sprintf("Chat theme: %s - %s", theme.Name, theme.Description)
}

// transcript renders each distinct message at its most recent position,
// oldest first. De-duplicated lines remove the repetition walls the small
// models tend to amplify (echo loops).
func transcript(speaker, listener model.User, msgs []model.Message, language string) string {
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
	if isRussian(language) {
		b.WriteString("Переписка, без повторов, от старых сообщений к новым:\n")
	} else {
		b.WriteString("Chat transcript, distinct lines, oldest first:\n")
	}
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
func buildTransitionPrompt(theme Theme, speaker, listener model.User, msgs []model.Message, language string) []llmMessage {
	russian := isRussian(language)
	if russian {
		theme = localizedTheme(theme, language)
	}
	var sys, task string
	if russian {
		sys = fmt.Sprintf(`Ты — %s (адрес %s), участник небольшого чата.
Открытая биография: %s
Личная биография: %s
Пиши коротко, дружелюбно и непринуждённо, как человек в мессенджере. Не выдумывай личные факты. Весь текст — на русском. Не добавляй подписи, Markdown или адреса 0x... .`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
		task = fmt.Sprintf(`Знакомство заканчивается. Напиши %s одно короткое личное сообщение, которое естественно подводит итог знакомству:
- отзовись на конкретную вещь, которую узнал(а) о занятии или увлечении %s;
- предложи полезную идею, только если она действительно уместна и не требует выдуманных знаний;
- ненавязчиво предложи перейти к теме «%s»;
- без вопроса, контактов и конкретных договорённостей о встрече. Не превращай это в список или рекламный текст.`, listener.Name, listener.Name, theme.Name)
	} else {
		sys = fmt.Sprintf(`You are role-playing the chat agent "%s" (address %s) in a small peer-to-peer social network.
Public bio: %s
Private bio: %s
Write briefly, casually, and warmly, like a person texting. Do not invent personal facts. Never add labels, markdown, explanations, or raw addresses (0x...).`,
			speaker.Name, speaker.Address, bioOrNone(speaker.PublicBio), bioOrNone(speaker.PrivateBio))
		task = fmt.Sprintf(`The getting-to-know-you part is ending. Send %s one short personal message that naturally wraps up the introduction:
- react to one specific thing you learned about %s's work or hobby;
- offer a useful idea only if it genuinely fits and does not require invented expertise;
- casually suggest moving on to the theme "%s";
- no question, contact details, or concrete meetup plans. Do not make it sound like a checklist or a pitch.`, listener.Name, listener.Name, theme.Name)
	}

	b := strings.Builder{}
	b.WriteString(task)
	b.WriteString("\n\n" + transcript(speaker, listener, msgs, language))
	return []llmMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}
}

// networkingSeeds are optional turn angles for the networking phase; they
// rotate through the curiosity lists so the chat keeps discovering new
// work/business/hobby angles - when the conversation itself has not
// already provided a better thread.
var networkingSeeds = []string{
	"SUBJECT: only your craft. If their current focus has not come up, ask what the partner is focused on these days in their work, business or hobby - nothing else.",
	"SUBJECT: only your craft. If it has not come up, ask what they enjoy or care about OUTSIDE their main work, and what it looks like.",
	"SUBJECT: only your craft. If their goal has not come up, ask what they are working toward right now; add one line about your own current goal.",
	"SUBJECT: only your craft. If it fits, ask what challenge or opportunity they are navigating lately in their project.",
	"SUBJECT: only your craft. If it has not come up, ask what strength, skill or experience they are proud of; say one of yours.",
	"SUBJECT: only your craft. If it fits, ask what kind of people, ideas or opportunities are useful to them now.",
	"SUBJECT: only your craft. If it fits, tell ONE short, concrete story that makes your work or hobby memorable.",
	"SUBJECT: only your craft. If it fits, name one way you could be genuinely helpful in the partner's field.",
	"SUBJECT: only your craft. If it fits, compare the two of you: what is more rewarding - your day job or the hobby craft, and why.",
	"SUBJECT: only your craft. If it fits, tell what would make this conversation valuable for you and ask the partner the same.",
}

var networkingSeedsRU = []string{
	"Если ещё не обсуждали, спроси, чем собеседник сейчас занят или увлечён.",
	"Если разговор к этому располагает, спроси, что ему нравится делать вне основной работы.",
	"Если цель ещё не обсуждали, спроси, к чему он сейчас стремится; своей поделись только если это к месту.",
	"Если это продолжает тему, спроси, с какой трудностью или возможностью он сейчас сталкивается.",
	"Если уместно, спроси, каким своим навыком он особенно доволен; о своём расскажи только если есть что добавить.",
	"Если разговор естественно к этому пришёл, спроси, какие идеи или люди были бы ему сейчас полезны.",
	"Если к месту, расскажи короткую конкретную историю о своём деле или увлечении.",
	"Если это действительно полезно, предложи одну идею для его занятия, не изображая эксперта.",
	"Если тема уже возникла, сравни, что приятнее — основная работа или увлечение, и почему.",
	"Если это не звучит формально, скажи, что тебе интересно в разговоре; не требуй такого же ответа.",
}

// topicSeeds are concrete turn directives for themed debates; small chat
// models follow them far better than abstract "discuss further" warnings.
var topicSeeds = []string{
	"Give one concrete everyday example of the theme from your own work or hobby.",
	"If it has not come up, ask the partner for their blunt opinion on one controversial side of the theme.",
	"Tell how the theme showed up in your life this week...",
	"Play the opposite view for one turn, then find the common ground.",
	"If it has not come up, ask what the partner expects the theme to look like in ten years.",
	"Point to one everyday detail of life that the theme quietly changes.",
	"Agree with the partner's last thought and add your own small twist.",
	"If it has not come up, ask what the partner would change first about how the world handles this theme.",
	"Admit one doubt or fear about the theme and, if it fits, ask the partner to soften it.",
	"Compare the theme with something small you both met in daily life.",
}

var topicSeedsRU = []string{
	"Приведи один простой пример этой темы из повседневной жизни, только если он основан на твоём опыте.",
	"Если ещё не обсуждали, спроси мнение собеседника об одной спорной стороне темы.",
	"Расскажи, как эта тема проявилась в жизни на этой неделе, не выдумывая событий.",
	"На один ход попробуй взглянуть с другой стороны, а затем найди, в чём вы согласны.",
	"Если ещё не обсуждали, спроси, как эта тема может измениться через десять лет.",
	"Назови одну повседневную мелочь, на которую эта тема влияет.",
	"Согласись или мягко поспорь с последней мыслью собеседника и добавь своё наблюдение.",
	"Если ещё не обсуждали, спроси, что собеседник изменил бы в первую очередь.",
	"Поделись сомнением по теме, только если оно естественно связано с твоей ролью.",
	"Сравни тему с небольшим знакомым бытовым опытом, если такая связь действительно есть.",
}

// topicSeed picks a deterministic optional angle for this turn: the same
// turn of the same chat always gets the same angle, neighbouring turns
// rotate through the list. The seed is a fallback, not a command: the turn
// belongs to the conversation first. The first turn keeps its scenario
// hello prompt.
func topicSeed(ev model.Event, phase string, turnNumber, attempt int, overLimit bool, language string) string {
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
		if isRussian(language) {
			seed = topicSeedsRU[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(topicSeedsRU)]
		} else {
			seed = topicSeeds[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(topicSeeds)]
		}
	} else {
		if isRussian(language) {
			seed = networkingSeedsRU[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(networkingSeedsRU)]
		} else {
			seed = networkingSeeds[(int(fnv32a(ev.Chat))+turnNumber*(1+attempt))%len(networkingSeeds)]
		}
	}
	if isRussian(language) {
		return "Необязательная идея для этой реплики — только если она естественно продолжает разговор и ещё не обсуждалась: " + seed +
			" Сначала ответь на заданный вопрос. Если идея уводит в сторону или повторяется, проигнорируй её и продолжай текущую тему."
	}
	return "Optional angle for this turn, only if it naturally follows the conversation and has not already been covered: " + seed +
		" Answer any pending question first. If this angle would force a topic change or repetition, ignore it and continue the current thread."
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
func questionDirect(listener model.User, lastMsg, language string) string {
	if !strings.Contains(lastMsg, "?") {
		return ""
	}
	if isRussian(language) {
		return fmt.Sprintf("%s задал(а) вопрос в последнем сообщении. Сначала ответь прямо на него. Если там также есть проблема или переживание, коротко отреагируй на это. Продолжай ту же тему; не перескакивай на новую и не задавай ещё один вопрос, если для этого нет естественного повода.", listener.Name)
	}
	return fmt.Sprintf("%s asked a question in their latest message. Answer it directly first. If their message also shared a difficulty or feeling, acknowledge it briefly. Continue the same thread; don't introduce a new topic or ask another question unless it genuinely fits.", listener.Name)
}

// goodbyeReplies are pattern acknowledgements a listener may answer a
// farewell with: short, polite, generic — no conversation content. The
// worker picks one of them locally without calling the LLM. Some carry an
// emoji for warmth; plain ones stay in the list too, so a goodbye without
// emoji happens as well.
var goodbyeReplies = []string{
	"Ha, sure thing! It was really nice talking with you too - take care! 👋",
	"Thanks, you too! Have a good one and see you around 🙂",
	"Likewise! Was a pleasure chatting. Good luck out there! ☀️",
	"Take care too! Let's chat again some other day.",
	"Sounds good, thanks! Have a great day and stay well 🌿",
	"Thanks a lot, same here! Keep in touch and be safe.",
	"You too! It was a real pleasure — talk soon 👋",
	"Thanks, same to you! See you around.",
}

var goodbyeRepliesRU = []string{
	"Да, конечно! Было приятно поболтать. Давай, береги себя! 👋",
	"Спасибо, и тебе хорошего дня! Ещё спишемся 🙂",
	"Мне тоже было приятно пообщаться. Удачи тебе! ☀️",
	"Давай, до связи! Может, ещё как-нибудь поболтаем.",
	"Спасибо! Хорошего дня и береги себя 🌿",
	"И тебе спасибо за разговор. Пока!",
	"И тебе! Было приятно, давай до связи 👋",
	"Спасибо, и тебе! Удачи и до встречи.",
}

// goodbyeReply returns one greeting-like reply to a farewell.
func goodbyeReply(language string) string {
	if isRussian(language) {
		return sanitizeContent(goodbyeRepliesRU[randInt(len(goodbyeRepliesRU))])
	}
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

// splitIntoMessages keeps a short reply together as one messenger bubble.
// It drops content after the first question so each turn has at most one
// question, and caps oversized messages without cutting through a word.
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
	part := trimAndLimit([]rune(s), limit)
	if part == "" {
		return nil
	}
	return []string{part}
}

// whitespaceRe collapses newlines and tabs into spaces so every part reads
// as one chat bubble.
var whitespaceRe = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")

// trimAndLimit cleans a message (whitespace, wrapping quotes) and caps it
// at the message limit, cutting on the last word boundary when possible.
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
