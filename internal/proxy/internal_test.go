package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalURL(t *testing.T) {
	for in, want := range map[string]string{":8090": "http://127.0.0.1:8090/v1", "0.0.0.0:9000": "http://127.0.0.1:9000/v1", "127.0.0.2:1": "http://127.0.0.2:1/v1", "off": "", "": ""} {
		if got := LocalURL(in); got != want {
			t.Errorf("LocalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInternalKey(t *testing.T) {
	p := newTestProxy(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","content":[],"usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	defer up.Close()
	if err := p.SaveUpstream(Upstream{Name: "a", Style: "anthropic", BaseURL: up.URL, Priority: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !p.HasStyle("anthropic") || p.HasStyle("openai") {
		t.Fatal("HasStyle wrong")
	}
	h := p.Handler()
	call := func(key string) int {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
		req.Header.Set("x-api-key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(p.InternalKey()); c != 200 {
		t.Fatalf("internal key: %d", c)
	}
	if c := call("sk-blv-int-nope"); c != 401 {
		t.Fatalf("bad internal key: %d", c)
	}
	if p.InternalKey() != p.InternalKey() {
		t.Fatal("internal key not stable")
	}
}
