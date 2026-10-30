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
	blocks := []slack.Block{
		section(fmt.Sprintf("✋  *Approval needed*  ·  `%s`\n%s", a.Tool, escape(core.Secrets.Redact(a.Summary)))),
		section("```" + strings.ReplaceAll(args, "```", "ˋˋˋ") + "```"),
		contextLine(fmt.Sprintf("👤 Requested for *%s*", escape(a.RequestedBy)), "⏳ Expires in 30 min", "🛡️ "+who),
		slack.NewActionBlock("approval:"+a.ID,
			slack.NewButtonBlockElement("approve", a.ID, plain("✅ Approve")).WithStyle(slack.StylePrimary),
			slack.NewButtonBlockElement("deny", a.ID, plain("❌ Deny")).WithStyle(slack.StyleDanger),
			slack.NewButtonBlockElement("always", a.ID, plain("♾️ Always allow "+a.Tool)),
		),
	}
	opts := []slack.MsgOption{slack.MsgOptionBlocks(blocks...), slack.MsgOptionText("✋ Approval needed: "+core.Secrets.Redact(a.Summary), false)}
	if thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	ch, ts, err := b.API.PostMessageContext(context.Background(), channel, opts...)
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
	line := resolvedLine(a)
	b.API.UpdateMessageContext(context.Background(), channel, ts,
		slack.MsgOptionBlocks(contextLine(line)), slack.MsgOptionText(line, false))
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

func (b *Bot) ephemeral(ctx context.Context, cb slack.InteractionCallback, text string) {
	opts := []slack.MsgOption{slack.MsgOptionText(text, false), slack.MsgOptionBlocks(section(text))}
	if thread := cb.Message.ThreadTimestamp; thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	b.API.PostEphemeralContext(ctx, cb.Channel.ID, cb.User.ID, opts...)
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
		b.ephemeral(ctx, cb, "🙅 You're not allowed to decide on this action.")
		return
	}
	if err := b.approvals.Decide(id, act.ActionID, b.userName(ctx, cb.User.ID)); err != nil {
		b.ephemeral(ctx, cb, "⚠️ "+err.Error())
		return
	}
	b.ephemeral(ctx, cb, decidedText(act.ActionID, b.approvals.Tool(id)))
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
	blocks := []slack.Block{
		section(fmt.Sprintf("🔐  *%s*  ·  only visible to you", escape(ref))),
		section("```" + strings.ReplaceAll(value, "```", "ˋˋˋ") + "```"),
		contextLine("Ephemeral message: it disappears when Slack reloads. Don't paste it into a channel."),
	}
	opts := []slack.MsgOption{slack.MsgOptionText("🔐 "+ref+" (only visible to you)", false), slack.MsgOptionBlocks(blocks...)}
	if thread := firstNonEmpty(rc.ThreadTS, rc.MessageTS); thread != "" && !strings.HasPrefix(rc.Channel, "D") {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	_, err := b.API.PostEphemeralContext(context.Background(), rc.Channel, rc.UserID, opts...)
	return err
}
