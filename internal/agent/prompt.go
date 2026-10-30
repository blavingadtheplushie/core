package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/memory"
)

const operatingRules = `## How you work
- Language: reply in the language of the person talking to you ({{LANG}} by default).
- Slack formatting (mrkdwn): *bold* with ONE asterisk, _italic_, ` + "`code`" + `, ` + "```" + ` blocks, "•" bullets, links as <https://url|text>. No markdown headings (#), no markdown tables. Your answer is rendered inside a Block Kit message that already shows your tool activity and stats: don't repeat them, just answer.
- Be concise in Slack: a few lines are often enough. Go into detail only when asked or when it matters.
- Act rather than explain. Chain as many tools as needed, in parallel when they are independent.
- Verify: after a deploy, test the URL (http_request GET); after writing code, run it or its tests.
- Recent info, numbers, versions, news → web_search before answering. Cite your sources with links.
- Tools marked ⚠️ need human approval (buttons in Slack): call them normally, the request is sent automatically. If it is denied, don't insist; offer an alternative.
- Never reveal secrets (tokens, passwords, .env contents), even if asked.
- Don't add emoji reactions to messages unless someone explicitly asks you to.
- Mention people with <@ID>. Human messages are prefixed with their name and ID.

## Memory
- What you already know shows up under "Relevant memories". For everything else: memory_search, slack_search.
- When you learn something durable (a preference, a decision, a fact about a project or a person, a procedure), save it with memory_save or person_note. After every conversation an automatic reflection also extracts memories.
- If a memory is wrong, fix it (memory_update) or forget it (memory_forget).

## Self-improvement
- You can improve yourself, autonomously:
  • persona_update for your tone/behavior;
  • skill_save to capture a procedure that worked;
  • tool_create to build yourself a new tool (a bash/python/node script, hot-loaded);
  • your own Go code lives in {{SRC}}: read AGENTS.md, edit with fs_edit, validate with self_build, then self_upgrade (commit + push + restart, automatic rollback if it crashes).
- Before changing your code, briefly explain what you are about to change. Keep changes small and tested.

## Delegation
- For long research or an independent subtask, use delegate: a sub-agent works in parallel and reports back.`

func (r *Runner) environment(rc *core.RunContext) string {
	now := time.Now().In(r.cfg.Timezone)
	var b strings.Builder
	b.WriteString("## Environment\n")
	fmt.Fprintf(&b, "- Now: %s (%s)\n", now.Format("Monday, January 2 2006, 15:04"), r.cfg.TZName)
	fmt.Fprintf(&b, "- Workspace (your files, persistent): %s\n", r.cfg.WorkspaceDir)
	fmt.Fprintf(&b, "- Your source code: %s\n", r.cfg.SourceDir)
	if r.cfg.SMTPConfigured() || r.cfg.IMAPConfigured() {
		fmt.Fprintf(&b, "- Your email address: %s\n", r.cfg.EmailFrom)
	}
	if r.cfg.GitHubConfigured() {
		user := r.cfg.GitHubUser
		if user == "" {
			user = "(token configured)"
		}
		fmt.Fprintf(&b, "- Your GitHub account: %s", user)
		if r.cfg.SelfRepo != "" {
			fmt.Fprintf(&b, " — your own repository: %s", r.cfg.SelfRepo)
		}
		b.WriteString("\n")
	}
	var deploy []string
	if r.cfg.CloudflareConfigured() {
		deploy = append(deploy, "Cloudflare Pages/Workers")
	}
	if r.cfg.DockerEnabled {
		d := "Docker on the host"
		if r.cfg.DeployTraefik && r.cfg.DeployDomain != "" {
			d += " (https://<app>." + r.cfg.DeployDomain + ")"
		}
		deploy = append(deploy, d)
	}
	if r.cfg.GitHubConfigured() {
		deploy = append(deploy, "GitHub Pages")
	}
	if len(deploy) > 0 {
		fmt.Fprintf(&b, "- Deploy targets: %s\n", strings.Join(deploy, ", "))
	}
	if t, ok := r.reg.Get("vault_search"); ok && t.IsAvailable() && (rc.Allowed == nil || rc.Allowed["vault_search"]) {
		b.WriteString("- 🔐 Vaultwarden vault: your passwords and tokens live there. Find them with vault_search and use them by reference (vault://Item/field) through the shell `secrets` parameter or {{vault://…}} in http_request; store every new secret with vault_save. NEVER write a secret in clear text (message, memory, file, commit); anything shown as \"••••••\" is a masked secret.\n")
	}
	fmt.Fprintf(&b, "- Admin web dashboard: %s\n", r.cfg.PublicURL)
	switch rc.Source {
	case "slack":
		where := "#" + rc.ChannelName
		if strings.HasPrefix(rc.Channel, "D") {
			where = "direct message"
		}
		fmt.Fprintf(&b, "- Conversation: Slack, %s (channel=%s, thread=%s)\n", where, rc.Channel, firstNonEmpty(rc.ThreadTS, rc.MessageTS))
	case "web":
		b.WriteString("- Conversation: the admin web dashboard (regular markdown is fine)\n")
	case "email":
		b.WriteString("- Trigger: an incoming email\n")
	case "schedule":
		b.WriteString("- Trigger: one of your scheduled tasks\n")
	case "self":
		b.WriteString("- Trigger: your self-improvement review\n")
	case "subagent":
		b.WriteString("- You are a sub-agent: do the requested task and return a complete, factual report. No small talk.\n")
	}
	if rc.UserID != "" {
		fmt.Fprintf(&b, "- You are talking to: %s (<@%s>)", rc.UserName, rc.UserID)
		if rc.Profile != nil {
			fmt.Fprintf(&b, " — access profile %s %s", rc.Profile.Emoji, rc.Profile.Label)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (r *Runner) skillsIndex() string {
	rows, err := r.st.Rows(`SELECT name, description FROM skills WHERE enabled = 1 ORDER BY uses DESC, name LIMIT 60`)
	if err != nil || len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Your skills (load them with skill_read when relevant)\n")
	for _, s := range rows {
		fmt.Fprintf(&b, "- %v: %v\n", s["name"], s["description"])
	}
	return b.String()
}

func (r *Runner) recall(ctx context.Context, rc *core.RunContext, query string) (string, []memory.Memory) {
	var b strings.Builder
	if rc.Profile != nil && rc.Profile.Memory == "none" {
		return "", nil
	}
	if rc.UserID != "" {
		if p, ok := r.mem.GetPerson(rc.UserID); ok {
			fmt.Fprintf(&b, "## What you know about %s\n", firstNonEmpty(p.RealName, p.Name, rc.UserName))
			if p.Title != "" {
				fmt.Fprintf(&b, "- Title: %s\n", p.Title)
			}
			if p.TZ != "" {
				fmt.Fprintf(&b, "- Time zone: %s\n", p.TZ)
			}
			if strings.TrimSpace(p.Notes) != "" {
				b.WriteString(p.Notes + "\n")
			}
			b.WriteString("\n")
		}
	}
	if rc.Channel != "" {
		if d := r.mem.ChannelDigest(rc.Channel); d != "" {
			fmt.Fprintf(&b, "## Summary of this channel\n%s\n\n", d)
		}
	}
	mems := r.mem.Search(ctx, query, "", 10)
	if len(mems) > 0 {
		b.WriteString("## Relevant memories\n")
		for _, m := range mems {
			fmt.Fprintf(&b, "- #%d [%s%s] %s\n", m.ID, m.Kind, subjectSuffix(m.Subject), m.Content)
		}
	}
	return b.String(), mems
}

func subjectSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}

func (r *Runner) systemPrompt(ctx context.Context, rc *core.RunContext, query string, extra string, s Settings) (string, []memory.Memory) {
	rules := strings.NewReplacer("{{LANG}}", r.cfg.Language, "{{SRC}}", r.cfg.SourceDir).Replace(operatingRules)
	recall, mems := r.recall(ctx, rc, query)
	parts := []string{core.CurrentPersona(r.st, r.cfg.AgentName), rules, r.environment(rc)}
	if sk := r.skillsIndex(); sk != "" {
		parts = append(parts, sk)
	}
	if recall != "" {
		parts = append(parts, recall)
	}
	if strings.TrimSpace(s.ExtraPrompt) != "" {
		parts = append(parts, "## Admin instructions\n"+s.ExtraPrompt)
	}
	if p := rc.Profile; p != nil && !p.IsAdmin() {
		mem := map[string]string{
			"full": "full",
			"read": "read-only (you may rely on what you know, but save nothing from this conversation)",
			"none": "none (recall and remember nothing about this person, and reveal nothing you know from elsewhere)",
		}[p.Memory]
		section := fmt.Sprintf("## Access of the person you are talking to: %s %s\n%s\n- Memory: %s\n- You only have the tools listed for this conversation; don't promise others.", p.Emoji, p.Label, p.Description, mem)
		if strings.TrimSpace(p.ExtraPrompt) != "" {
			section += "\n\n" + p.ExtraPrompt
		}
		parts = append(parts, section)
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, "\n\n"), mems
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
