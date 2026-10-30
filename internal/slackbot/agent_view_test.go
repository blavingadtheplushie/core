package slackbot

import "testing"

func TestThreadTitle(t *testing.T) {
	if got := threadTitle("  hello\n  octopus  "); got != "hello octopus" {
		t.Fatalf("got %q", got)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	if r := []rune(threadTitle(long)); len(r) != 61 {
		t.Fatalf("not clipped: %d", len(r))
	}
}
