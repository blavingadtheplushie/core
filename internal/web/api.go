package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"blavingad/internal/agent"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/memory"
	"blavingad/internal/scheduler"
	"blavingad/internal/selfupdate"
	"blavingad/internal/store"
	"blavingad/internal/vault"
)

func page(r *http.Request, size int) (limit, offset, p int) {
	p, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return size, (p - 1) * size, p
}

func actor(r *http.Request) string {
	if u := currentUser(r); u != nil {
		return firstNonEmpty(u.Name, u.Email)
	}
	return "web"
}

// ---------- overview ----------

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	day := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	week := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var tokensToday int
	s.st.DB.QueryRow(`SELECT COALESCE(SUM(prompt_tokens + completion_tokens),0) FROM runs WHERE started_at > ?`, day).Scan(&tokensToday)
	stats := map[string]any{
		"runs_today":        s.st.Count(`SELECT COUNT(*) FROM runs WHERE started_at > ? AND parent_id IS NULL`, day),
		"runs_week":         s.st.Count(`SELECT COUNT(*) FROM runs WHERE started_at > ? AND parent_id IS NULL`, week),
		"runs_total":        s.st.Count(`SELECT COUNT(*) FROM runs WHERE parent_id IS NULL`),
		"tool_calls_today":  s.st.Count(`SELECT COUNT(*) FROM run_steps WHERE kind = 'tool' AND created_at > ?`, day),
		"tokens_today":      tokensToday,
		"memories":          s.st.Count(`SELECT COUNT(*) FROM memories`),
		"people":            s.st.Count(`SELECT COUNT(*) FROM people`),
		"messages_indexed":  s.st.Count(`SELECT COUNT(*) FROM slack_messages`),
		"pending_approvals": s.st.Count(`SELECT COUNT(*) FROM approvals WHERE status = 'pending'`),
		"deployments_live":  s.st.Count(`SELECT COUNT(*) FROM deployments WHERE status = 'live'`),
		"custom_tools":      s.st.Count(`SELECT COUNT(*) FROM custom_tools`),
		"skills":            s.st.Count(`SELECT COUNT(*) FROM skills`),
		"evolutions_week":   s.st.Count(`SELECT COUNT(*) FROM evolutions WHERE created_at > ?`, week),
		"active_runs":       s.runner.ActiveCount(),
		"feedback_up":       s.st.Count(`SELECT COUNT(*) FROM runs WHERE feedback > 0 AND started_at > ?`, week),
		"feedback_down":     s.st.Count(`SELECT COUNT(*) FROM runs WHERE feedback < 0 AND started_at > ?`, week),
	}
	daily := []map[string]any{}
	counts := map[string][2]int{}
	if rows, err := s.st.DB.Query(`SELECT substr(started_at,1,10) AS d, COUNT(*), COALESCE(SUM(tool_calls),0) FROM runs WHERE started_at > ? AND parent_id IS NULL GROUP BY d`,
		time.Now().Add(-14*24*time.Hour).UTC().Format(time.RFC3339)); err == nil {
		for rows.Next() {
			var d string
			var n, t int
			rows.Scan(&d, &n, &t)
			counts[d] = [2]int{n, t}
		}
		rows.Close()
	}
	for i := 13; i >= 0; i-- {
		d := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		daily = append(daily, map[string]any{"day": d, "runs": counts[d][0], "tools": counts[d][1]})
	}
	topTools, _ := s.st.Rows(`SELECT name, COUNT(*) AS calls, SUM(CASE WHEN status != 'ok' THEN 1 ELSE 0 END) AS errors, CAST(AVG(duration_ms) AS INTEGER) AS avg_ms
		FROM run_steps WHERE kind = 'tool' AND created_at > ? GROUP BY name ORDER BY calls DESC LIMIT 8`, week)
	recent, _ := s.st.Rows(`SELECT id, source, COALESCE(user_name,'') AS user_name, COALESCE(channel_name,'') AS channel_name, substr(input,1,220) AS input, status, tool_calls, started_at, finished_at
		FROM runs WHERE parent_id IS NULL ORDER BY started_at DESC LIMIT 8`)
	evolutions, _ := s.st.Rows(`SELECT id, kind, summary, status, created_at FROM evolutions ORDER BY created_at DESC LIMIT 5`)
	sources, _ := s.st.Rows(`SELECT source AS label, COUNT(*) AS value FROM runs WHERE started_at > ? AND parent_id IS NULL GROUP BY source ORDER BY value DESC`, week)
	writeJSON(w, 200, map[string]any{
		"stats": stats, "daily": daily, "top_tools": topTools, "recent": recent, "evolutions": evolutions, "sources": sources,
		"integrations": s.integrations(), "version": s.self.Version, "uptime": time.Since(s.started).Round(time.Second).String(),
		"memory_kinds": s.mem.Stats(),
	})
}

func host(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

func (s *Server) integrations() []map[string]any {
	c := s.cfg
	slack := map[string]any{"connected": false}
	if s.hooks.SlackStatus != nil {
		slack = s.hooks.SlackStatus()
	}
	slackDetail := "not configured"
	slackOK := false
	if c.SlackConfigured() {
		slackOK, _ = slack["connected"].(bool)
		slackDetail = "disconnected"
		if slackOK {
			slackDetail = "Socket Mode connected"
		}
	}
	emailDetail := "not configured"
	emailOK := c.IMAPConfigured() || c.SMTPConfigured()
	if emailOK {
		emailDetail = c.EmailFrom
		if s.mail != nil && s.mail.LastError != "" {
			emailOK = false
			emailDetail = "error: " + s.mail.LastError
		}
	}
	gh := "not configured"
	if c.GitHubConfigured() {
		gh = firstNonEmpty(c.GitHubUser, "token configured")
		if c.SelfRepo != "" {
			gh += " · " + c.SelfRepo
		}
	}
	embed := "disabled (keywords only)"
	if c.EmbedConfigured() {
		embed = c.EmbedModel
	}
	docker := "disabled"
	if c.DockerEnabled {
		docker = "Docker socket mounted"
		if c.DeployTraefik && c.DeployDomain != "" {
			docker += " · *." + c.DeployDomain
		}
	}
	oidcDetail := "not configured"
	if c.OIDCConfigured() {
		oidcDetail = host(c.OIDCIssuer)
	}
	vaultRow := map[string]any{"key": "vault", "label": "Vaultwarden vault", "emoji": "🔐", "ok": false, "detail": "not configured"}
	if s.hooks.VaultStatus != nil {
		if st, ok := s.hooks.VaultStatus()["status"].(vault.Status); ok && st.Enabled {
			vaultRow["configured"] = true
			vaultRow["ok"] = st.Unlocked
			vaultRow["detail"] = fmt.Sprintf("%s · %d items", host(st.URL), st.Items)
			if st.Error != "" {
				vaultRow["detail"] = "error: " + st.Error
			}
		}
	}
	return []map[string]any{
		vaultRow,
		{"key": "llm", "label": "Model", "emoji": "🧠", "ok": c.LLMConfigured(), "detail": s.llmDetail()},
		{"key": "slack", "label": "Slack", "emoji": "💬", "ok": slackOK, "configured": c.SlackConfigured(), "detail": slackDetail},
		{"key": "exa", "label": "Web search (Exa)", "emoji": "🔎", "ok": c.ExaConfigured(), "detail": map[bool]string{true: "key configured", false: "not configured"}[c.ExaConfigured()]},
		{"key": "embed", "label": "Semantic memory", "emoji": "🧬", "ok": c.EmbedConfigured(), "detail": embed},
		{"key": "github", "label": "GitHub", "emoji": "🐙", "ok": c.GitHubConfigured(), "detail": gh},
		{"key": "email", "label": "Email", "emoji": "📬", "ok": emailOK, "configured": c.IMAPConfigured() || c.SMTPConfigured(), "detail": emailDetail},
		{"key": "cloudflare", "label": "Cloudflare", "emoji": "☁️", "ok": c.CloudflareConfigured(), "detail": map[bool]string{true: "account " + short(c.CloudflareAccount, 8), false: "not configured"}[c.CloudflareConfigured()]},
		{"key": "docker", "label": "Docker", "emoji": "🐳", "ok": c.DockerEnabled, "detail": docker},
		s.tunnelIntegration(),
		{"key": "oidc", "label": "SSO (OIDC)", "emoji": "🛡️", "ok": c.OIDCConfigured(), "detail": oidcDetail},
	}
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- runs ----------

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset, p := page(r, 25)
	q := r.URL.Query()
	where := []string{"1=1"}
	args := []any{}
	if q.Get("all") != "1" {
		where = append(where, "parent_id IS NULL")
	}
	if v := q.Get("source"); v != "" {
		where = append(where, "source = ?")
		args = append(args, v)
	}
	if v := q.Get("status"); v != "" {
		where = append(where, "status = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		where = append(where, "(input LIKE ? OR output LIKE ? OR user_name LIKE ?)")
		like := "%" + v + "%"
		args = append(args, like, like, like)
	}
	wc := strings.Join(where, " AND ")
	total := s.st.Count(`SELECT COUNT(*) FROM runs WHERE `+wc, args...)
	items, err := s.st.Rows(`SELECT id, source, COALESCE(user_name,'') AS user_name, COALESCE(channel_name,'') AS channel_name, substr(input,1,240) AS input,
		substr(COALESCE(output,''),1,240) AS output, status, model, iterations, tool_calls, prompt_tokens, completion_tokens, feedback, started_at, finished_at, parent_id
		FROM runs WHERE `+wc+` ORDER BY started_at DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": p, "page_size": limit})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.st.Row(`SELECT * FROM runs WHERE id = ?`, id)
	if err != nil {
		writeErr(w, 404, "run not found")
		return
	}
	steps, _ := s.st.Rows(`SELECT id, kind, name, input, output, status, duration_ms, tokens, created_at FROM run_steps WHERE run_id = ? ORDER BY id`, id)
	children, _ := s.st.Rows(`SELECT id, substr(input,1,200) AS input, status, tool_calls, started_at, finished_at FROM runs WHERE parent_id = ? ORDER BY started_at`, id)
	approvals, _ := s.st.Rows(`SELECT id, tool, summary, status, decided_by, created_at, decided_at FROM approvals WHERE run_id = ? ORDER BY created_at`, id)
	// Resolve recalled memory ids into content for the trace view.
	for _, st := range steps {
		if st["kind"] == "recall" {
			var parts []string
			for _, idStr := range strings.Split(fmt.Sprint(st["output"]), ",") {
				if mid, err := strconv.ParseInt(idStr, 10, 64); err == nil {
					if m, err := s.mem.Get(mid); err == nil {
						parts = append(parts, fmt.Sprintf("#%d [%s] %s", m.ID, m.Kind, m.Content))
					}
				}
			}
			st["output"] = strings.Join(parts, "\n")
		}
	}
	writeJSON(w, 200, map[string]any{"run": run, "steps": steps, "children": children, "approvals": approvals, "active": s.isActive(id)})
}

func (s *Server) isActive(id string) bool {
	var status string
	s.st.DB.QueryRow(`SELECT status FROM runs WHERE id = ?`, id).Scan(&status)
	return status == "running"
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.runner.Cancel(r.PathValue("id")) {
		writeErr(w, 404, "this run is not in progress")
		return
	}
	s.st.Audit(actor(r), "run.cancel", r.PathValue("id"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- chat ----------

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Thread  string `json:"thread"`
		Message string `json:"message"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeErr(w, 400, "empty message")
		return
	}
	if body.Thread == "" {
		body.Thread = store.NewID("web")
	}
	var history []llm.Message
	prev, _ := s.st.Rows(`SELECT input, COALESCE(output,'') AS output FROM runs WHERE source = 'web' AND thread_ts = ? AND parent_id IS NULL ORDER BY started_at DESC LIMIT 12`, body.Thread)
	for i := len(prev) - 1; i >= 0; i-- {
		history = append(history, llm.Message{Role: "user", Content: fmt.Sprint(prev[i]["input"])})
		if out := fmt.Sprint(prev[i]["output"]); out != "" {
			history = append(history, llm.Message{Role: "assistant", Content: out})
		}
	}
	u := currentUser(r)
	started := make(chan string, 1)
	go s.runner.Run(context.Background(), agent.Request{
		Source: "web", Channel: "web", ChannelName: "web", ThreadTS: body.Thread, UserName: firstNonEmpty(u.Name, u.Email, "admin"),
		Text: body.Message, History: history, OnStart: func(id string) { started <- id },
	})
	select {
	case id := <-started:
		writeJSON(w, 200, map[string]string{"run_id": id, "thread": body.Thread})
	case <-time.After(10 * time.Second):
		writeErr(w, 500, "the run did not start")
	}
}

func (s *Server) chatThreads(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT thread_ts AS id, MIN(started_at) AS started_at, MAX(started_at) AS updated_at, COUNT(*) AS turns,
		(SELECT substr(input,1,120) FROM runs r2 WHERE r2.thread_ts = runs.thread_ts AND r2.source = 'web' ORDER BY started_at LIMIT 1) AS title
		FROM runs WHERE source = 'web' AND parent_id IS NULL GROUP BY thread_ts ORDER BY updated_at DESC LIMIT 50`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (s *Server) chatThread(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT id, input, COALESCE(output,'') AS output, status, tool_calls, started_at, finished_at, COALESCE(user_name,'') AS user_name
		FROM runs WHERE source = 'web' AND thread_ts = ? AND parent_id IS NULL ORDER BY started_at`, r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The chronological thought process shown above each answer.
	for _, run := range rows {
		steps, _ := s.st.Rows(`SELECT kind, name, substr(COALESCE(input,''),1,600) AS input, substr(COALESCE(output,''),1,800) AS output, status
			FROM run_steps WHERE run_id = ? AND kind IN ('recall','thought','tool','reflect') ORDER BY id`, run["id"])
		run["steps"] = steps
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

// ---------- approvals ----------

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	q := `SELECT a.id, a.run_id, a.tool, a.args, a.summary, a.status, COALESCE(a.requested_by,'') AS requested_by, COALESCE(a.decided_by,'') AS decided_by,
		a.created_at, a.decided_at, substr(COALESCE(r.input,''),1,200) AS run_input FROM approvals a LEFT JOIN runs r ON r.id = a.run_id`
	var args []any
	if status != "" && status != "all" {
		q += " WHERE a.status = ?"
		args = append(args, status)
	}
	q += " ORDER BY a.created_at DESC LIMIT 100"
	rows, err := s.st.Rows(q, args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	var body struct{ Decision string }
	readJSON(r, &body)
	if body.Decision != "approve" && body.Decision != "deny" && body.Decision != "always" {
		writeErr(w, 400, "invalid decision")
		return
	}
	if err := s.approvals.Decide(r.PathValue("id"), body.Decision, actor(r)+" (web)"); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- memory ----------

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	limit, offset, p := page(r, 30)
	q := r.URL.Query()
	if q.Get("semantic") == "1" && strings.TrimSpace(q.Get("q")) != "" {
		items := s.mem.Search(r.Context(), q.Get("q"), q.Get("kind"), 30)
		if items == nil {
			items = []memory.Memory{}
		}
		writeJSON(w, 200, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 30, "kinds": s.mem.Stats(), "vectors": s.mem.VectorsEnabled()})
		return
	}
	items, total, err := s.mem.List(q.Get("q"), q.Get("kind"), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": p, "page_size": limit, "kinds": s.mem.Stats(), "vectors": s.mem.VectorsEnabled()})
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var m memory.Memory
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	m.Source = "web:" + actor(r)
	res, err := s.mem.Save(r.Context(), m)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "memory.create", fmt.Sprint(res.ID), nil)
	writeJSON(w, 201, res)
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var m memory.Memory
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.mem.Update(r.Context(), id, m); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "memory not found")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "memory.update", fmt.Sprint(id), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.mem.Delete(id); err != nil {
		writeErr(w, 404, "memory not found")
		return
	}
	s.st.Audit(actor(r), "memory.delete", fmt.Sprint(id), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listPeople(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT slack_id, COALESCE(name,'') AS name, COALESCE(real_name,'') AS real_name, COALESCE(title,'') AS title, COALESCE(email,'') AS email,
		COALESCE(tz,'') AS tz, notes, message_count, last_seen_at FROM people ORDER BY message_count DESC, real_name LIMIT 300`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, p := range rows {
		prof, listed := s.access.Resolve(fmt.Sprint(p["slack_id"]))
		p["profile"], p["profile_label"], p["profile_emoji"], p["listed"] = prof.Name, prof.Label, prof.Emoji, listed
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (s *Server) updatePerson(w http.ResponseWriter, r *http.Request) {
	var body struct{ Notes string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.mem.SetPersonNotes(r.PathValue("id"), body.Notes); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "person.update", r.PathValue("id"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT c.id, COALESCE(c.name,'') AS name, COALESCE(c.kind,'') AS kind, COALESCE(c.topic,'') AS topic, c.digest, c.updated_at,
		(SELECT COUNT(*) FROM slack_messages m WHERE m.channel = c.id) AS messages,
		(SELECT MAX(ts) FROM slack_messages m WHERE m.channel = c.id) AS last_ts
		FROM channels c ORDER BY messages DESC LIMIT 200`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (s *Server) digestChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.st.DB.Exec(`UPDATE channels SET digest_until = '0' WHERE id = ? AND digest = ''`, id)
	if err := s.runner.DigestChannel(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "digest": s.mem.ChannelDigest(id)})
}

// ---------- tools ----------

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	week := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	usage := map[string][2]int{}
	if rows, err := s.st.DB.Query(`SELECT name, COUNT(*), SUM(CASE WHEN status != 'ok' THEN 1 ELSE 0 END) FROM run_steps WHERE kind = 'tool' AND created_at > ? GROUP BY name`, week); err == nil {
		for rows.Next() {
			var n string
			var c, e int
			rows.Scan(&n, &c, &e)
			usage[n] = [2]int{c, e}
		}
		rows.Close()
	}
	custom := map[string]map[string]any{}
	if rows, err := s.st.Rows(`SELECT name, runtime, code, author, created_at, updated_at FROM custom_tools`); err == nil {
		for _, c := range rows {
			custom[fmt.Sprint(c["name"])] = c
		}
	}
	out := []map[string]any{}
	for _, t := range s.reg.All() {
		item := map[string]any{
			"name": t.Name, "description": t.Description, "category": t.Category, "emoji": t.Emoji,
			"sensitive": t.Risk == core.RiskSensitive, "policy": s.reg.Policy(t), "default_policy": core.DefaultPolicy(t),
			"available": t.IsAvailable(), "custom": t.Custom, "params": t.Params, "conditional": t.NeedsApproval != nil,
			"calls_7d": usage[t.Name][0], "errors_7d": usage[t.Name][1],
		}
		if c, ok := custom[t.Name]; ok {
			item["runtime"], item["code"], item["author"], item["updated_at"] = c["runtime"], c["code"], c["author"], c["updated_at"]
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) updateTool(w http.ResponseWriter, r *http.Request) {
	var body struct{ Policy string }
	readJSON(r, &body)
	p := core.Policy(body.Policy)
	if p != core.PolicyAuto && p != core.PolicyApprove && p != core.PolicyOff {
		writeErr(w, 400, "invalid policy")
		return
	}
	if err := s.reg.SetPolicy(r.PathValue("name"), p); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	s.st.Audit(actor(r), "tool.policy", r.PathValue("name"), map[string]string{"policy": body.Policy})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t, ok := s.reg.Get(name)
	if !ok || !t.Custom {
		writeErr(w, 400, "only agent-created tools can be deleted")
		return
	}
	s.st.DB.Exec(`DELETE FROM custom_tools WHERE name = ?`, name)
	s.reg.Remove(name)
	s.st.Audit(actor(r), "tool.delete", name, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- skills & persona ----------

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT id, name, description, content, enabled, COALESCE(author,'') AS author, uses, created_at, updated_at FROM skills ORDER BY updated_at DESC`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (s *Server) saveSkill(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Description, Content string }
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "name and content are required")
		return
	}
	name := strings.Trim(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, body.Name), "-")
	now := store.Now()
	_, err := s.st.DB.Exec(`INSERT INTO skills(id, name, description, content, author, created_at, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description, content = excluded.content, updated_at = excluded.updated_at`,
		store.NewID("skl"), name, body.Description, body.Content, actor(r), now, now)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "skill.save", name, nil)
	writeJSON(w, 200, map[string]string{"name": name})
}

func (s *Server) toggleSkill(w http.ResponseWriter, r *http.Request) {
	var body struct{ Enabled bool }
	readJSON(r, &body)
	s.st.DB.Exec(`UPDATE skills SET enabled = ? WHERE name = ?`, body.Enabled, r.PathValue("name"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	s.st.DB.Exec(`DELETE FROM skills WHERE name = ?`, r.PathValue("name"))
	s.st.Audit(actor(r), "skill.delete", r.PathValue("name"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getPersona(w http.ResponseWriter, r *http.Request) {
	versions, _ := s.st.Rows(`SELECT id, content, COALESCE(reason,'') AS reason, COALESCE(author,'') AS author, created_at FROM persona_versions ORDER BY id DESC LIMIT 30`)
	writeJSON(w, 200, map[string]any{"current": core.CurrentPersona(s.st, "{{NAME}}"), "default": core.DefaultPersona, "versions": versions, "name": s.cfg.AgentName})
}

func (s *Server) putPersona(w http.ResponseWriter, r *http.Request) {
	var body struct{ Content, Reason string }
	if err := readJSON(r, &body); err != nil || len(strings.TrimSpace(body.Content)) < 20 {
		writeErr(w, 400, "persona too short")
		return
	}
	s.st.DB.Exec(`INSERT INTO persona_versions(content, reason, author, created_at) VALUES(?,?,?,?)`, body.Content, firstNonEmpty(body.Reason, "edited from the web"), actor(r), store.Now())
	s.st.DB.Exec(`INSERT INTO evolutions(id, kind, summary, details, status, created_at) VALUES(?,?,?,?,?,?)`, store.NewID("evo"), "persona", "Persona edited by "+actor(r), body.Content, "live", store.Now())
	s.st.Audit(actor(r), "persona.update", "", nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- evolution ----------

func (s *Server) getEvolution(w http.ResponseWriter, r *http.Request) {
	items, _ := s.st.Rows(`SELECT id, kind, summary, details, COALESCE(commit_sha,'') AS commit_sha, status, COALESCE(run_id,'') AS run_id, created_at FROM evolutions ORDER BY created_at DESC LIMIT 100`)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, 200, map[string]any{"items": items, "info": s.self.Info(ctx), "settings": s.runner.Settings()})
}

func (s *Server) triggerReview(w http.ResponseWriter, r *http.Request) {
	if s.hooks.RunSelfReview == nil {
		writeErr(w, 500, "unavailable")
		return
	}
	go s.hooks.RunSelfReview()
	s.st.Audit(actor(r), "self.review", "", nil)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	rc := &core.RunContext{Source: "web", UserName: actor(r)}
	out, err := s.self.Rollback(r.Context(), rc, "requested from the dashboard by "+actor(r))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"output": out})
}

// ---------- schedules ----------

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT s.id, s.name, COALESCE(s.cron,'') AS cron, COALESCE(s.run_at,'') AS run_at, s.prompt, COALESCE(s.channel,'') AS channel,
		COALESCE(c.name,'') AS channel_name, s.enabled, COALESCE(s.created_by,'') AS created_by, s.last_run_at, s.next_run_at, s.created_at
		FROM schedules s LEFT JOIN channels c ON c.id = s.channel ORDER BY s.enabled DESC, s.next_run_at`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows, "timezone": s.cfg.TZName, "self_review": s.runner.Settings().SelfReviewCron})
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Prompt, Cron, At, Channel string }
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		writeErr(w, 400, "prompt is required")
		return
	}
	id, next, err := scheduler.Create(s.st, s.cfg.Timezone, scheduler.Schedule{Name: firstNonEmpty(body.Name, "Task"), Prompt: body.Prompt, Cron: body.Cron, RunAt: body.At, Channel: body.Channel, CreatedBy: actor(r)})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if s.hooks.ReloadSched != nil {
		s.hooks.ReloadSched()
	}
	s.st.Audit(actor(r), "schedule.create", id, nil)
	writeJSON(w, 201, map[string]string{"id": id, "next_run_at": next.UTC().Format(time.RFC3339)})
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct{ Enabled bool }
	readJSON(r, &body)
	id := r.PathValue("id")
	var cronExpr, runAt string
	if err := s.st.DB.QueryRow(`SELECT COALESCE(cron,''), COALESCE(run_at,'') FROM schedules WHERE id = ?`, id).Scan(&cronExpr, &runAt); err != nil {
		writeErr(w, 404, "task not found")
		return
	}
	var next any
	if body.Enabled {
		if t, err := scheduler.Next(cronExpr, runAt, time.Now(), s.cfg.Timezone); err == nil && (cronExpr != "" || t.After(time.Now())) {
			next = t.UTC().Format(time.RFC3339)
		} else {
			writeErr(w, 400, "this one-off task is in the past")
			return
		}
	}
	s.st.DB.Exec(`UPDATE schedules SET enabled = ?, next_run_at = ? WHERE id = ?`, body.Enabled, next, id)
	if s.hooks.ReloadSched != nil {
		s.hooks.ReloadSched()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	s.st.DB.Exec(`DELETE FROM schedules WHERE id = ?`, r.PathValue("id"))
	s.st.Audit(actor(r), "schedule.delete", r.PathValue("id"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	var sc scheduler.Schedule
	err := s.st.DB.QueryRow(`SELECT id, name, COALESCE(cron,''), COALESCE(run_at,''), prompt, COALESCE(channel,''), COALESCE(thread_ts,''), COALESCE(created_by,''), COALESCE(profile,'') FROM schedules WHERE id = ?`, r.PathValue("id")).
		Scan(&sc.ID, &sc.Name, &sc.Cron, &sc.RunAt, &sc.Prompt, &sc.Channel, &sc.ThreadTS, &sc.CreatedBy, &sc.Profile)
	if err != nil {
		writeErr(w, 404, "task not found")
		return
	}
	go s.hooks.RunSchedule(sc)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// ---------- deployments / emails / audit ----------

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Rows(`SELECT id, kind, name, COALESCE(url,'') AS url, status, details, COALESCE(run_id,'') AS run_id, created_at FROM deployments ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Collapse history: latest deployment per (kind, name) first.
	seen := map[string]bool{}
	var latest, history []map[string]any
	for _, d := range rows {
		k := fmt.Sprint(d["kind"], "/", d["name"])
		if !seen[k] {
			seen[k] = true
			latest = append(latest, d)
		} else {
			history = append(history, d)
		}
	}
	sort.SliceStable(latest, func(i, j int) bool { return latest[i]["status"] == "live" && latest[j]["status"] != "live" })
	writeJSON(w, 200, map[string]any{"items": latest, "history": history})
}

func (s *Server) listEmails(w http.ResponseWriter, r *http.Request) {
	limit, offset, p := page(r, 30)
	dir := r.URL.Query().Get("dir")
	where, args := "1=1", []any{}
	if dir == "in" || dir == "out" {
		where, args = "direction = ?", append(args, dir)
	}
	total := s.st.Count(`SELECT COUNT(*) FROM emails WHERE `+where, args...)
	rows, err := s.st.Rows(`SELECT id, direction, COALESCE(from_addr,'') AS from_addr, COALESCE(to_addr,'') AS to_addr, COALESCE(subject,'') AS subject,
		substr(COALESCE(body,''),1,180) AS preview, COALESCE(run_id,'') AS run_id, created_at FROM emails WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	lastPoll := ""
	lastErr := ""
	if s.mail != nil {
		if !s.mail.LastPoll.IsZero() {
			lastPoll = s.mail.LastPoll.UTC().Format(time.RFC3339)
		}
		lastErr = s.mail.LastError
	}
	writeJSON(w, 200, map[string]any{"items": rows, "total": total, "page": p, "page_size": limit, "address": s.cfg.EmailFrom,
		"imap": s.cfg.IMAPConfigured(), "smtp": s.cfg.SMTPConfigured(), "last_poll": lastPoll, "last_error": lastErr})
}

func (s *Server) getEmail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	e, err := s.mail.Get(id)
	if err != nil {
		writeErr(w, 404, "email not found")
		return
	}
	writeJSON(w, 200, e)
}

func (s *Server) refreshEmails(w http.ResponseWriter, r *http.Request) {
	if s.mail == nil || !s.cfg.IMAPConfigured() {
		writeErr(w, 400, "IMAP is not configured")
		return
	}
	n, err := s.mail.Fetch(r.Context())
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"received": n})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c := s.cfg
	writeJSON(w, 200, map[string]any{
		"settings": s.runner.Settings(),
		"env": map[string]any{
			"agent_name": c.AgentName, "language": c.Language, "timezone": c.TZName, "public_url": c.PublicURL,
			"llm_base_url": c.LLMBaseURL, "llm_context": c.LLMContextLimit, "llm_max_tokens": c.LLMMaxTokens,
			"embed_model": c.EmbedModel, "workspace": c.WorkspaceDir, "source_dir": c.SourceDir, "data_dir": c.DataDir,
			"slack_admins": c.SlackAdmins, "self_repo": c.SelfRepo, "email": c.EmailFrom, "deploy_domain": c.DeployDomain,
			"version": s.self.Version,
		},
	})
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	allowed := map[string]bool{}
	for _, k := range agent.SettingKeys {
		allowed[k] = true
	}
	for k, v := range body {
		if !allowed[k] {
			writeErr(w, 400, "unknown setting: "+k)
			return
		}
		val := fmt.Sprint(v)
		switch k {
		case "temperature":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil || f < 0 || f > 2 {
				writeErr(w, 400, "temperature must be between 0 and 2")
				return
			}
		case "max_iterations":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > 100 {
				writeErr(w, 400, "iterations must be between 1 and 100")
				return
			}
		case "bot_reply_limit":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 || n > 200 {
				writeErr(w, 400, "bot reply limit must be between 0 and 200")
				return
			}
		case "self_review_cron":
			if _, err := scheduler.Next(val, "", time.Now(), s.cfg.Timezone); err != nil {
				writeErr(w, 400, "invalid cron")
				return
			}
		case "model", "fast_model":
			if strings.TrimSpace(val) == "" {
				writeErr(w, 400, "model is required")
				return
			}
		case "email_profile":
			val = strings.TrimSpace(val)
			if _, ok := s.access.Profile(val); !ok || val == "admin" {
				writeErr(w, 400, "email profile must be an existing, non-admin access profile")
				return
			}
			v = val
		case "api_style":
			if val != llm.StyleOpenAI && val != llm.StyleAnthropic {
				writeErr(w, 400, "api_style must be openai or anthropic")
				return
			}
		case "base_url":
			val = strings.TrimRight(strings.TrimSpace(val), "/")
			if val != "" {
				if u, err := url.Parse(val); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					writeErr(w, 400, "base URL must be an http(s) URL")
					return
				}
			}
			v = val
		}
		if b, ok := v.(bool); ok {
			val = strconv.FormatBool(b)
		}
		s.st.SetSetting("agent."+k, val)
	}
	s.st.Audit(actor(r), "settings.update", "", body)
	writeJSON(w, 200, map[string]any{"settings": s.runner.Settings()})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, p := page(r, 40)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where, args := "1=1", []any{}
	if q != "" {
		where = "(action LIKE ? OR actor LIKE ? OR target LIKE ?)"
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	total := s.st.Count(`SELECT COUNT(*) FROM audit WHERE `+where, args...)
	rows, err := s.st.Rows(`SELECT id, COALESCE(actor,'') AS actor, action, COALESCE(target,'') AS target, COALESCE(details,'') AS details, created_at FROM audit WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows, "total": total, "page": p, "page_size": limit})
}

// ---------- access ----------

func (s *Server) getAccess(w http.ResponseWriter, r *http.Request) {
	profiles := s.access.Profiles()
	out := make([]map[string]any, 0, len(profiles))
	for i := range profiles {
		p := profiles[i]
		out = append(out, map[string]any{"profile": p, "resolved": s.access.AllowedTools(s.reg, &p)})
	}
	var catalog []map[string]any
	for _, t := range s.reg.All() {
		catalog = append(catalog, map[string]any{"name": t.Name, "category": t.Category, "emoji": t.Emoji, "sensitive": t.Risk == core.RiskSensitive, "available": t.IsAvailable()})
	}
	people, _ := s.st.Rows(`SELECT slack_id, COALESCE(NULLIF(real_name,''), name, '') AS name FROM people ORDER BY message_count DESC LIMIT 500`)
	writeJSON(w, 200, map[string]any{"profiles": out, "default": s.access.DefaultProfile(), "members": s.access.Members(), "tools": catalog, "people": people})
}

func (s *Server) setAccessDefault(w http.ResponseWriter, r *http.Request) {
	var body struct{ Profile string }
	readJSON(r, &body)
	if body.Profile == "admin" {
		writeErr(w, 400, "giving everyone the admin profile is not allowed")
		return
	}
	if err := s.access.SetDefault(body.Profile); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "access.default", body.Profile, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) setAccessMember(w http.ResponseWriter, r *http.Request) {
	var body struct{ Profile, Note string }
	readJSON(r, &body)
	if err := s.access.SetMember(r.PathValue("id"), body.Profile, body.Note, actor(r)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "access.assign", core.NormalizeSlackID(r.PathValue("id")), map[string]string{"profile": body.Profile})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteAccessMember(w http.ResponseWriter, r *http.Request) {
	if err := s.access.RemoveMember(r.PathValue("id")); err != nil {
		writeErr(w, 404, "not on the allowlist")
		return
	}
	s.st.Audit(actor(r), "access.remove", r.PathValue("id"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) saveAccessProfile(w http.ResponseWriter, r *http.Request) {
	var p core.Profile
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.access.SaveProfile(p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "access.profile", p.Name, p)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteAccessProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.access.DeleteProfile(r.PathValue("name")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.Audit(actor(r), "access.profile.delete", r.PathValue("name"), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- vault ----------

func (s *Server) getVault(w http.ResponseWriter, r *http.Request) {
	if s.hooks.VaultStatus == nil {
		writeJSON(w, 200, map[string]any{"status": map[string]any{"enabled": false}})
		return
	}
	writeJSON(w, 200, s.hooks.VaultStatus())
}

func (s *Server) syncVault(w http.ResponseWriter, r *http.Request) {
	if s.hooks.VaultSync == nil {
		writeErr(w, 400, "no vault configured")
		return
	}
	changed, err := s.hooks.VaultSync(r.Context())
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	s.st.Audit(actor(r), "vault.sync", "", map[string]any{"changed": changed})
	writeJSON(w, 200, map[string]any{"changed": changed, "restarting": len(changed) > 0})
}

// ---------- model endpoint ----------

func (s *Server) llmDetail() string {
	set := s.runner.Settings()
	base := firstNonEmpty(set.BaseURL, s.cfg.LLMBaseURL)
	style := map[string]string{llm.StyleOpenAI: "OpenAI API", llm.StyleAnthropic: "Anthropic API"}[set.APIStyle]
	return set.Model + " · " + host(base) + " · " + style
}

func (s *Server) llmModels(w http.ResponseWriter, r *http.Request) {
	if s.hooks.LLM == nil {
		writeErr(w, 500, "no model client")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ids, err := s.hooks.LLM.Models(ctx)
	if err != nil {
		writeErr(w, 502, "could not list models: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": ids})
}

// llmTest sends a tiny request with the current settings so a wrong model
// name, URL or API style shows up before anyone talks to the agent.
func (s *Server) llmTest(w http.ResponseWriter, r *http.Request) {
	if s.hooks.LLM == nil {
		writeErr(w, 500, "no model client")
		return
	}
	set := s.runner.Settings()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.hooks.LLM.Chat(ctx, llm.Request{Model: set.Model, MaxTokens: 32, Temperature: llm.F(0),
		Messages: []llm.Message{{Role: "system", Content: "You are a health check."}, {Role: "user", Content: "Reply with the single word: OK"}}})
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": core.Secrets.Redact(err.Error()), "model": set.Model, "api_style": set.APIStyle})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "reply": short(res.Message.Content, 200), "model": set.Model, "api_style": set.APIStyle,
		"latency_ms": time.Since(start).Milliseconds(), "tokens": res.Usage.PromptTokens + res.Usage.CompletionTokens})
}

// updateSelf rebuilds from source (optionally pulling SELF_REPO) in the
// background; progress and the outcome are streamed as "update" events.
func (s *Server) updateSelf(w http.ResponseWriter, r *http.Request) {
	var body struct{ Pull, Force bool }
	readJSON(r, &body)
	if !s.updating.CompareAndSwap(false, true) {
		writeErr(w, 409, "an update is already running")
		return
	}
	who := actor(r)
	go func() {
		defer s.updating.Store(false)
		publish := func(status, msg string) {
			s.bus.Publish("update", map[string]string{"status": status, "message": msg})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		res, err := s.self.Update(ctx, selfupdate.UpdateOptions{Pull: body.Pull, Force: body.Force, Actor: who,
			Log: func(line string) { publish("progress", line) }})
		switch {
		case errors.Is(err, selfupdate.ErrUpToDate):
			publish("uptodate", "Already up to date.")
		case err != nil:
			publish("failed", core.Secrets.Redact(err.Error()+"\n\n"+tailLines(res.BuildLog, 40)))
		default:
			publish("staged", "Built "+res.Commit+": restarting on the new version…")
		}
	}()
	writeJSON(w, 202, map[string]bool{"started": true})
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
