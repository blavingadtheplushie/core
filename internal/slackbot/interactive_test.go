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

func TestNoticeBlocks(t *testing.T) {
	n := core.Notice{Title: "Self-update", Task: "Update applied and stable · abc123", Detail: "feat: x", Body: "hello **world**"}
	_, plans := encode(t, noticeBlocks(n, true))
	if len(plans) != 1 || plans[0].Title != "Self-update" || plans[0].Tasks[0].Status != "complete" {
		t.Fatalf("bad plan: %+v", plans)
	}
	legacy := noticeBlocks(n, false)
	if len(legacy) != 2 {
		t.Fatalf("legacy: %d blocks", len(legacy))
	}
	if got := noticeText(n); got != "Self-update · Update applied and stable · abc123" {
		t.Fatalf("text %q", got)
	}
	r := resolvedNotice(&core.Approval{Status: "expired", Tool: "shell"})
	if r.Status != "error" || r.Task != "Expired · shell" {
		t.Fatalf("resolved %+v", r)
	}
}
