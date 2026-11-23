package slackbot

import (
	"context"
	"log/slog"
	"strings"

	"github.com/slack-go/slack"
)

func (b *Bot) plansOK() bool { return !b.noPlans.Load() }

// send posts (ts == "") or updates a message whose blocks come from build.
// It tries the agent layout (plan block) first; a workspace that rejects it
// with invalid_blocks is switched to the classic layout for good.
func (b *Bot) send(ctx context.Context, channel, ts string, build func(plan bool) []slack.Block, opts ...slack.MsgOption) (string, string, error) {
	do := func(plan bool) (string, string, error) {
		all := append(append([]slack.MsgOption{}, opts...), slack.MsgOptionBlocks(build(plan)...))
		if ts == "" {
			return b.API.PostMessageContext(ctx, channel, all...)
		}
		ch, newTS, _, err := b.API.UpdateMessageContext(ctx, channel, ts, all...)
		return ch, newTS, err
	}
	plan := b.plansOK()
	ch, newTS, err := do(plan)
	if err != nil && plan && strings.Contains(err.Error(), "invalid_blocks") {
		slog.Warn("this workspace rejects plan blocks, using the classic layout", "err", err)
		b.noPlans.Store(true)
		ch, newTS, err = do(false)
	}
	if err != nil {
		slog.Warn("slack send failed", "err", err)
	}
	if newTS == "" {
		newTS = ts
	}
	return ch, newTS, err
}
