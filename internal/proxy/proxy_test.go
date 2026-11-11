package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blavingad/internal/store"
)

func newTestProxy(t *testing.T) *Proxy {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, func(ctx context.Context, ref string) (string, error) { return "vault-secret", nil }, "cfg-secret")
}

func TestRejectsRawKey(t *testing.T) {
	p := newTestProxy(t)
	if err := p.SaveUpstream(Upstream{Name: "x", Style: "openai", BaseURL: "https://a/v1", KeyRef: "sk-raw"}); err == nil {
		t.Fatal("raw key accepted")
	}
}

func TestFallbackStreamAndAuth(t *testing.T) {
	p := newTestProxy(t)
	var badHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer bad.Close()
	var gotModel, gotAuth string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotModel, gotAuth = string(b), r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
	}))
	defer good.Close()

	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.SaveUpstream(Upstream{Name: "bad", Style: "anthropic", BaseURL: bad.URL, KeyRef: "config", Priority: 1, Enabled: true}))
	must(p.SaveUpstream(Upstream{Name: "good", Style: "anthropic", BaseURL: good.URL, KeyRef: "vault://X/password", Priority: 2, Enabled: true}))
	must(p.SaveUpstream(Upstream{Name: "oai", Style: "openai", BaseURL: good.URL, Priority: 0, Enabled: true}))
	must(p.SetMapping(Mapping{Alias: "opus", Upstream: "bad", Model: "claude-a"}))
	must(p.SetMapping(Mapping{Alias: "opus", Upstream: "good", Model: "claude-b"}))
	_, key, err := p.CreateKey("test", "U1", nil)
	must(err)

	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	post := func(k string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(`{"model":"opus","stream":true,"messages":[]}`))
		req.Header.Set("x-api-key", k)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		return resp
	}
	if r := post("sk-blv-nope"); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	r := post(key)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(body), "message_delta") {
		t.Fatalf("got %d %s", r.StatusCode, body)
	}
	if badHits != 1 || !strings.Contains(gotModel, `"claude-b"`) || gotAuth != "vault-secret" {
		t.Fatalf("hits=%d model=%s auth=%s", badHits, gotModel, gotAuth)
	}
	if !p.isDown("bad") {
		t.Fatal("bad upstream not cooling down")
	}
	k := p.Keys()[0]
	if k.Requests != 1 || k.InputTokens != 12 || k.OutputTokens != 7 {
		t.Fatalf("usage %+v", k)
	}
	// Cooling-down upstream is tried last → good first now.
	post(key)
	if badHits != 1 {
		t.Fatalf("cooled-down upstream tried first (hits=%d)", badHits)
	}
	must(p.RevokeKey("test", false))
	if r := post(key); r.StatusCode != 401 {
		t.Fatalf("revoked key works: %d", r.StatusCode)
	}
}

func TestBadRequestNotRetried(t *testing.T) {
	p := newTestProxy(t)
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"messages: required"}}`)
	}))
	defer up.Close()
	p.SaveUpstream(Upstream{Name: "a", Style: "openai", BaseURL: up.URL, Priority: 1, Enabled: true})
	p.SaveUpstream(Upstream{Name: "b", Style: "openai", BaseURL: up.URL, Priority: 2, Enabled: true})
	_, key, _ := p.CreateKey("k", "", nil)
	req, _ := http.NewRequest("POST", httptest.NewServer(p.Handler()).URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 || hits != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, hits)
	}
}
