package web

import (
	"context"
	"net/http"
	"net/url"

	"blavingad/internal/mcp"
)

// MCPHooks lets the dashboard manage MCP servers (implemented by tools.Deps).
type MCPHooks interface {
	MCPServers() []mcp.Server
	SaveMCPServer(mcp.Server) error
	DeleteMCPServer(name string) error
	MCPClient() *mcp.Client
}

func (s *Server) mcpOK(w http.ResponseWriter) (MCPHooks, bool) {
	if s.hooks.MCP == nil {
		writeErr(w, 503, "mcp not available")
		return nil, false
	}
	return s.hooks.MCP, true
}

func (s *Server) listMCP(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mcpOK(w)
	if !ok {
		return
	}
	c := m.MCPClient()
	out := []map[string]any{}
	profiles := s.access.Profiles()
	for _, sv := range m.MCPServers() {
		access := []map[string]any{}
		for i := range profiles {
			p := profiles[i]
			if !p.Reply {
				continue
			}
			access = append(access, map[string]any{"name": p.Name, "label": p.Label, "emoji": p.Emoji, "allowed": p.AllowsMCP(sv.Name), "locked": p.Name == "admin"})
		}
		out = append(out, map[string]any{"server": sv, "status": c.Status(r.Context(), sv), "access": access})
	}
	toolPolicy := ""
	if t, ok := s.reg.Get("mcp"); ok {
		toolPolicy = string(s.reg.Policy(t))
	}
	writeJSON(w, 200, map[string]any{"servers": out, "redirect": mcp.DefaultRedirect, "tool_policy": toolPolicy})
}

func (s *Server) saveMCP(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mcpOK(w)
	if !ok {
		return
	}
	var sv mcp.Server
	if err := readJSON(r, &sv); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	if err := m.SaveMCPServer(sv); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "mcp.server_save", sv.Name, map[string]string{"url": sv.URL, "auth": sv.Auth, "token_ref": sv.TokenRef, "policy": sv.Policy})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteMCP(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mcpOK(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := m.DeleteMCPServer(name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "mcp.server_delete", name, nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) findMCP(w http.ResponseWriter, r *http.Request) (MCPHooks, mcp.Server, bool) {
	m, ok := s.mcpOK(w)
	if !ok {
		return nil, mcp.Server{}, false
	}
	for _, sv := range m.MCPServers() {
		if sv.Name == r.PathValue("name") {
			return m, sv, true
		}
	}
	writeErr(w, 404, "unknown server")
	return nil, mcp.Server{}, false
}

// testMCP lists the remote tools (proves URL + auth work).
func (s *Server) testMCP(w http.ResponseWriter, r *http.Request) {
	m, sv, ok := s.findMCP(w, r)
	if !ok {
		return
	}
	tools, err := m.MCPClient().List(r.Context(), sv)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	names := []string{}
	for _, t := range tools {
		names = append(names, t.Name)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tools": names})
}

func (s *Server) mcpAuthStart(w http.ResponseWriter, r *http.Request) {
	m, sv, ok := s.findMCP(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Scope        string `json:"scope"`
	}
	readJSON(r, &body)
	u, redirect, err := m.MCPClient().AuthStart(r.Context(), sv, body.ClientID, body.ClientSecret, body.Scope, s.cfg.PublicURL+mcp.CallbackPath)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"url": u, "redirect": redirect, "auto": redirect != mcp.DefaultRedirect})
}

func (s *Server) mcpAuthFinish(w http.ResponseWriter, r *http.Request) {
	m, sv, ok := s.findMCP(w, r)
	if !ok {
		return
	}
	var body struct {
		RedirectURL string `json:"redirect_url"`
	}
	readJSON(r, &body)
	if err := m.MCPClient().AuthFinish(r.Context(), sv, body.RedirectURL); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "mcp.oauth_connected", sv.Name, nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// mcpOAuthCallback receives the OAuth redirect in the admin's browser,
// exchanges the code and sends them back to the MCP page (no copy-paste).
// The state (server~random, checked against the vault) protects it; the
// admin session is required too (the Lax cookie survives the top-level
// redirect from the provider).
func (s *Server) mcpOAuthCallback(w http.ResponseWriter, r *http.Request) {
	back := func(key, val string) {
		http.Redirect(w, r, "/?"+key+"="+url.QueryEscape(val)+"#/mcp", http.StatusFound)
	}
	if s.sessionUser(r) == nil {
		back("auth_error", "Sign in to the dashboard first, then click Connect again.")
		return
	}
	m, ok := s.mcpOK(w)
	if !ok {
		return
	}
	name := mcp.ServerFromState(r.URL.Query().Get("state"))
	var sv mcp.Server
	for _, x := range m.MCPServers() {
		if x.Name == name {
			sv = x
		}
	}
	if sv.Name == "" {
		back("mcp_error", "Unknown or missing OAuth state: click Connect again.")
		return
	}
	ctx := context.WithValue(r.Context(), userKey{}, s.sessionUser(r))
	if err := m.MCPClient().AuthFinish(ctx, sv, r.URL.String()); err != nil {
		back("mcp_error", sv.Name+": "+err.Error())
		return
	}
	s.st.Audit(actor(r.WithContext(ctx)), "mcp.oauth_connected", sv.Name, nil)
	s.bus.Publish("mcp", map[string]string{"status": "connected", "name": sv.Name})
	back("mcp_connected", sv.Name)
}

// setMCPAccess grants/revokes one server for one access profile.
func (s *Server) setMCPAccess(w http.ResponseWriter, r *http.Request) {
	_, sv, ok := s.findMCP(w, r)
	if !ok {
		return
	}
	var body struct {
		Profile string `json:"profile"`
		Allow   bool   `json:"allow"`
	}
	readJSON(r, &body)
	p, found := s.access.Profile(body.Profile)
	if !found {
		writeErr(w, 404, "unknown profile")
		return
	}
	if p.Name == "admin" {
		writeErr(w, 400, "the admin profile always has every server")
		return
	}
	p.SetMCPAccess(sv.Name, body.Allow)
	if err := s.access.SaveProfile(p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "mcp.access", sv.Name, map[string]string{"profile": p.Name, "allow": map[bool]string{true: "yes", false: "no"}[body.Allow]})
	s.bus.Publish("access", map[string]string{"profile": p.Name})
	writeJSON(w, 200, map[string]any{"ok": true, "tools": p.Tools})
}
