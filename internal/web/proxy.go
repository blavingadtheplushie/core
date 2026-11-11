package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"blavingad/internal/proxy"
)

// LLM proxy page: upstreams, aliases, client keys, usage.

func (s *Server) proxyOK(w http.ResponseWriter) (*proxy.Proxy, bool) {
	if s.hooks.Proxy == nil {
		writeErr(w, 503, "llm proxy not available")
		return nil, false
	}
	return s.hooks.Proxy, true
}

func (s *Server) getProxy(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	stats, err := px.Stats(hours)
	out := map[string]any{
		"addr": s.cfg.ProxyAddr, "listening": px.Listening(), "local_url": proxy.LocalURL(s.cfg.ProxyAddr),
		"public_url": s.st.GetSetting("proxy.public_url", ""),
		"upstreams":  px.Upstreams(), "health": px.Health(), "mappings": px.Mappings(), "keys": px.Keys(),
		"aliases": px.Aliases(), "stats": stats,
	}
	if err != nil {
		out["stats_error"] = err.Error()
	}
	if rows, err := s.st.Rows(`SELECT COUNT(*) AS attempts, SUM(CASE WHEN status BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS ok
		FROM llm_usage WHERE key_id = ? AND ts >= ?`, proxy.InternalKeyID, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)); err == nil && len(rows) == 1 {
		out["internal"] = rows[0]
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveProxyUpstream(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	var u proxy.Upstream
	if err := readJSON(r, &u); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := px.SaveUpstream(u); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "proxy.upstream_save", u.Name, map[string]string{"base_url": u.BaseURL, "style": u.Style, "key_ref": u.KeyRef})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteProxyUpstream(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := px.DeleteUpstream(name); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	s.st.Audit(actor(r), "proxy.upstream_delete", name, nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) saveProxyMapping(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	var m proxy.Mapping
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if m.Upstream == "" {
		m.Upstream = "*"
	}
	if err := px.SetMapping(m); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "proxy.map_set", m.Alias, map[string]string{"upstream": m.Upstream, "model": m.Model})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteProxyMapping(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	alias, up := r.URL.Query().Get("alias"), r.URL.Query().Get("upstream")
	if err := px.DeleteMapping(alias, up); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	s.st.Audit(actor(r), "proxy.map_delete", alias, map[string]string{"upstream": up})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) createProxyKey(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	var body struct {
		Name   string `json:"name"`
		Models string `json:"models"` // comma separated
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	var models []string
	for _, m := range strings.Split(body.Models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	k, plain, err := px.CreateKey(body.Name, actor(r), models)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "proxy.key_create", k.Name, map[string]string{"prefix": k.Prefix})
	// The plaintext key is returned once, to the logged-in admin only.
	writeJSON(w, 200, map[string]any{"key": k, "secret": plain})
}

func (s *Server) deleteProxyKey(w http.ResponseWriter, r *http.Request) {
	px, ok := s.proxyOK(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	del := r.URL.Query().Get("delete") == "1"
	if err := px.RevokeKey(id, del); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	act := "proxy.key_revoke"
	if del {
		act = "proxy.key_delete"
	}
	s.st.Audit(actor(r), act, id, nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}
