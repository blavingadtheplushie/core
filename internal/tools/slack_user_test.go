package tools

import (
	"context"
	"testing"

	"blavingad/internal/core"
)

func TestSlackUserToolsGuarded(t *testing.T) {
	d := vaultDeps(t)
	registerSlackUser(d)
	for _, n := range []string{"slack_search_as_paul", "slack_read_as_paul"} {
		if _, ok := d.Reg.Get(n); !ok {
			t.Fatalf("%s not registered", n)
		}
	}
	for _, src := range []string{"email", "self"} {
		if _, err := d.slackAsUser(context.Background(), &core.RunContext{Source: src, UserID: "U1"}); err == nil {
			t.Errorf("source %s must be refused", src)
		}
	}
	rc := &core.RunContext{Source: "slack", UserID: "U1", Allowed: map[string]bool{"slack_search_as_paul": true}}
	if _, err := d.slackAsUser(context.Background(), rc); err == nil {
		t.Error("profile without vault access must be refused")
	}
}
