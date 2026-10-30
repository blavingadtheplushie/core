package slackbot

import (
	"context"
	"log/slog"
	"strings"
	"time"

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

func (b *Bot) rememberFinal(runID string, f *finalState) {
	f.at = time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finals == nil {
		b.finals = map[string]*finalState{}
	}
	b.finals[runID] = f
	for id, old := range b.finals {
		if time.Since(old.at) > 15*time.Minute {
			delete(b.finals, id)
		}
	}
}

func (b *Bot) takeFinal(runID string) *finalState {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.finals[runID]
	delete(b.finals, runID)
	return f
}

// watchReflections completes the "Reflection" task of an answer's plan once
// the background reflection is done.
func (b *Bot) watchReflections(ctx context.Context) {
	ch, unsub := b.bus.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if ev.Type != "run.reflected" {
				continue
			}
			data, _ := ev.Data.(map[string]any)
			runID, _ := data["run_id"].(string)
			f := b.takeFinal(runID)
			if f == nil {
				continue
			}
			lines, _ := data["lines"].([]string)
			refl := reflectionEvent(true, true, lines)
			b.send(ctx, f.channel, f.ts, func(plan bool) []slack.Block {
				return f.live.finalBlocks(f.res, f.chunks, refl, plan)
			}, slack.MsgOptionText(short(f.res.Output, 300), false))
		}
	}
}
