package web

import (
	"net/http"

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
	for _, sv := range m.MCPServers() {
		out = append(out, map[string]any{"server": sv, "status": c.Status(r.Context(), sv)})
	}
	writeJSON(w, 200, map[string]any{"servers": out, "redirect": mcp.DefaultRedirect})
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
	s.st.Audit(actor(r), "mcp.server_save", sv.Name, map[string]string{"url": sv.URL, "auth": sv.Auth, "token_ref": sv.TokenRef})
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
	u, err := m.MCPClient().AuthStart(r.Context(), sv, body.ClientID, body.ClientSecret, body.Scope, "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"url": u})
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
