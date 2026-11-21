package slackbot

import "testing"

func TestBotHistoryText(t *testing.T) {
	cases := []struct{ text, out, want string }{
		{"Working on it…", "", ""},
		{"🤖 Working on it…", "", ""},
		{"Working on it… (step 3)", "", ""},
		{"Working on it… la réponse", "", "la réponse"},
		{"Working on it… tronqué", "la réponse complète", "la réponse complète"},
		{"Working on it…", "la réponse complète", "la réponse complète"},
		{"réponse simple", "", "réponse simple"},
	}
	for _, c := range cases {
		if got := botHistoryText(c.text, c.out); got != c.want {
			t.Errorf("botHistoryText(%q, %q) = %q, want %q", c.text, c.out, got, c.want)
		}
	}
}
