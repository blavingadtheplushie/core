package slackbot

import (
	"testing"

	"github.com/slack-go/slack"
)

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

func TestAnsweredInThread(t *testing.T) {
	var m slack.Message
	if answeredInThread(m) {
		t.Error("plain DM message is not answered")
	}
	m.ReplyCount = 1
	if !answeredInThread(m) {
		t.Error("DM message with a thread was answered there")
	}
}
