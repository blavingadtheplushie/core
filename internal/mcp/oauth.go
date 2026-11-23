package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"blavingad/internal/httpx"
)

// DefaultRedirect is a loopback URL: the browser fails to load it, and the
// human pastes the final URL back (auth_finish). Only used as a fallback for
// servers without dynamic registration whose client was registered with it.
const DefaultRedirect = "http://127.0.0.1:33418/callback"

// CallbackPath is the dashboard route that receives OAuth redirects and
// finishes the flow by itself (PUBLIC_URL + CallbackPath).
const CallbackPath = "/api/mcp/oauth/callback"

// ServerFromState extracts the server name encoded in an OAuth state.
func ServerFromState(state string) string {
	if i := strings.Index(state, "~"); i > 0 {
		return state[:i]
	}
	return ""
}

func getJSON(ctx context.Context, u string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpx.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// discover finds the authorization server metadata (RFC 9728 then 8414).
func discover(ctx context.Context, server string) (map[string]any, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	issuers := []string{}
	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	for _, p := range []string{"/.well-known/oauth-protected-resource" + u.Path, "/.well-known/oauth-protected-resource"} {
		if getJSON(ctx, origin+p, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			issuers = append(issuers, prm.AuthorizationServers...)
			break
		}
	}
	issuers = append(issuers, origin)
	for _, iss := range issuers {
		iu, err := url.Parse(strings.TrimRight(iss, "/"))
		if err != nil {
			continue
		}
		base := iu.Scheme + "://" + iu.Host
		for _, p := range []string{"/.well-known/oauth-authorization-server" + iu.Path, "/.well-known/openid-configuration" + iu.Path, iu.Path + "/.well-known/openid-configuration"} {
			var m map[string]any
			if getJSON(ctx, base+p, &m) == nil && m["token_endpoint"] != nil {
				if len(prm.ScopesSupported) > 0 {
					m["scopes_supported"] = prm.ScopesSupported
				}
				return m, nil
			}
		}
	}
	return nil, errors.New("no OAuth metadata found for " + server)
}

// defaultScope is every scope the server advertises (MCP spec guidance
// when the client has no better idea).
func defaultScope(m map[string]any) string {
	var out []string
	switch v := m["scopes_supported"].(type) {
	case []string:
		out = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, " ")
}

func randB64(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthStart registers a client if needed (dynamic registration) and
// returns the URL the human must open, plus the redirect URI actually used
// (the dashboard callback, or the loopback fallback for a pre-registered
// client of a server without dynamic registration). clientID/secret may be given.
func (c *Client) AuthStart(ctx context.Context, s Server, clientID, clientSecret, scope, redirect string) (string, string, error) {
	if redirect == "" {
		redirect = DefaultRedirect
	}
	m, err := discover(ctx, s.URL)
	if err != nil {
		return "", "", err
	}
	if scope == "" {
		scope = s.Scope
	}
	if scope == "" {
		// No scope = many servers (Attend…) grant a token that sees no tool.
		scope = defaultScope(m)
	}
	st, _ := c.loadOAuth(ctx, s)
	if clientID != "" {
		st.ClientID, st.ClientSecret = clientID, clientSecret
	}
	reg, _ := m["registration_endpoint"].(string)
	if reg == "" && st.ClientID != "" && st.RedirectURI != "" && st.RedirectURI != redirect {
		// Can't register the new redirect: keep the one this client knows.
		redirect = st.RedirectURI
	}
	if st.ClientID == "" || st.RedirectURI != redirect {
		if reg == "" {
			return "", "", errors.New("this server has no dynamic registration: pass client_id (and client_secret)")
		}
		body, _ := json.Marshal(map[string]any{"client_name": "blavingad", "redirect_uris": []string{redirect},
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
			"token_endpoint_auth_method": "none"})
		req, _ := http.NewRequestWithContext(ctx, "POST", reg, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpx.Client.Do(req)
		if err != nil {
			return "", "", err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var r struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		}
		if resp.StatusCode >= 300 || json.Unmarshal(b, &r) != nil || r.ClientID == "" {
			return "", "", fmt.Errorf("client registration refused: HTTP %d %.300s", resp.StatusCode, b)
		}
		st.ClientID, st.ClientSecret = r.ClientID, r.ClientSecret
	}
	st.TokenEndpoint, _ = m["token_endpoint"].(string)
	st.AuthEndpoint, _ = m["authorization_endpoint"].(string)
	st.RedirectURI, st.Scope = redirect, scope
	st.Verifier, st.State = randB64(48), s.Name+"~"+randB64(24)
	sum := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {st.ClientID}, "redirect_uri": {redirect}, "state": {st.State},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {s.URL}}
	if scope != "" {
		q.Set("scope", scope)
	}
	if err := c.Store.SaveOAuth(ctx, s.OAuthItem(), st); err != nil {
		return "", "", err
	}
	return st.AuthEndpoint + "?" + q.Encode(), redirect, nil
}

// AuthFinish exchanges the code found in the redirect URL (pasted, or
// received by the dashboard callback).
func (c *Client) AuthFinish(ctx context.Context, s Server, redirected string) error {
	st, err := c.loadOAuth(ctx, s)
	if err != nil || st.Verifier == "" {
		return errors.New("run auth_start first")
	}
	u, err := url.Parse(strings.TrimSpace(redirected))
	if err != nil {
		return err
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		return errors.New("authorization refused: " + e)
	}
	if q.Get("state") != st.State {
		return errors.New("state mismatch: run auth_start again")
	}
	f := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {st.RedirectURI},
		"client_id": {st.ClientID}, "code_verifier": {st.Verifier}, "resource": {s.URL}}
	if st.ClientSecret != "" {
		f.Set("client_secret", st.ClientSecret)
	}
	if err := c.tokenRequest(ctx, &st, f); err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	return c.Store.SaveOAuth(ctx, s.OAuthItem(), st)
}

// Status says whether a server is usable (without calling it).
func (c *Client) Status(ctx context.Context, s Server) string {
	switch s.Auth {
	case "oauth":
		st, err := c.loadOAuth(ctx, s)
		if err != nil || st.AccessToken == "" {
			return "not connected"
		}
		return "connected"
	case "bearer":
		if s.TokenRef == "" {
			return "missing token_ref"
		}
		return "bearer " + s.TokenRef
	}
	return "public"
}
