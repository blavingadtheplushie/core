package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// NotifyApproval implements core.ApprovalNotifier with a Block Kit card.
func (b *Bot) NotifyApproval(a *core.Approval) (string, string) {
	channel, thread := a.Channel, a.ThreadTS
	if channel == "" || strings.HasPrefix(channel, "web") {
		channel, thread = b.runner.Settings().HomeChannel, ""
	}
	if channel == "" {
		return "", ""
	}
	args := a.Args
	var pretty any
	if json.Unmarshal([]byte(args), &pretty) == nil {
		if bts, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			args = string(bts)
		}
	}
	args = core.Secrets.Redact(args)
	if len(args) > 2500 {
		args = args[:2500] + "\n…"
	}
	who := "Admins can approve."
	if a.RequesterID != "" {
		if p, _ := b.access.Resolve(a.RequesterID); p.Approve == "own" {
			who = "Admins or the requester can approve."
		}
	}
	summary := core.Secrets.Redact(a.Summary)
	meta := "Requested for " + a.RequestedBy + " · expires in 30 min · " + who
	buttons := slack.NewActionBlock("approval:"+a.ID,
		slack.NewButtonBlockElement("approve", a.ID, plain("Approve")).WithStyle(slack.StylePrimary),
		slack.NewButtonBlockElement("deny", a.ID, plain("Deny")).WithStyle(slack.StyleDanger),
		slack.NewButtonBlockElement("always", a.ID, plain("Always allow "+a.Tool)),
	)
	build := func(plan bool) []slack.Block {
		if plan {
			return []slack.Block{
				planOf("Approval needed", planTask("t1", a.Tool+" · "+summary, "in_progress", args, meta, true, false)),
				buttons,
			}
		}
		return []slack.Block{
			section(fmt.Sprintf("*Approval needed* · `%s`\n%s", a.Tool, escape(summary))),
			section("```" + strings.ReplaceAll(args, "```", "ˋˋˋ") + "```"),
			contextLine(escape(meta)),
			buttons,
		}
	}
	opts := []slack.MsgOption{slack.MsgOptionText("Approval needed: "+summary, false)}
	if thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	ch, ts, err := b.send(context.Background(), channel, "", build, opts...)
	if err != nil && b.plansOK() {
		// An approval must never get lost: retry once with the classic card.
		ch, ts, err = b.API.PostMessageContext(context.Background(), channel, append(opts, slack.MsgOptionBlocks(build(false)...))...)
	}
	if err != nil {
		return "", ""
	}
	return ch, ts
}

// ResolveApproval removes the approval card once it is decided: the decider
// gets a short ephemeral confirmation instead (see decide). Expired cards, or
// cards Slack refuses to delete, shrink to one sober context line.
func (b *Bot) ResolveApproval(a *core.Approval, channel, ts string) {
	if a.Status == "approved" || a.Status == "denied" {
		if _, _, err := b.API.DeleteMessageContext(context.Background(), channel, ts); err == nil {
			return
		}
	}
	n := resolvedNotice(a)
	b.send(context.Background(), channel, ts, func(plan bool) []slack.Block {
		if !plan {
			return []slack.Block{contextLine(resolvedLine(a))}
		}
		return noticeBlocks(n, true)
	}, slack.MsgOptionText(noticeText(n), false))
}

// resolvedNotice is the plan version of resolvedLine.
func resolvedNotice(a *core.Approval) core.Notice {
	label, ok := map[string]string{"approved": "Approved", "denied": "Denied", "expired": "Expired"}[a.Status]
	if !ok {
		label = a.Status
	}
	n := core.Notice{Title: "Approval", Task: label + " · " + a.Tool, Status: "complete"}
	if a.Status == "expired" {
		n.Status = "error"
		n.Detail = "Nobody answered within 30 min."
	}
	if a.DecidedBy != "" {
		n.Detail = "By " + a.DecidedBy
	}
	return n
}

// resolvedLine is the one-line, emoji-free status of a decided approval.
func resolvedLine(a *core.Approval) string {
	label, ok := map[string]string{"approved": "Approved", "denied": "Denied", "expired": "Expired"}[a.Status]
	if !ok {
		label = a.Status
	}
	line := label + " · `" + a.Tool + "`"
	if a.DecidedBy != "" {
		line += " · " + escape(a.DecidedBy)
	}
	return line
}

func (b *Bot) ephemeral(ctx context.Context, cb slack.InteractionCallback, n core.Notice) {
	b.ephemeralNotice(ctx, cb.Channel.ID, cb.User.ID, cb.Message.ThreadTimestamp, n)
}

func (b *Bot) onInteraction(ctx context.Context, cb slack.InteractionCallback) {
	if cb.Type != slack.InteractionTypeBlockActions {
		return
	}
	for _, act := range cb.ActionCallback.BlockActions {
		switch act.ActionID {
		case "approve", "deny", "always":
			b.decide(ctx, cb, act)
		}
	}
}

func (b *Bot) decide(ctx context.Context, cb slack.InteractionCallback, act *slack.BlockAction) {
	id := act.Value
	clicker, _ := b.access.Resolve(cb.User.ID)
	allowed := clicker.CanApprove(cb.User.ID, b.approvals.Requester(id))
	if act.ActionID == "always" && clicker.Approve != "any" {
		allowed = false // changing a tool policy is an admin decision
	}
	if !allowed {
		b.ephemeral(ctx, cb, core.Notice{Title: "Approval", Task: "Not allowed", Status: "error", Detail: "Your access profile can't decide on this action."})
		return
	}
	if err := b.approvals.Decide(id, act.ActionID, b.userName(ctx, cb.User.ID)); err != nil {
		b.ephemeral(ctx, cb, core.Notice{Title: "Approval", Task: "Not recorded", Status: "error", Detail: err.Error()})
		return
	}
	b.ephemeral(ctx, cb, core.Notice{Title: "Approval", Task: strings.ReplaceAll(decidedText(act.ActionID, b.approvals.Tool(id)), "`", "")})
}

// decidedText is the sober ephemeral confirmation sent to whoever clicked.
func decidedText(action, tool string) string {
	label := "Denied"
	switch action {
	case "approve":
		label = "Approved"
	case "always":
		label = "Approved (always allowed from now on)"
	}
	if tool == "" {
		return label
	}
	return label + " · `" + tool + "`"
}

func (b *Bot) publishHome(ctx context.Context, user string) {
	runs := b.st.Count(`SELECT COUNT(*) FROM runs WHERE parent_id IS NULL`)
	mems := b.st.Count(`SELECT COUNT(*) FROM memories`)
	tools := b.st.Count(`SELECT COUNT(*) FROM custom_tools`)
	deploys := b.st.Count(`SELECT COUNT(*) FROM deployments WHERE status = 'live'`)
	recent, _ := b.st.Rows(`SELECT input, status FROM runs WHERE user_id = ? AND parent_id IS NULL ORDER BY started_at DESC LIMIT 5`, user)
	var lines []string
	for _, r := range recent {
		icon := "✅"
		if r["status"] != "done" {
			icon = "⚠️"
		}
		in := fmt.Sprint(r["input"])
		if i := strings.Index(in, "): "); i > 0 && i < 100 {
			in = in[i+3:]
		}
		lines = append(lines, icon+" "+escape(short(in, 90)))
	}
	recentText := "_Nothing yet — say hi!_"
	if len(lines) > 0 {
		recentText = strings.Join(lines, "\n")
	}
	prof, listed := b.access.Resolve(user)
	accessText := fmt.Sprintf("*Your access:* %s %s — %s", prof.Emoji, prof.Label, prof.Description)
	if !listed {
		accessText += "\n_You're not on my whitelist: ask an admin if you need more._"
	}
	blocks := []slack.Block{
		slack.NewHeaderBlock(plain("🤖 " + b.cfg.AgentName)),
		section("Your resident AI agent. Mention me in a channel, DM me, or reply in a thread I'm part of. I can search the web, write code, deploy, send emails — and I improve myself. 🧬"),
		slack.NewSectionBlock(nil, []*slack.TextBlockObject{
			mrkdwn(fmt.Sprintf("*💬 Conversations*\n%d", runs)),
			mrkdwn(fmt.Sprintf("*🧠 Memories*\n%d", mems)),
			mrkdwn(fmt.Sprintf("*🛠️ Tools I built*\n%d", tools)),
			mrkdwn(fmt.Sprintf("*🚀 Live deployments*\n%d", deploys)),
		}, nil),
		slack.NewDividerBlock(),
		section(accessText),
		section("*Your recent requests*\n" + recentText),
	}
	if b.cfg.PublicURL != "" {
		blocks = append(blocks, slack.NewActionBlock("home",
			slack.NewButtonBlockElement("open_web", "web", plain("🖥️ Open the dashboard")).WithURL(b.cfg.PublicURL)))
	}
	b.API.PublishViewContext(ctx, slack.PublishViewContextRequest{UserID: user, View: slack.HomeTabViewRequest{Type: slack.VTHomeTab, Blocks: slack.Blocks{BlockSet: blocks}}})
}

// RevealSecret shows a vault value to the requesting human only (ephemeral
// message). It never goes through the model, the database or the logs.
func (b *Bot) RevealSecret(rc *core.RunContext, ref, value string) error {
	if rc.Channel == "" || strings.HasPrefix(rc.Channel, "web") || rc.UserID == "" {
		return fmt.Errorf("no Slack conversation to send it to")
	}
	note := "Ephemeral message: it disappears when Slack reloads. Don't paste it into a channel."
	build := func(plan bool) []slack.Block {
		if plan {
			return []slack.Block{planOf("Secret · only visible to you", planTask("t1", ref, "complete", note, value, false, true))}
		}
		return []slack.Block{
			section(fmt.Sprintf("*%s* · only visible to you", escape(ref))),
			section("```" + strings.ReplaceAll(value, "```", "ˋˋˋ") + "```"),
			contextLine(note),
		}
	}
	thread := firstNonEmpty(rc.ThreadTS, rc.MessageTS)
	if strings.HasPrefix(rc.Channel, "D") {
		thread = ""
	}
	do := func(plan bool) error {
		opts := []slack.MsgOption{slack.MsgOptionText(ref+" (only visible to you)", false), slack.MsgOptionBlocks(build(plan)...)}
		if thread != "" {
			opts = append(opts, slack.MsgOptionTS(thread))
		}
		_, err := b.API.PostEphemeralContext(context.Background(), rc.Channel, rc.UserID, opts...)
		return err
	}
	err := do(b.plansOK())
	if err != nil && b.plansOK() {
		err = do(false)
	}
	return err
}
