package web

import (
	"context"
	"net/http"
	"time"

	"blavingad/internal/agent"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/textutil"
)

// Model router page (#/router): fast endpoint, routing rules, 7-day stats.

func (s *Server) routerPayload() map[string]any {
	c := s.runner.RouterConfig()
	ready, why := s.runner.RouterReady(c)
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	stats, _ := s.st.Rows(`SELECT r.model AS model, COUNT(*) AS runs, COALESCE(SUM(r.prompt_tokens),0) AS prompt_tokens,
		COALESCE(SUM(r.completion_tokens),0) AS completion_tokens FROM runs r WHERE r.started_at >= ? AND r.parent_id IS NULL GROUP BY r.model ORDER BY runs DESC`, since)
	escal, _ := s.st.Rows(`SELECT COUNT(*) AS n FROM run_steps WHERE kind = 'route' AND input = 'escalate' AND created_at >= ?`, since)
	recent, _ := s.st.Rows(`SELECT st.run_id AS run_id, st.name AS model, st.input AS decision, st.output AS reason, st.created_at AS created_at,
		COALESCE(r.input,'') AS input FROM run_steps st LEFT JOIN runs r ON r.id = st.run_id WHERE st.kind = 'route' ORDER BY st.id DESC LIMIT 25`)
	for _, row := range recent {
		row["input"] = textutil.Short(core.Secrets.Redact(textutil.FirstNonEmpty(asString(row["input"]))), 120)
	}
	n := 0
	if len(escal) == 1 {
		if v, ok := escal[0]["n"].(int64); ok {
			n = int(v)
		}
	}
	set := s.runner.Settings()
	return map[string]any{"config": c, "defaults": agent.DefaultRouterConfig(), "sources": agent.RouterSources, "ready": ready, "why": why,
		"main_model": set.Model, "stats": stats, "escalations": n, "recent": recent}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *Server) getRouter(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.routerPayload())
}

func (s *Server) putRouter(w http.ResponseWriter, r *http.Request) {
	var c agent.RouterConfig
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.runner.SaveRouterConfig(c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "router.update", "", map[string]any{"enabled": c.Enabled, "model": c.Model, "base_url": c.BaseURL, "key_ref": c.KeyRef})
	writeJSON(w, 200, s.routerPayload())
}

// testRouter sends a tiny request to the fast endpoint.
func (s *Server) testRouter(w http.ResponseWriter, r *http.Request) {
	c := s.runner.RouterConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.runner.FastClient().Chat(ctx, llm.Request{Model: c.Model, MaxTokens: 32, Temperature: llm.F(0),
		Messages: []llm.Message{{Role: "system", Content: "You are a health check."}, {Role: "user", Content: "Reply with the single word: OK"}}})
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": core.Secrets.Redact(err.Error()), "model": c.Model})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "reply": textutil.Short(res.Message.Content, 200), "model": c.Model,
		"latency_ms": time.Since(start).Milliseconds(), "tokens": res.Usage.PromptTokens + res.Usage.CompletionTokens})
}
