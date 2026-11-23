// Package mcp is the single MCP (Streamable HTTP) client: JSON-RPC over
// POST, SSE or JSON replies, session ids, and optional auth (static bearer
// from the vault, or OAuth with automatic refresh; tokens live in the vault).
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"blavingad/internal/httpx"
)

// Server is one configured MCP endpoint.
type Server struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	// Auth: none | bearer (TokenRef = vault ref of the token) | oauth
	// (state kept in vault item OAuthItem(), hidden field "state").
	Auth     string `json:"auth"`
	TokenRef string `json:"token_ref,omitempty"`
	Enabled  bool   `json:"enabled"`
	// Policy overrides the mcp tool policy for this server: auto | approve |
	// off, empty = same as the mcp tool.
	Policy string `json:"policy,omitempty"`
}

// OAuthItem is the vault item holding a server's OAuth state.
func (s Server) OAuthItem() string {
	return "MCP_OAUTH_" + strings.ToUpper(strings.ReplaceAll(s.Name, "-", "_"))
}

// OAuthState is what we persist for OAuth servers.
type OAuthState struct {
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	TokenEndpoint string `json:"token_endpoint"`
	AuthEndpoint  string `json:"authorization_endpoint,omitempty"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
	Scope         string `json:"scope,omitempty"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresAt     int64  `json:"expires_at"`
	// pending PKCE flow
	Verifier string `json:"verifier,omitempty"`
	State    string `json:"state,omitempty"`
}

// Store abstracts secret persistence (the vault).
type Store interface {
	Resolve(ctx context.Context, ref string) (string, error)
	SaveOAuth(ctx context.Context, item string, st OAuthState) error
}

type Client struct {
	Store  Store
	ImgDir string
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

func (c *Client) lock(name string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	if c.locks[name] == nil {
		c.locks[name] = &sync.Mutex{}
	}
	return c.locks[name]
}

func (c *Client) loadOAuth(ctx context.Context, s Server) (OAuthState, error) {
	var st OAuthState
	raw, err := c.Store.Resolve(ctx, "vault://"+s.OAuthItem()+"/state")
	if err != nil {
		return st, fmt.Errorf("%s is not connected (no OAuth state in the vault: %v); run auth_start", s.Name, err)
	}
	err = json.Unmarshal([]byte(raw), &st)
	return st, err
}

// Refresh exchanges the refresh token (also used on 401).
func (c *Client) refresh(ctx context.Context, s Server, st *OAuthState) error {
	if st.RefreshToken == "" || st.TokenEndpoint == "" {
		return errors.New("token expired and no refresh token: run auth_start again")
	}
	f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {st.RefreshToken}, "client_id": {st.ClientID}, "resource": {s.URL}}
	if st.ClientSecret != "" {
		f.Set("client_secret", st.ClientSecret)
	}
	if err := c.tokenRequest(ctx, st, f); err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}
	return c.Store.SaveOAuth(ctx, s.OAuthItem(), *st)
}

func (c *Client) tokenRequest(ctx context.Context, st *OAuthState, f url.Values) error {
	req, _ := http.NewRequestWithContext(ctx, "POST", st.TokenEndpoint, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpx.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, b)
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &t); err != nil || t.AccessToken == "" {
		return fmt.Errorf("bad token response: %.200s", b)
	}
	st.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		st.RefreshToken = t.RefreshToken
	}
	st.ExpiresAt = 0
	if t.ExpiresIn > 0 {
		st.ExpiresAt = time.Now().Unix() + t.ExpiresIn
	}
	st.Verifier, st.State = "", ""
	return nil
}

func (c *Client) token(ctx context.Context, s Server, force bool) (string, error) {
	switch s.Auth {
	case "", "none":
		return "", nil
	case "bearer":
		return c.Store.Resolve(ctx, s.TokenRef)
	case "oauth":
		l := c.lock(s.Name)
		l.Lock()
		defer l.Unlock()
		st, err := c.loadOAuth(ctx, s)
		if err != nil {
			return "", err
		}
		if force || (st.ExpiresAt > 0 && time.Now().Unix() > st.ExpiresAt-120) {
			if err := c.refresh(ctx, s, &st); err != nil {
				return "", err
			}
		}
		return st.AccessToken, nil
	}
	return "", fmt.Errorf("unknown auth %q", s.Auth)
}

type session struct {
	c   *Client
	s   Server
	tok string
	sid string
	n   int
}

func (ss *session) rpc(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		body["params"] = params
	}
	id := 0
	if !notify {
		ss.n++
		id = ss.n
		body["id"] = id
	}
	for attempt := 0; ; attempt++ {
		buf, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", ss.s.URL, bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("User-Agent", "blavingad-mcp/2")
		if ss.tok != "" {
			req.Header.Set("Authorization", "Bearer "+ss.tok)
		}
		if ss.sid != "" {
			req.Header.Set("Mcp-Session-Id", ss.sid)
		}
		resp, err := httpx.Client.Do(req)
		if err != nil {
			return nil, err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 && ss.s.Auth == "oauth" {
			if ss.tok, err = ss.c.token(ctx, ss.s, true); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("MCP HTTP %d: %.500s", resp.StatusCode, b)
		}
		if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
			ss.sid = v
		}
		if notify {
			return nil, nil
		}
		return pickMessage(b, id)
	}
}

// pickMessage finds the JSON-RPC reply with this id in a JSON or SSE body.
func pickMessage(b []byte, id int) (json.RawMessage, error) {
	type msg struct {
		ID     any             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	try := func(raw string) (json.RawMessage, bool, error) {
		var m msg
		if json.Unmarshal([]byte(raw), &m) != nil {
			return nil, false, nil
		}
		if fmt.Sprint(m.ID) != fmt.Sprint(id) && m.ID != nil {
			return nil, false, nil
		}
		if len(m.Error) > 0 && string(m.Error) != "null" {
			return nil, true, fmt.Errorf("MCP error: %s", m.Error)
		}
		return m.Result, true, nil
	}
	t := strings.TrimSpace(string(b))
	if strings.HasPrefix(t, "{") {
		if r, ok, err := try(t); ok {
			return r, err
		}
	}
	for _, ev := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n\n") {
		var data []string
		for _, line := range strings.Split(ev, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(line[5:]))
			}
		}
		if r, ok, err := try(strings.Join(data, "\n")); ok {
			return r, err
		}
	}
	return nil, fmt.Errorf("unparseable MCP response: %.400s", t)
}

func (c *Client) open(ctx context.Context, s Server) (*session, error) {
	tok, err := c.token(ctx, s, false)
	if err != nil {
		return nil, err
	}
	ss := &session{c: c, s: s, tok: tok}
	if _, err := ss.rpc(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "blavingad", "version": "2"}}, false); err != nil {
		return nil, err
	}
	ss.rpc(ctx, "notifications/initialized", nil, true)
	return ss, nil
}

// Tool is a remote tool description.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *Client) List(ctx context.Context, s Server) ([]Tool, error) {
	ss, err := c.open(ctx, s)
	if err != nil {
		return nil, err
	}
	r, err := ss.rpc(ctx, "tools/list", map[string]any{}, false)
	if err != nil {
		return nil, err
	}
	var out struct{ Tools []Tool }
	err = json.Unmarshal(r, &out)
	return out.Tools, err
}

// Call runs a remote tool; images are saved to ImgDir and referenced.
func (c *Client) Call(ctx context.Context, s Server, tool string, args map[string]any) (string, bool, error) {
	ss, err := c.open(ctx, s)
	if err != nil {
		return "", false, err
	}
	if args == nil {
		args = map[string]any{}
	}
	r, err := ss.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, false)
	if err != nil {
		return "", false, err
	}
	var res struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
	}
	json.Unmarshal(r, &res)
	var parts []string
	for i, ct := range res.Content {
		switch ct["type"] {
		case "text":
			parts = append(parts, fmt.Sprint(ct["text"]))
		case "image":
			data, _ := base64.StdEncoding.DecodeString(fmt.Sprint(ct["data"]))
			ext := strings.TrimPrefix(fmt.Sprint(ct["mimeType"]), "image/")
			if ext == "" || ext == "<nil>" {
				ext = "png"
			}
			dir := filepath.Join(c.ImgDir, s.Name)
			os.MkdirAll(dir, 0o755)
			p := filepath.Join(dir, fmt.Sprintf("mcp_%d_%d.%s", time.Now().UnixMilli(), i, ext))
			os.WriteFile(p, data, 0o644)
			parts = append(parts, "[image saved: "+p+"]")
		default:
			b, _ := json.Marshal(ct)
			parts = append(parts, string(b))
		}
	}
	if len(res.Content) == 0 {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, "\n"), res.IsError, nil
}
