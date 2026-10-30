package slackbot

import (
	"context"
	"log/slog"
	"strings"

	"github.com/slack-go/slack"
)

// Slack "agent messaging experience" (manifest features.agent_view): the bot
// lives in the Messages tab, each DM message opens a thread, a status shows
// while it works and suggested prompts sit at the top of the tab. Every call
// here is best-effort: on a plain app (no agent_view) Slack rejects them and
// the classic behaviour remains.

var agentPrompts = []slack.AssistantThreadsPrompt{
	{Title: "What can you do?", Message: "What can you do for me?"},
	{Title: "Summarize a channel", Message: "Summarize what happened this week in #general."},
	{Title: "Build and deploy a site", Message: "Build me a small static site and deploy it."},
	{Title: "Check my emails", Message: "Any new emails in your mailbox?"},
}

// agentWelcome sets the suggested prompts when someone opens the DM.
func (b *Bot) agentWelcome(ctx context.Context, channel string) {
	if channel == "" {
		return
	}
	err := b.API.SetAssistantThreadsSuggestedPromptsContext(ctx, slack.AssistantThreadsSetSuggestedPromptsParameters{
		ChannelID: channel, Prompts: agentPrompts,
	})
	if err != nil {
		slog.Debug("agent suggested prompts unavailable", "err", err)
	}
}

// agentThinking shows the native "is thinking…" status in a DM thread (it
// also opens the thread) and titles new threads after the first message.
func (b *Bot) agentThinking(ctx context.Context, channel, thread string, first bool, text string) {
	if thread == "" {
		return
	}
	if err := b.API.SetAssistantThreadsStatusContext(ctx, slack.AssistantThreadsSetStatusParameters{
		ChannelID: channel, ThreadTS: thread, Status: "is thinking…",
		LoadingMessages: []string{"is thinking…", "is waving eight arms…", "is reading the currents…"},
	}); err != nil {
		slog.Debug("agent status unavailable", "err", err)
		return
	}
	if first {
		if title := threadTitle(b.cleanText(text)); title != "" {
			b.API.SetAssistantThreadsTitleContext(ctx, slack.AssistantThreadsSetTitleParameters{ChannelID: channel, ThreadTS: thread, Title: title})
		}
	}
}

func threadTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return clipRunes(text, 60)
}
