package agent

import "testing"

func TestRouteDecision(t *testing.T) {
	c := DefaultRouterConfig()
	c.Enabled = true
	cases := []struct {
		req  Request
		fast bool
	}{
		{Request{Source: "slack", Text: "tu marches tjrs ?"}, true},
		{Request{Source: "slack", Text: "can you debug this crash"}, false},
		{Request{Source: "slack", Text: "what's the prefix for X"}, true}, // "fix" inside a word
		{Request{Source: "slack", Text: "salut !opus"}, false},
		{Request{Source: "slack", Text: "fix the bug !fast"}, true},
		{Request{Source: "self", Text: "review"}, false},
		{Request{Source: "slack", Text: "hi", Depth: 1}, false},
		{Request{Source: "slack", Text: string(make([]byte, 900))}, false},
	}
	for i, tc := range cases {
		if got, why := routeDecision(c, tc.req); got != tc.fast {
			t.Errorf("case %d (%q): fast=%v want %v (%s)", i, tc.req.Text[:min(len(tc.req.Text), 30)], got, tc.fast, why)
		}
	}
}
