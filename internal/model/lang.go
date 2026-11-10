package model

import "unicode"

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
func DetectLanguage(text string) string {
	cyrillic, latin := 0, 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			latin++
		}
	}
	if cyrillic >= latin && cyrillic > 0 {
		return "ru"
	}
	return DefaultLanguage
}

// TextMatchesLanguage reports whether the text is written in the given
// room language: Russian rooms take Cyrillic-led texts, English rooms take
// texts without any Cyrillic.
func TextMatchesLanguage(text, language string) bool {
	if language == "ru" {
		return DetectLanguage(text) == "ru"
	}
	for _, r := range text {
		if unicode.Is(unicode.Cyrillic, r) {
			return false
		}
	}
	return true
}
