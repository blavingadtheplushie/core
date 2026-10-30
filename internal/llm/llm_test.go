package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "key")
	c.Headers = map[string]string{"X-Snowflake-Authorization-Token-Type": "PROGRAMMATIC_ACCESS_TOKEN"}
	if _, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Accept": "application/json", "Content-Type": "application/json", "Authorization": "Bearer key",
		"X-Snowflake-Authorization-Token-Type": "PROGRAMMATIC_ACCESS_TOKEN",
	} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, got.Get(k), want)
		}
	}
}

func TestAlternativeContentShapes(t *testing.T) {
	cases := map[string]string{
		`{"choices":[{"message":{"role":"assistant","content":null,"content_list":[{"type":"text","text":"from content_list"}]}}]}`:          "from content_list",
		`{"choices":[{"message":{"role":"assistant","content":[{"type":"reasoning","text":"hmm"},{"type":"output_text","text":"parts"}]}}]}`: "parts",
		`{"choices":[{"text":"legacy text","message":{"role":"assistant"}}]}`:                                                                "legacy text",
		`{"choices":[{"message":{"role":"assistant","content":"","refusal":"I can't help with that"}}]}`:                                     "🙅 I can't help with that",
	}
	for body, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		res, err := New(srv.URL, "").Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
		srv.Close()
		if err != nil || res.Message.Content != want {
			t.Errorf("%s → %q, %v (want %q)", body[:40], res.Message.Content, err, want)
		}
		if res != nil && res.Raw == "" {
			t.Error("raw body must be kept for diagnosis")
		}
	}
}
