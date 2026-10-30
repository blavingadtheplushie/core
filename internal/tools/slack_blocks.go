package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// jsonBlock passes a model-authored Block Kit block through untouched.
type jsonBlock map[string]any

func (b jsonBlock) BlockType() slack.MessageBlockType {
	t, _ := b["type"].(string)
	return slack.MessageBlockType(t)
}
func (b jsonBlock) ID() string { s, _ := b["block_id"].(string); return s }
func (b jsonBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(b))
}

// userActionPrefix namespaces every action_id of agent-authored blocks so a
// custom button can never trigger a built-in handler (approve/deny/always…).
const userActionPrefix = "blocks:"

// parseUserBlocks validates a Block Kit payload ([...] or {"blocks":[...]})
// and namespaces its action_ids.
func parseUserBlocks(raw string) ([]slack.Block, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("blocks is empty")
	}
	var list []map[string]any
	if strings.HasPrefix(raw, "{") {
		var wrap struct {
			Blocks []map[string]any `json:"blocks"`
		}
		if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		list = wrap.Blocks
	} else if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("invalid JSON (expected an array of blocks): %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("no blocks")
	}
	if len(list) > 50 {
		return nil, fmt.Errorf("%d blocks: Slack accepts at most 50 per message", len(list))
	}
	out := make([]slack.Block, 0, len(list))
	for i, b := range list {
		t, _ := b["type"].(string)
		if t == "" {
			return nil, fmt.Errorf("block %d has no \"type\"", i)
		}
		namespaceActions(b)
		out = append(out, jsonBlock(b))
	}
	return out, nil
}

func namespaceActions(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "action_id" {
				if s, ok := val.(string); ok && !strings.HasPrefix(s, userActionPrefix) {
					x[k] = userActionPrefix + s
				}
				continue
			}
			namespaceActions(val)
		}
	case []any:
		for _, e := range x {
			namespaceActions(e)
		}
	}
}

func registerSlackBlocks(d *Deps) {
	d.Reg.Add(&core.Tool{
		Name: "slack_post_blocks", Category: "slack", Emoji: "🧱", Available: d.slackOK,
		Description: "Posts a rich Block Kit message (header, section, image, context, divider, table, rich_text, actions with link buttons…) in a Slack channel or thread — defaults to the current conversation. Use it when someone wants a fancy layout or real buttons; normal answers stay plain mrkdwn. Buttons should be link buttons (\"url\"): clicks on non-link buttons do nothing. Max 50 blocks.",
		Params: core.Obj(map[string]any{
			"blocks":    core.Str("Block Kit JSON: an array of blocks, or {\"blocks\":[…]}."),
			"text":      core.Str("Plain-text fallback for notifications (required by Slack)."),
			"channel":   core.Str("Channel ID or #name (default: current conversation)."),
			"thread_ts": core.Str("Thread timestamp (default: current thread; \"none\" = top level)."),
		}, "blocks", "text"),
		Summarize: summarizeField("Blocks: ", "text", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Blocks   json.RawMessage `json:"blocks"`
				Text     string          `json:"text"`
				Channel  string          `json:"channel"`
				ThreadTS string          `json:"thread_ts"`
			}](args)
			if err != nil {
				return "", err
			}
			raw := string(a.Blocks)
			var s string
			if json.Unmarshal(a.Blocks, &s) == nil { // passed as a JSON string
				raw = s
			}
			blocks, err := parseUserBlocks(raw)
			if err != nil {
				return "", err
			}
			ch := rc.Channel
			if a.Channel != "" {
				if ch, err = d.resolveChannel(ctx, a.Channel); err != nil {
					return "", err
				}
			}
			if ch == "" {
				return "", errors.New("missing channel")
			}
			thread := a.ThreadTS
			if thread == "" && ch == rc.Channel {
				thread = firstNonEmpty(rc.ThreadTS, rc.MessageTS)
			}
			if thread == "none" {
				thread = ""
			}
			text := a.Text
			if text == "" {
				text = "Block Kit message"
			}
			opts := []slack.MsgOption{slack.MsgOptionText(text, false), slack.MsgOptionBlocks(blocks...)}
			if thread != "" {
				opts = append(opts, slack.MsgOptionTS(thread))
			}
			_, ts, err := d.Slack.PostMessageContext(ctx, ch, opts...)
			if err != nil && strings.Contains(err.Error(), "not_in_channel") {
				d.Slack.JoinConversationContext(ctx, ch)
				_, ts, err = d.Slack.PostMessageContext(ctx, ch, opts...)
			}
			if err != nil {
				return "", fmt.Errorf("slack refused the blocks: %w", err)
			}
			d.St.Audit(rc.Actor(), "slack.post_blocks", ch, map[string]any{"ts": ts, "blocks": len(blocks)})
			return fmt.Sprintf("Block Kit message posted (channel=%s ts=%s, %d blocks).", ch, ts, len(blocks)), nil
		},
	})
}
