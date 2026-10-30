package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"blavingad/internal/store"
)

const (
	sessionCookie = "blv_session"
	stateCookie   = "blv_oidc_state"
	sessionTTL    = 7 * 24 * time.Hour
)

type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type oidcClient struct {
	mu       sync.Mutex
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func (s *Server) oidc(ctx context.Context) (*oidcClient, error) {
	s.oc.mu.Lock()
	defer s.oc.mu.Unlock()
	if s.oc.provider != nil {
		return &s.oc, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, strings.TrimRight(s.cfg.OIDCIssuer, "/"))
	if err != nil {
		return nil, err
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if len(s.cfg.OIDCAllowedGroups) > 0 {
		scopes = append(scopes, "groups")
	}
	s.oc.provider = p
	s.oc.oauth = &oauth2.Config{ClientID: s.cfg.OIDCClientID, ClientSecret: s.cfg.OIDCClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: s.cfg.PublicURL + "/api/auth/callback", Scopes: scopes}
	s.oc.verifier = p.Verifier(&oidc.Config{ClientID: s.cfg.OIDCClientID})
	return &s.oc, nil
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) secure() bool { return strings.HasPrefix(s.cfg.PublicURL, "https://") }

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secure(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge.Seconds())})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) sessionUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	var u User
	var expires string
	err = s.st.DB.QueryRow(`SELECT u.id, COALESCE(u.email,''), COALESCE(u.name,''), COALESCE(u.avatar_url,''), s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, hashToken(c.Value)).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &expires)
	if err != nil {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
		s.st.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value))
		return nil
	}
	return &u
}

func (s *Server) createSession(w http.ResponseWriter, userID string) {
	token := randomToken(32)
	s.st.DB.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at) VALUES(?,?,?,?)`,
		hashToken(token), userID, time.Now().Add(sessionTTL).UTC().Format(time.RFC3339), store.Now())
	s.st.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	s.setCookie(w, sessionCookie, token, sessionTTL)
}

func (s *Server) upsertUser(sub, email, name, avatar string) (string, error) {
	var id string
	if s.st.DB.QueryRow(`SELECT id FROM users WHERE sub = ?`, sub).Scan(&id) == nil {
		_, err := s.st.DB.Exec(`UPDATE users SET email=?, name=?, avatar_url=?, last_login_at=? WHERE id=?`, email, name, avatar, store.Now(), id)
		return id, err
	}
	id = store.NewID("usr")
	_, err := s.st.DB.Exec(`INSERT INTO users(id, sub, email, name, avatar_url, created_at, last_login_at) VALUES(?,?,?,?,?,?,?)`, id, sub, email, name, avatar, store.Now(), store.Now())
	return id, err
}

// ---------- handlers ----------

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	writeJSON(w, 200, map[string]any{
		"authenticated":   u != nil,
		"user":            u,
		"oidcEnabled":     s.cfg.OIDCConfigured(),
		"passwordEnabled": s.cfg.AdminPassword != "",
		"agentName":       s.cfg.AgentName,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDCConfigured() {
		http.Redirect(w, r, "/?auth_error="+url.QueryEscape("OIDC is not configured"), http.StatusFound)
		return
	}
	oc, err := s.oidc(r.Context())
	if err != nil {
		slog.Warn("oidc discovery", "err", err)
		http.Redirect(w, r, "/?auth_error="+url.QueryEscape("OIDC provider unreachable"), http.StatusFound)
		return
	}
	state, nonce, verifier := randomToken(24), randomToken(24), oauth2.GenerateVerifier()
	s.st.DB.Exec(`DELETE FROM oidc_states WHERE created_at < ?`, time.Now().Add(-15*time.Minute).UTC().Format(time.RFC3339Nano))
	s.st.DB.Exec(`INSERT INTO oidc_states(state, verifier, nonce, created_at) VALUES(?,?,?,?)`, state, verifier, nonce, store.Now())
	s.setCookie(w, stateCookie, state, 10*time.Minute)
	http.Redirect(w, r, oc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string, err error) {
		if err != nil {
			slog.Warn("oidc callback", "msg", msg, "err", err)
		}
		s.clearCookie(w, stateCookie)
		http.Redirect(w, r, "/?auth_error="+url.QueryEscape(msg), http.StatusFound)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("Sign-in denied: "+e, nil)
		return
	}
	state := r.URL.Query().Get("state")
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		fail("Invalid sign-in session, please try again.", err)
		return
	}
	var verifier, nonce, created string
	if err := s.st.DB.QueryRow(`SELECT verifier, nonce, created_at FROM oidc_states WHERE state = ?`, state).Scan(&verifier, &nonce, &created); err != nil {
		fail("Sign-in session expired.", err)
		return
	}
	s.st.DB.Exec(`DELETE FROM oidc_states WHERE state = ?`, state)
	if t, err := time.Parse(time.RFC3339Nano, created); err != nil || time.Since(t) > 10*time.Minute {
		fail("Sign-in session expired.", nil)
		return
	}
	oc, err := s.oidc(r.Context())
	if err != nil {
		fail("OIDC provider unreachable", err)
		return
	}
	tok, err := oc.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail("Could not exchange the code.", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := oc.verifier.Verify(r.Context(), raw)
	if err != nil {
		fail("Invalid ID token.", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		fail("Invalid nonce.", nil)
		return
	}
	var claims struct {
		Sub               string   `json:"sub"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		PreferredUsername string   `json:"preferred_username"`
		Picture           string   `json:"picture"`
		Groups            []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		fail("Unreadable profile.", err)
		return
	}
	if err := s.authorize(claims.Email, claims.Groups); err != nil {
		fail(err.Error(), nil)
		return
	}
	name := firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email)
	id, err := s.upsertUser(claims.Sub, claims.Email, name, claims.Picture)
	if err != nil {
		fail("Could not save the user.", err)
		return
	}
	s.clearCookie(w, stateCookie)
	s.createSession(w, id)
	s.st.Audit(name, "auth.login", claims.Email, map[string]string{"method": "oidc"})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) authorize(email string, groups []string) error {
	if len(s.cfg.OIDCAllowedEmails) == 0 && len(s.cfg.OIDCAllowedGroups) == 0 {
		return nil
	}
	if slices.Contains(s.cfg.OIDCAllowedEmails, strings.ToLower(email)) {
		return nil
	}
	for _, g := range groups {
		if slices.Contains(s.cfg.OIDCAllowedGroups, g) {
			return nil
		}
	}
	return errors.New("Your account is not allowed on this dashboard.")
}

var (
	loginMu       sync.Mutex
	loginFailures = map[string][]time.Time{}
)

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminPassword == "" {
		writeErr(w, 404, "password sign-in is disabled")
		return
	}
	ip := clientIP(r)
	loginMu.Lock()
	recent := loginFailures[ip][:0]
	for _, t := range loginFailures[ip] {
		if time.Since(t) < 15*time.Minute {
			recent = append(recent, t)
		}
	}
	loginFailures[ip] = recent
	blocked := len(recent) >= 8
	loginMu.Unlock()
	if blocked {
		writeErr(w, 429, "too many attempts, try again in a few minutes")
		return
	}
	var body struct{ Password string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.AdminPassword)) != 1 {
		loginMu.Lock()
		loginFailures[ip] = append(loginFailures[ip], time.Now())
		loginMu.Unlock()
		writeErr(w, 401, "wrong password")
		return
	}
	id, _ := s.upsertUser("local:admin", "", "Admin", "")
	s.createSession(w, id)
	s.st.Audit("Admin", "auth.login", "", map[string]string{"method": "password"})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.st.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value))
	}
	s.clearCookie(w, sessionCookie)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}
