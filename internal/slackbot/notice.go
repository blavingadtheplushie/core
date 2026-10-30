package slackbot

import (
	"context"
	"log/slog"
	"strings"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// planTask builds one task card of a plan block.
func planTask(id, title, status, details, output string, preDetails, preOutput bool) map[string]any {
	if status == "" {
		status = "complete"
	}
	t := map[string]any{"task_id": id, "title": clipRunes(title, 140), "status": status}
	if d := strings.TrimSpace(details); d != "" {
		t["details"] = richText(clipRunes(d, 1500), preDetails)
	}
	if o := strings.TrimSpace(output); o != "" {
		t["output"] = richText(clipRunes(o, 1200), preOutput)
	}
	return t
}

func planOf(title string, tasks ...any) slack.Block {
	return rawBlock{kind: "plan", body: map[string]any{"title": clipRunes(title, 140), "tasks": tasks}}
}

// noticeBlocks renders a system message in the same layout as answers.
func noticeBlocks(n core.Notice, plan bool) []slack.Block {
	task := n.Task
	if task == "" {
		task = n.Title
	}
	var blocks []slack.Block
	if plan {
		blocks = append(blocks, planOf(n.Title, planTask("t1", task, n.Status, "", n.Detail, false, false)))
	} else {
		line := "*" + escape(n.Title) + "*"
		if task != n.Title {
			line += " · " + escape(task)
		}
		if d := strings.TrimSpace(n.Detail); d != "" {
			line += "\n" + escape(clipRunes(d, 1200))
		}
		blocks = append(blocks, contextLine(line))
	}
	for _, c := range Chunk(ToMrkdwn(n.Body), finalChunkSize) {
		if len(blocks) >= 45 {
			break
		}
		blocks = append(blocks, section(c))
	}
	return blocks
}

func noticeText(n core.Notice) string {
	t := n.Title
	if n.Task != "" && n.Task != n.Title {
		t += " · " + n.Task
	}
	return short(t, 300)
}

// ephemeralNotice shows a notice to one person only, with the plan layout
// when the workspace accepts it.
func (b *Bot) ephemeralNotice(ctx context.Context, channel, user, thread string, n core.Notice) error {
	do := func(plan bool) error {
		opts := []slack.MsgOption{slack.MsgOptionText(noticeText(n), false), slack.MsgOptionBlocks(noticeBlocks(n, plan)...)}
		if thread != "" {
			opts = append(opts, slack.MsgOptionTS(thread))
		}
		_, err := b.API.PostEphemeralContext(ctx, channel, user, opts...)
		return err
	}
	err := do(b.plansOK())
	if err != nil && b.plansOK() && strings.Contains(err.Error(), "invalid_blocks") {
		slog.Warn("ephemeral plan rejected, using the classic layout", "err", err)
		err = do(false)
	}
	return err
}
