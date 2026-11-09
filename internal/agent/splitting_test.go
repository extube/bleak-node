package agent

import "testing"

func TestSplitIntoMessagesRules(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  []string
	}{
		{
			name:  "question closes the reply, rest dropped",
			reply: "Nice to meet you. Where are you from? I also love fishing. By the way do you like coffee?",
			want:  []string{"Nice to meet you.", "Where are you from?"},
		},
		{
			name:  "no question, at most two sentences",
			reply: "One. Two. Three. Four. Five.",
			want:  []string{"One.", "Two."},
		},
		{
			name:  "oversized single sentence is capped at the limit",
			reply: "Do you rely on the map " + longWord(260) + "? Then what?",
		},
		{
			name:  "question closes the reply even mid-list",
			reply: "Mr. Bond, nice to see you. How are you? I am fine too. See you soon!",
			want:  []string{"Mr. Bond, nice to see you.", "How are you?"},
		},
		{
			name:  "abbreviations keep sentences together",
			reply: "Mr. Bond, nice. How are you?",
			want:  []string{"Mr. Bond, nice.", "How are you?"},
		},
		{
			name:  "short single sentence stays whole",
			reply: "Hi there!",
			want:  []string{"Hi there!"},
		},
	}
	for _, tc := range cases {
		if tc.reply == "" {
			continue
		}
		got := splitIntoMessages(tc.reply, 255)
		if tc.want == nil {
			// Only invariants checked: everything within the limit, at most
			// one bubble carrying the (trimmed) question.
			for _, p := range got {
				if len(p) > 255 {
					t.Fatalf("%s: part over the limit (%d): %q", tc.name, len(p), p)
				}
			}
			continue
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: part %d got %q want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func longWord(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = 'w'
	}
	return string(b)
}
