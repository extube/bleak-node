package model

import "testing"

// The detector keeps rooms single-language; proper nouns carry no language
// signal (issue #52: the "Foreigner" persona in a Russian room).
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Привет! Я Аня, гончар.", "ru"},
		{"Hello there, I am new in this city.", "en"},
		// The persona NAME (capitalized, not sentence-initial) is skipped:
		// a Latin-heavy name must not flip a Russian message.
		{"Привет, я Foreigner — рад знакомству.", "ru"},
		// …but real English words (lowercase) still dominate.
		{"Привет, я Foreigner — I speak English entirely.", "en"},
		// A Latin name at sentence start IS counted (no name signal).
		{"Foreigner здесь впервые.", "ru"},
		{"", "en"},
	}
	for _, c := range cases {
		if got := DetectLanguage(c.text); got != c.want {
			t.Fatalf("DetectLanguage(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestTextMatchesLanguage(t *testing.T) {
	if !TextMatchesLanguage("Привет, я Foreigner — рад знакомству.", "ru") {
		t.Fatal("Russian message with a foreign name rejected")
	}
	if TextMatchesLanguage("Привет, я Foreigner — I speak English entirely.", "ru") {
		t.Fatal("English sentence passed as Russian")
	}
	if TextMatchesLanguage("Привет", "en") {
		t.Fatal("Cyrillic text passed in an English room")
	}
	// Symmetric case: a Cyrillic persona name fits an English room.
	if !TextMatchesLanguage("Hi, I'm Аня — nice to meet you.", "en") {
		t.Fatal("English message with a Cyrillic name rejected")
	}
}
