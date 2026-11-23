package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memStore struct{ m map[string]string }

func (s *memStore) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := s.m[ref]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (s *memStore) SaveOAuth(_ context.Context, item string, st OAuthState) error {
	b, _ := json.Marshal(st)
	s.m["vault://"+item+"/state"] = string(b)
	return nil
}

// fake server: SSE replies, requires a bearer, returns 401 for the stale one.
func fake(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "R1" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"access_token":"fresh","refresh_token":"R2","expires_in":3600}`))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		w.Header().Set("Mcp-Session-Id", "s1")
		if req["id"] == nil {
			w.WriteHeader(202)
			return
		}
		var res string
		switch req["method"] {
		case "initialize":
			res = `{}`
		case "tools/list":
			res = `{"tools":[{"name":"echo","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"}}}}]}`
		case "tools/call":
			res = `{"content":[{"type":"text","text":"hi"}]}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		id, _ := json.Marshal(req["id"])
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + string(id) + ",\"result\":" + res + "}\n\n"))
	})
	return httptest.NewServer(mux)
}

func TestOAuthRefreshOn401AndCall(t *testing.T) {
	srv := fake(t)
	defer srv.Close()
	s := Server{Name: "x", URL: srv.URL + "/mcp", Auth: "oauth", Enabled: true}
	st := &memStore{m: map[string]string{}}
	st.SaveOAuth(context.Background(), s.OAuthItem(), OAuthState{TokenEndpoint: srv.URL + "/token", ClientID: "c", AccessToken: "stale", RefreshToken: "R1", ExpiresAt: time.Now().Unix() + 9999})
	c := &Client{Store: st, ImgDir: t.TempDir()}
	tools, err := c.List(context.Background(), s)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("list: %v %v", tools, err)
	}
	out, isErr, err := c.Call(context.Background(), s, "echo", nil)
	if err != nil || isErr || out != "hi" {
		t.Fatalf("call: %q %v %v", out, isErr, err)
	}
	if !strings.Contains(st.m["vault://MCP_OAUTH_X/state"], `"refresh_token":"R2"`) {
		t.Fatal("rotated refresh token not persisted")
	}
}

func TestPickMessagePlainJSON(t *testing.T) {
	r, err := pickMessage([]byte(`{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`), 3)
	if err != nil || string(r) != `{"ok":true}` {
		t.Fatal(string(r), err)
	}
	if _, err := pickMessage([]byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-1}}`), 3); err == nil {
		t.Fatal("expected error")
	}
}
