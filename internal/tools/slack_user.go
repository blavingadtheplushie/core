package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// Paul's Slack user token (xoxp) lives in the vault. Tools using it act AS
// that person, so they only run for the token's owner (checked with
// auth.test), never for anyone else, never from email or autonomous runs.
const slackUserRef = "vault://SLACK_USER_TOKEN_PAUL/password"

var (
	slackUserMu    sync.Mutex
	slackUserOwner = map[string]string{} // token -> owner user ID
)

// slackAsUser returns a Slack client authenticated as the token owner, after
// checking that the person behind the run IS that owner.
func (d *Deps) slackAsUser(ctx context.Context, rc *core.RunContext) (*slack.Client, error) {
	if rc.Source == "email" || rc.Source == "self" {
		return nil, errors.New("not available in autonomous runs")
	}
	if err := d.vaultAllowed(rc); err != nil {
		return nil, err
	}
	tok, err := d.resolveRef(ctx, slackUserRef)
	if err != nil || strings.TrimSpace(tok) == "" {
		if rerr := d.Vault.Refresh(ctx); rerr == nil {
			tok, err = d.resolveRef(ctx, slackUserRef)
		}
	}
	tok = strings.TrimSpace(tok)
	if err != nil || tok == "" {
		return nil, fmt.Errorf("no Slack user token: put it in the Password field of the vault item SLACK_USER_TOKEN_PAUL (%v)", err)
	}
	api := slack.New(tok)
	slackUserMu.Lock()
	owner := slackUserOwner[tok]
	slackUserMu.Unlock()
	if owner == "" {
		at, err := api.AuthTestContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("user token check failed: %v", err)
		}
		owner = at.UserID
		slackUserMu.Lock()
		slackUserOwner[tok] = owner
		slackUserMu.Unlock()
	}
	if rc.Source != "web" && rc.UserID != owner {
		return nil, errors.New("this tool acts as the token's owner: only that person can use it")
	}
	return api, nil
}

func registerSlackUser(d *Deps) {
	ok := func() bool { return d.vaultReady() }

	d.Reg.Add(&core.Tool{
		Name: "slack_search_as_paul", Category: "slack", Emoji: "🔎", Available: ok,
		Description: "Searches Slack messages AS PAUL (his user token): every public/private channel he is in, workspace-wide (search.messages). Only works when Paul himself asks. Supports Slack search modifiers in the query (in:#channel, from:@user, before:/after:/on:YYYY-MM-DD, has:link…). DMs are not covered. Results stay in the conversation where Paul asked.",
		Params: core.Obj(map[string]any{
			"query": core.Str("Slack search query (modifiers allowed)."),
			"sort":  core.Enum("Sort order (default score).", "score", "timestamp"),
			"count": core.Int("Results per page (default 20, max 100)."),
			"page":  core.Int("Page (default 1)."),
		}, "query"),
		Summarize: summarizeField("Searching as Paul: ", "query", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Query, Sort string
				Count, Page int
			}](args)
			if strings.TrimSpace(a.Query) == "" {
				return "", errors.New("empty query")
			}
			api, err := d.slackAsUser(ctx, rc)
			if err != nil {
				return "", err
			}
			p := slack.NewSearchParameters()
			if a.Sort == "timestamp" {
				p.Sort = "timestamp"
			}
			p.SortDirection = "desc"
			p.Count = 20
			if a.Count > 0 && a.Count <= 100 {
				p.Count = a.Count
			}
			if a.Page > 0 {
				p.Page = a.Page
			}
			res, err := api.SearchMessagesContext(ctx, a.Query, p)
			if err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "slack.search_as_user", a.Query, map[string]any{"results": res.Total})
			if len(res.Matches) == 0 {
				return "No results.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d results (page %d/%d):\n", res.Total, res.Paging.Page, res.Paging.Pages)
			for _, m := range res.Matches {
				who := m.Username
				if m.User != "" {
					if n := d.userName(ctx, m.User); n != "" {
						who = n
					}
				}
				ch := m.Channel.Name
				if ch == "" {
					ch = m.Channel.ID
				}
				fmt.Fprintf(&b, "[%s] #%s %s: %s  (channel=%s, ts=%s, link=%s)\n",
					tsTime(m.Timestamp), ch, who, trunc(m.Text, 600), m.Channel.ID, m.Timestamp, m.Permalink)
			}
			return Clip(b.String(), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_read_as_paul", Category: "slack", Emoji: "🧵", Available: ok,
		Description: "Reads a Slack thread (thread_ts) or the latest messages of a channel AS PAUL, including channels Blavingad is not in. Use after slack_search_as_paul. Only works when Paul himself asks.",
		Params: core.Obj(map[string]any{
			"channel":   core.Str("Channel ID (C…/G…)."),
			"thread_ts": core.Str("Parent message timestamp to read a whole thread (optional)."),
			"limit":     core.Int("Number of messages (default 30, max 200)."),
		}, "channel"),
		Summarize: summarizeField("Reading as Paul ", "channel", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Channel  string `json:"channel"`
				ThreadTS string `json:"thread_ts"`
				Limit    int    `json:"limit"`
			}](args)
			if a.Channel == "" {
				return "", errors.New("channel is required")
			}
			if a.Limit <= 0 || a.Limit > 200 {
				a.Limit = 30
			}
			api, err := d.slackAsUser(ctx, rc)
			if err != nil {
				return "", err
			}
			var msgs []slack.Message
			if a.ThreadTS != "" {
				msgs, _, _, err = api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{ChannelID: a.Channel, Timestamp: a.ThreadTS, Limit: a.Limit})
			} else {
				var res *slack.GetConversationHistoryResponse
				res, err = api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: a.Channel, Limit: a.Limit})
				if res != nil {
					msgs = res.Messages
					for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
						msgs[i], msgs[j] = msgs[j], msgs[i]
					}
				}
			}
			if err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "slack.read_as_user", a.Channel, map[string]any{"thread": a.ThreadTS, "messages": len(msgs)})
			if len(msgs) == 0 {
				return "No messages.", nil
			}
			return Clip(d.formatMessages(ctx, msgs), 0), nil
		},
	})
}
