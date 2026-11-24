package model

import (
	"strings"
	"unicode"
)

// DefaultLanguage is the language of rooms created without an explicit one.
const DefaultLanguage = "en"

// SupportedLanguages lists the language codes a room may use.
var SupportedLanguages = map[string]struct{}{
	"en": {},
	"ru": {},
}

// SupportedLanguage reports whether code is a supported room language.
func SupportedLanguage(code string) bool {
	_, ok := SupportedLanguages[code]
	return ok
}

// DetectLanguage reports the dominant script of a text: "ru" when Cyrillic
// letters are at least as many as Latin ones, "en" (Latin) otherwise. It is
// the gate that keeps rooms single-language: users, chats and messages of a
// room must match the room language.
//
// Proper nouns do not count toward dominance (issue #52): a capitalized
// token that is not sentence-initial is a NAME ("Foreigner", "English" in
// «Привет, я Foreigner — I speak English») — a name carries no language
// signal, and counting its letters let a Latin-heavy persona name flip the
// detection of an otherwise Russian message.
func DetectLanguage(text string) string {
	cyrillic, latin := 0, 0
	sentenceStart := true
	word := strings.Builder{}
	flush := func() {
		if word.Len() == 0 {
			return
		}
		w := []rune(word.String())
		word.Reset()
		upperInitial := unicode.IsUpper(w[0])
		proper := upperInitial && !sentenceStart
		sentenceStart = false
		if proper {
			return // a name: no language signal
		}
		for _, r := range w {
			switch {
			case unicode.Is(unicode.Cyrillic, r):
				cyrillic++
			case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
				latin++
			}
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) {
			word.WriteRune(r)
			continue
		}
		flush()
		if strings.ContainsRune(".!?", r) {
			sentenceStart = true
		}
	}
	flush()
	if cyrillic >= latin && cyrillic > 0 {
		return "ru"
	}
	return DefaultLanguage
}

// TextMatchesLanguage reports whether the text is written in the given
// room language: the text must detect as the room's language — Russian
// rooms take Cyrillic-dominant texts, English rooms Latin-dominant ones.
// Proper nouns (persona names) carry no language signal in either
// direction (issue #52): «Hi, I'm Аня» fits an English room as much as
// «Привет, я Foreigner» fits a Russian one.
func TextMatchesLanguage(text, language string) bool {
	if language == "ru" {
		return DetectLanguage(text) == "ru"
	}
	return DetectLanguage(text) != "ru"
}
