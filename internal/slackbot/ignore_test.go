package slackbot

import "testing"

func TestIsIgnoredText(t *testing.T) {
	cases := map[string]bool{
		"## not for bots":            true,
		"  ##<@U123> hi":             true,
		"##":                         true,
		"# one hash":                 false,
		"hello ## later":             false,
		"<@U123> ## mention first":   false,
		"":                           false,
	}
	for in, want := range cases {
		if got := isIgnoredText(in); got != want {
			t.Errorf("isIgnoredText(%q) = %v, want %v", in, got, want)
		}
	}
}
