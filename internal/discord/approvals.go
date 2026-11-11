package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/store"
	"blavingad/internal/tools"
)

// Approver mirrors approval prompts into the Discord DMs of every linked
// Discord user whose (Slack) access profile may decide them, with buttons.
// Slack keeps its own card; whichever surface answers first wins.
type Approver struct {
	St        *store.Store
	Access    *core.Access
	Approvals *core.Approvals

	mu   sync.Mutex
	sent map[string][]dmMsg // approval id → messages posted
}

type dmMsg struct{ channel, id string }

func NewApprover(b *Bridge, ap *core.Approvals) *Approver {
	a := &Approver{St: b.St, Access: b.Access, Approvals: ap, sent: map[string][]dmMsg{}}
	b.approver = a
	return a
}

// approvers lists linked Discord users allowed to decide this approval.
func (a *Approver) approvers(ap *core.Approval) []struct {
	discordID string
	admin     bool
} {
	var out []struct {
		discordID string
		admin     bool
	}
	for did, slackID := range Links(a.St) {
		if slackID == "" {
			continue
		}
		p, _ := a.Access.Resolve(slackID)
		if p.CanApprove(slackID, ap.RequesterID) {
			out = append(out, struct {
				discordID string
				admin     bool
			}{did, p.Approve == "any"})
		}
	}
	return out
}

func approvalText(ap *core.Approval) string {
	args := ap.Args
	var pretty any
	if json.Unmarshal([]byte(args), &pretty) == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			args = string(b)
		}
	}
	args = core.Secrets.Redact(args)
	if r := []rune(args); len(r) > 1400 {
		args = string(r[:1400]) + "\n…"
	}
	args = strings.ReplaceAll(args, "```", "ˋˋˋ")
	return fmt.Sprintf("**Approval needed** · `%s`\n%s\n```json\n%s\n```\n-# Requested for %s · expires in 30 min",
		ap.Tool, short(core.Secrets.Redact(ap.Summary), 300), args, short(ap.RequestedBy, 80))
}

func buttons(id, tool string, admin bool) []any {
	row := []map[string]any{
		{"type": 2, "style": 3, "label": "Approve", "custom_id": "apr:approve:" + id},
		{"type": 2, "style": 4, "label": "Deny", "custom_id": "apr:deny:" + id},
	}
	if admin {
		row = append(row, map[string]any{"type": 2, "style": 2, "label": short("Always allow "+tool, 70), "custom_id": "apr:always:" + id})
	}
	return []any{map[string]any{"type": 1, "components": row}}
}

// NotifyApproval implements core.ApprovalNotifier.
func (a *Approver) NotifyApproval(ap *core.Approval) (string, string) {
	if !tools.DiscordState.Connected.Load() {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	text := approvalText(ap)
	var msgs []dmMsg
	for _, u := range a.approvers(ap) {
		ch, err := tools.DiscordDMChannel(ctx, u.discordID)
		if err != nil {
			slog.Warn("discord: approval DM channel", "user", u.discordID, "err", err)
			continue
		}
		raw, err := tools.DiscordDo(ctx, "POST", "/channels/"+ch+"/messages", map[string]any{
			"content": text, "components": buttons(ap.ID, ap.Tool, u.admin),
			"allowed_mentions": map[string]any{"parse": []string{}},
		})
		if err != nil {
			slog.Warn("discord: approval DM", "user", u.discordID, "err", err)
			continue
		}
		var m struct {
			ID string `json:"id"`
		}
		json.Unmarshal(raw, &m)
		msgs = append(msgs, dmMsg{ch, m.ID})
	}
	if len(msgs) == 0 {
		return "", ""
	}
	a.mu.Lock()
	a.sent[ap.ID] = msgs
	a.mu.Unlock()
	return "discord", ap.ID
}

func resolvedText(ap *core.Approval) string {
	label := map[string]string{"approved": "Approved", "denied": "Denied", "expired": "Expired"}[ap.Status]
	if label == "" {
		label = ap.Status
	}
	s := fmt.Sprintf("%s · `%s` · %s", label, ap.Tool, short(core.Secrets.Redact(ap.Summary), 200))
	if ap.DecidedBy != "" {
		s += "\n-# By " + short(ap.DecidedBy, 80)
	} else if ap.Status == "expired" {
		s += "\n-# Nobody answered within 30 min."
	}
	return s
}

// ResolveApproval implements core.ApprovalNotifier: buttons go away.
func (a *Approver) ResolveApproval(ap *core.Approval, _, _ string) {
	a.mu.Lock()
	msgs := a.sent[ap.ID]
	delete(a.sent, ap.ID)
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, m := range msgs {
		tools.DiscordDo(ctx, "PATCH", "/channels/"+m.channel+"/messages/"+m.id, map[string]any{
			"content": resolvedText(ap), "components": []any{},
		})
	}
}

// handleButton processes an apr:<action>:<id> component click.
func (b *Bridge) handleButton(ctx context.Context, interactionID, token, customID string, u user) {
	reply := func(text string) {
		tools.DiscordDo(ctx, "POST", "/interactions/"+interactionID+"/"+token+"/callback",
			map[string]any{"type": 4, "data": map[string]any{"content": text, "flags": 64}})
	}
	parts := strings.SplitN(customID, ":", 3)
	if len(parts) != 3 || b.approver == nil {
		reply("Unknown action.")
		return
	}
	action, id := parts[1], parts[2]
	ap := b.approver.Approvals
	slackID, prof := b.profile(u)
	allowed := slackID != "" && prof.CanApprove(slackID, ap.Requester(id))
	if action == "always" && prof.Approve != "any" {
		allowed = false
	}
	if !allowed {
		reply("Your access profile can't decide on this action.")
		return
	}
	if err := ap.Decide(id, action, u.display()+" (Discord)"); err != nil {
		reply("Not recorded: " + err.Error())
		return
	}
	label := map[string]string{"approve": "Approved", "deny": "Denied", "always": "Approved (always allowed from now on)"}[action]
	// Update the clicked message right away (ResolveApproval edits it too).
	tools.DiscordDo(ctx, "POST", "/interactions/"+interactionID+"/"+token+"/callback",
		map[string]any{"type": 7, "data": map[string]any{"content": label + " · `" + ap.Tool(id) + "`\n-# By " + short(u.display(), 80), "components": []any{}}})
}
