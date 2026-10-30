package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/memory"
	"blavingad/internal/store"
)

const reflectPrompt = `You are the memory module of an AI agent named %s. Analyze the exchange below and extract ONLY what deserves to be remembered for the long run (still useful weeks from now):
- facts about projects, the team, the infrastructure, decisions made;
- explicit preferences of people (how they want to be answered, the tools they use…);
- lessons: a mistake the agent made and how to avoid it, or an approach that worked well;
- important dated events (X deployed at some URL, a release…).
Ignore small talk, short-lived info, anything already under "Already known", and secrets (tokens, passwords: NEVER).
Each memory must be a precise, self-contained sentence (with names, absolute dates, URLs).

Reply with strict JSON:
{"memories":[{"kind":"fact|preference|project|lesson|procedure|event|person","subject":"…","content":"…","importance":1-5,"tags":"a,b"}],
 "person_notes":[{"user_id":"U…","note":"…"}]}
Use empty lists if nothing is worth keeping. At most 6 memories.`

// reflect extracts durable memories from a finished run, then publishes
// run.reflected (with what was kept, possibly nothing) for the surfaces.
func (r *Runner) reflect(runID string, rc *core.RunContext, msgs []llm.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var reflected []string
	defer func() {
		r.bus.Publish("run.reflected", map[string]any{"run_id": runID, "lines": reflected})
	}()
	start := time.Now()
	var b strings.Builder
	for _, m := range msgs[1:] {
		switch {
		case m.Role == "user":
			fmt.Fprintf(&b, "USER (%s, %s): %s\n", rc.UserName, rc.UserID, clip(m.Content, 3000))
		case m.Role == "assistant" && m.Content != "":
			fmt.Fprintf(&b, "AGENT: %s\n", clip(m.Content, 2500))
		case m.Role == "assistant":
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "AGENT→%s %s\n", tc.Function.Name, clip(tc.Function.Arguments, 300))
			}
		case m.Role == "tool":
			fmt.Fprintf(&b, "  ↳ %s\n", clip(m.Content, 400))
		}
	}
	transcript := clip(b.String(), 24000)
	if len(transcript) < 80 {
		return
	}
	known := r.mem.Search(ctx, transcript[:min(len(transcript), 2000)], "", 12)
	var kb strings.Builder
	for _, m := range known {
		kb.WriteString("- " + m.Content + "\n")
	}
	s := r.Settings()
	resp, err := r.llm.Chat(ctx, llm.Request{Model: s.FastModel, JSONMode: true, Temperature: llm.F(0.1), MaxTokens: 1500, Messages: []llm.Message{
		{Role: "system", Content: fmt.Sprintf(reflectPrompt, r.cfg.AgentName)},
		{Role: "user", Content: "Already known:\n" + kb.String() + "\n\nExchange:\n" + transcript},
	}})
	if err != nil {
		// Some servers reject response_format; retry once without it.
		resp, err = r.llm.Chat(ctx, llm.Request{Model: s.FastModel, Temperature: llm.F(0.1), MaxTokens: 1500, Messages: []llm.Message{
			{Role: "system", Content: fmt.Sprintf(reflectPrompt, r.cfg.AgentName)},
			{Role: "user", Content: "Already known:\n" + kb.String() + "\n\nExchange:\n" + transcript},
		}})
		if err != nil {
			slog.Warn("reflection failed", "err", err)
			return
		}
	}
	var out struct {
		Memories    []memory.Memory `json:"memories"`
		PersonNotes []struct {
			UserID string `json:"user_id"`
			Note   string `json:"note"`
		} `json:"person_notes"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Message.Content)), &out); err != nil {
		slog.Warn("reflection parse failed", "err", err)
		return
	}
	saved := 0
	var lines []string
	for i, m := range out.Memories {
		if i >= 6 || strings.TrimSpace(m.Content) == "" {
			break
		}
		m.Source = "reflection:" + runID
		if res, err := r.mem.Save(ctx, m); err == nil {
			saved++
			verb := "+"
			if res.Merged {
				verb = "~"
			}
			lines = append(lines, fmt.Sprintf("%s #%d [%s] %s", verb, res.ID, m.Kind, m.Content))
		}
	}
	for _, pn := range out.PersonNotes {
		if pn.UserID != "" && pn.Note != "" {
			r.mem.AppendPersonNote(pn.UserID, pn.Note)
			lines = append(lines, "👤 <@"+pn.UserID+">: "+pn.Note)
		}
	}
	if len(lines) > 0 {
		r.step(runID, "reflect", "reflection", "", strings.Join(lines, "\n"), "ok", time.Since(start), resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
		r.bus.Publish("memory.updated", map[string]any{"run_id": runID, "count": saved})
	}
	reflected = lines
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	return s
}

// DigestChannel folds new messages into a rolling channel summary so the
// agent knows what a channel is about without re-reading it.
func (r *Runner) DigestChannel(ctx context.Context, channel string) error {
	var name, digest, until string
	r.st.DB.QueryRow(`SELECT COALESCE(name,''), digest, COALESCE(digest_until,'0') FROM channels WHERE id = ?`, channel).Scan(&name, &digest, &until)
	rows, err := r.st.DB.Query(`SELECT ts, COALESCE(user_name, user_id, '?'), text FROM slack_messages WHERE channel = ? AND ts > ? ORDER BY ts LIMIT 400`, channel, until)
	if err != nil {
		return err
	}
	var b strings.Builder
	last := until
	n := 0
	for rows.Next() {
		var ts, who, text string
		rows.Scan(&ts, &who, &text)
		fmt.Fprintf(&b, "%s: %s\n", who, clip(text, 600))
		last = ts
		n++
	}
	rows.Close()
	if n < 5 {
		return nil
	}
	s := r.Settings()
	resp, err := r.llm.Chat(ctx, llm.Request{Model: s.FastModel, Temperature: llm.F(0.2), MaxTokens: 900, Messages: []llm.Message{
		{Role: "system", Content: "You maintain the summary of a Slack channel for an AI agent. Merge the previous summary and the new messages into an up-to-date summary (at most 15 bullets): what the channel is about, ongoing projects and decisions, who does what, open questions. Drop what is outdated. No secrets."},
		{Role: "user", Content: "Channel #" + name + "\n\nPrevious summary:\n" + digest + "\n\nNew messages:\n" + clip(b.String(), 30000)},
	}})
	if err != nil {
		return err
	}
	_, err = r.st.DB.Exec(`UPDATE channels SET digest = ?, digest_until = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(resp.Message.Content), last, store.Now(), channel)
	return err
}

// SelfReview is the daily introspection run: the agent looks at its
// failures and feedback and improves itself autonomously.
func (r *Runner) SelfReview(ctx context.Context) Result {
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	var b strings.Builder
	runs := r.st.Count(`SELECT COUNT(*) FROM runs WHERE started_at > ? AND parent_id IS NULL`, since)
	fmt.Fprintf(&b, "Last 24 hours: %d conversations.\n\n", runs)
	if rows, err := r.st.Rows(`SELECT id, input, error FROM runs WHERE started_at > ? AND status = 'error' LIMIT 10`, since); err == nil && len(rows) > 0 {
		b.WriteString("### Failed runs\n")
		for _, x := range rows {
			fmt.Fprintf(&b, "- %v « %v » → %v\n", x["id"], short(fmt.Sprint(x["input"]), 120), short(fmt.Sprint(x["error"]), 200))
		}
	}
	if rows, err := r.st.Rows(`SELECT id, input, output FROM runs WHERE started_at > ? AND feedback < 0 LIMIT 10`, since); err == nil && len(rows) > 0 {
		b.WriteString("\n### Answers rated 👎\n")
		for _, x := range rows {
			fmt.Fprintf(&b, "- %v Q: %v\n  R: %v\n", x["id"], short(fmt.Sprint(x["input"]), 150), short(fmt.Sprint(x["output"]), 250))
		}
	}
	if rows, err := r.st.Rows(`SELECT name, COUNT(*) AS n, MAX(substr(output,1,200)) AS sample FROM run_steps WHERE kind = 'tool' AND status = 'error' AND created_at > ? GROUP BY name ORDER BY n DESC LIMIT 10`, since); err == nil && len(rows) > 0 {
		b.WriteString("\n### Failing tools\n")
		for _, x := range rows {
			fmt.Fprintf(&b, "- %v ×%v : %v\n", x["name"], x["n"], short(fmt.Sprint(x["sample"]), 180))
		}
	}
	if rows, err := r.st.Rows(`SELECT tool, summary FROM approvals WHERE created_at > ? AND status = 'denied' LIMIT 10`, since); err == nil && len(rows) > 0 {
		b.WriteString("\n### Actions denied by humans\n")
		for _, x := range rows {
			fmt.Fprintf(&b, "- %v : %v\n", x["tool"], x["summary"])
		}
	}
	task := b.String() + `

This is your daily self-improvement review. 🧬
1. Analyze these signals (dig into the runs with your tools if needed: memory_search, and self_info for your code).
2. Pick AT MOST 2 concrete, low-risk improvements: a lesson (memory_save kind=lesson), a skill (skill_save), a new tool (tool_create), a persona tweak, or a small fix to your code (fs_edit → self_build → self_upgrade) if a bug is clear.
3. If nothing significant stands out, change nothing.
4. End with a short Slack report (3-6 lines, emojis welcome) of what you improved and why.`
	return r.Run(ctx, Request{Source: "self", UserName: "self-review", Text: task, NoReflect: true})
}
