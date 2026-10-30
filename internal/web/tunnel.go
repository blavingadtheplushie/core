package web

import (
	"net/http"
	"strings"

	"blavingad/internal/cloudflare"
)

// tunnel describes the Cloudflare Tunnel and its routes for the dashboard.
func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request) {
	cf := cloudflare.New(s.cfg)
	id := cloudflare.TunnelID(s.cfg, s.st)
	out := map[string]any{"configured": cf.Configured(), "id": id, "routes": []any{}}
	if !cf.Configured() || id == "" {
		writeJSON(w, 200, out)
		return
	}
	t, err := cf.Tunnel(r.Context(), id)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}
	out["name"], out["status"], out["connections"] = t.Name, t.Status, len(t.Connections)
	routes, err := cf.Routes(r.Context(), id)
	if err != nil {
		out["error"] = err.Error()
	} else if routes != nil {
		out["routes"] = routes
	}
	writeJSON(w, 200, out)
}

func (s *Server) tunnelClient(w http.ResponseWriter) (*cloudflare.Client, string, bool) {
	cf := cloudflare.New(s.cfg)
	id := cloudflare.TunnelID(s.cfg, s.st)
	if !cf.Configured() || id == "" {
		writeErr(w, 400, cloudflare.ErrNoTunnel.Error())
		return nil, "", false
	}
	return cf, id, true
}

func (s *Server) addTunnelRoute(w http.ResponseWriter, r *http.Request) {
	var body cloudflare.Route
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	cf, id, ok := s.tunnelClient(w)
	if !ok {
		return
	}
	msg, err := cf.AddRoute(r.Context(), id, cloudflare.Route{Hostname: body.Hostname, Service: body.Service, Path: strings.TrimSpace(body.Path)})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "tunnel.route_add", body.Hostname, map[string]string{"service": body.Service})
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) removeTunnelRoute(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("hostname")
	cf, id, ok := s.tunnelClient(w)
	if !ok {
		return
	}
	msg, err := cf.RemoveRoute(r.Context(), id, host)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "tunnel.route_remove", host, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

// tunnelIntegration is the overview row; it never calls Cloudflare.
func (s *Server) tunnelIntegration() map[string]any {
	cf := cloudflare.New(s.cfg)
	id := cloudflare.TunnelID(s.cfg, s.st)
	detail := "not configured"
	switch {
	case cf.Configured() && id != "":
		detail = "tunnel " + short(id, 8)
	case cf.Configured():
		detail = "not set up yet (ask it: tunnel_setup)"
	}
	return map[string]any{"key": "tunnel", "label": "Cloudflare Tunnel", "emoji": "🚇", "ok": cf.Configured() && id != "", "configured": cf.Configured(), "detail": detail}
}
