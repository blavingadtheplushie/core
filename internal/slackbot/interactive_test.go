package slackbot

import (
	"strings"
	"testing"

	"blavingad/internal/core"
)

func TestResolvedLineIsSober(t *testing.T) {
	got := resolvedLine(&core.Approval{Status: "expired", Tool: "shell"})
	if got != "Expired · `shell`" {
		t.Fatalf("got %q", got)
	}
	got = resolvedLine(&core.Approval{Status: "approved", Tool: "shell", DecidedBy: "Paul"})
	if got != "Approved · `shell` · Paul" {
		t.Fatalf("got %q", got)
	}
}

func TestDecidedText(t *testing.T) {
	cases := map[string]string{"approve": "Approved · `shell`", "deny": "Denied · `shell`"}
	for action, want := range cases {
		if got := decidedText(action, "shell"); got != want {
			t.Errorf("%s: got %q want %q", action, got, want)
		}
	}
	if got := decidedText("always", "shell"); !strings.HasPrefix(got, "Approved (always") {
		t.Errorf("always: got %q", got)
	}
	if got := decidedText("approve", ""); got != "Approved" {
		t.Errorf("no tool: got %q", got)
	}
}
