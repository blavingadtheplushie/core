package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseUserBlocks(t *testing.T) {
	in := `[{"type":"header","text":{"type":"plain_text","text":"hi"}},
	 {"type":"actions","elements":[{"type":"button","action_id":"approve","value":"ap_1","text":{"type":"plain_text","text":"x"}}]}]`
	blocks, err := parseUserBlocks(in)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("got %v %v", blocks, err)
	}
	out, _ := json.Marshal(blocks)
	if strings.Contains(string(out), `"action_id":"approve"`) || !strings.Contains(string(out), `"action_id":"blocks:approve"`) {
		t.Fatalf("action_id not namespaced: %s", out)
	}
	if _, err := parseUserBlocks(`{"blocks":[{"type":"divider"}]}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{``, `[]`, `[{"text":"no type"}]`, `nope`} {
		if _, err := parseUserBlocks(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
	many := "[" + strings.TrimSuffix(strings.Repeat(`{"type":"divider"},`, 51), ",") + "]"
	if _, err := parseUserBlocks(many); err == nil {
		t.Fatal("expected 50-block limit")
	}
}
