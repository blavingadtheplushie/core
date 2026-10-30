package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
	"blavingad/internal/store"
)

func (d *Deps) slackOK() bool { return d.Slack != nil }

func (d *Deps) userName(ctx context.Context, id string) string {
	if id == "" {
		return "?"
	}
	var name string
	if d.St.DB.QueryRow(`SELECT COALESCE(NULLIF(real_name,''), name) FROM people WHERE slack_id = ?`, id).Scan(&name) == nil && name != "" {
		return name
	}
	if d.Slack != nil {
		if u, err := d.Slack.GetUserInfoContext(ctx, id); err == nil {
			d.St.DB.Exec(`INSERT OR IGNORE INTO people(slack_id, name, real_name, title, email, tz, updated_at) VALUES(?,?,?,?,?,?,?)`,
				u.ID, u.Name, u.RealName, u.Profile.Title, u.Profile.Email, u.TZ, store.Now())
			if u.RealName != "" {
				return u.RealName
			}
			return u.Name
		}
	}
	return id
}

// resolveChannel accepts an ID, "#name" or "name".
func (d *Deps) resolveChannel(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("missing channel")
	}
	if strings.HasPrefix(ref, "<#") {
		ref = strings.Trim(strings.SplitN(ref[2:], "|", 2)[0], ">")
	}
	if len(ref) >= 9 && strings.ToUpper(ref) == ref && strings.ContainsAny(ref[:1], "CGD") {
		return ref, nil
	}
	name := strings.TrimPrefix(ref, "#")
	var id string
	if d.St.DB.QueryRow(`SELECT id FROM channels WHERE name = ?`, name).Scan(&id) == nil {
		return id, nil
	}
	cursor := ""
	for i := 0; i < 20; i++ {
		chans, next, err := d.Slack.GetConversationsContext(ctx, &slack.GetConversationsParameters{Cursor: cursor, Limit: 200, ExcludeArchived: true, Types: []string{"public_channel", "private_channel"}})
		if err != nil {
			return "", err
		}
		for _, c := range chans {
			if c.Name == name {
				return c.ID, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return "", fmt.Errorf("channel not found: %s", ref)
}

func tsTime(ts string) string {
	f, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return ts
	}
	return time.Unix(int64(f), 0).Format("2006-01-02 15:04")
}

func (d *Deps) formatMessages(ctx context.Context, msgs []slack.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		who := m.Username
		if m.User != "" {
			who = d.userName(ctx, m.User)
		}
		if who == "" {
			who = m.BotID
		}
		fmt.Fprintf(&b, "[%s] %s: %s  (ts=%s", tsTime(m.Timestamp), who, m.Text, m.Timestamp)
		if m.ThreadTimestamp != "" && m.ThreadTimestamp != m.Timestamp {
			fmt.Fprintf(&b, ", thread=%s", m.ThreadTimestamp)
		}
		if m.ReplyCount > 0 {
			fmt.Fprintf(&b, ", %d replies", m.ReplyCount)
		}
		for _, f := range m.Files {
			fmt.Fprintf(&b, ", file=%s", f.Name)
		}
		b.WriteString(")\n")
	}
	return b.String()
}

func registerSlack(d *Deps) {
	ok := d.slackOK

	d.Reg.Add(&core.Tool{
		Name: "slack_read_channel", Category: "slack", Emoji: "📖", Available: ok,
		Description: "Reads the latest messages of a Slack channel (chronological order). Accepts an ID or #name.",
		Params: core.Obj(map[string]any{
			"channel": core.Str("Channel ID or #name. Empty = current channel."),
			"limit":   core.Int("Number of messages (default 30, max 200)."),
			"oldest":  core.Str("Minimum Slack timestamp or ISO date."),
		}),
		Summarize: summarizeField("Reading ", "channel", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Channel, Oldest string
				Limit           int
			}](args)
			if a.Channel == "" {
				a.Channel = rc.Channel
			}
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			if a.Limit <= 0 || a.Limit > 200 {
				a.Limit = 30
			}
			if t, err := time.Parse("2006-01-02", a.Oldest); err == nil {
				a.Oldest = fmt.Sprint(t.Unix())
			} else if t, err := time.Parse(time.RFC3339, a.Oldest); err == nil {
				a.Oldest = fmt.Sprint(t.Unix())
			}
			res, err := d.Slack.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: ch, Limit: a.Limit, Oldest: a.Oldest})
			if err != nil {
				return "", err
			}
			msgs := res.Messages
			for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
				msgs[i], msgs[j] = msgs[j], msgs[i]
			}
			if len(msgs) == 0 {
				return "No messages.", nil
			}
			return Clip(d.formatMessages(ctx, msgs), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_read_thread", Category: "slack", Emoji: "🧵", Available: ok,
		Description: "Reads all replies in a Slack thread.",
		Params: core.Obj(map[string]any{
			"channel":   core.Str("Channel ID or #name."),
			"thread_ts": core.Str("Timestamp of the parent message."),
		}, "channel", "thread_ts"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Channel  string `json:"channel"`
				ThreadTS string `json:"thread_ts"`
			}](args)
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			msgs, _, _, err := d.Slack.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{ChannelID: ch, Timestamp: a.ThreadTS, Limit: 200})
			if err != nil {
				return "", err
			}
			return Clip(d.formatMessages(ctx, msgs), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_search", Category: "slack", Emoji: "🗂️",
		Description: "Full-text search across all the Slack messages you've read (your conversation memory). Optional filters by channel and author.",
		Params: core.Obj(map[string]any{
			"query":   core.Str("Keywords."),
			"channel": core.Str("Channel ID or #name (optional)."),
			"user":    core.Str("Author's Slack ID (optional)."),
			"limit":   core.Int("Max results (default 20)."),
		}, "query"),
		Summarize: summarizeField("Slack search: ", "query", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Query, Channel, User string
				Limit                int
			}](args)
			q := store.FTSQuery(a.Query)
			if q == "" {
				return "", errors.New("empty query")
			}
			if a.Limit <= 0 || a.Limit > 60 {
				a.Limit = 20
			}
			sqlq := `SELECT m.channel, COALESCE(c.name, m.channel), m.ts, COALESCE(m.thread_ts,''), COALESCE(m.user_name, m.user_id), m.text
				FROM slack_messages_fts f JOIN slack_messages m ON m.id = f.rowid LEFT JOIN channels c ON c.id = m.channel
				WHERE slack_messages_fts MATCH ?`
			params := []any{q}
			if a.Channel != "" && d.Slack != nil {
				if ch, err := d.resolveChannel(ctx, a.Channel); err == nil {
					sqlq += " AND m.channel = ?"
					params = append(params, ch)
				}
			}
			if a.User != "" {
				sqlq += " AND m.user_id = ?"
				params = append(params, a.User)
			}
			sqlq += " ORDER BY bm25(slack_messages_fts) LIMIT ?"
			params = append(params, a.Limit)
			rows, err := d.St.DB.Query(sqlq, params...)
			if err != nil {
				return "", err
			}
			defer rows.Close()
			var b strings.Builder
			n := 0
			for rows.Next() {
				var ch, chName, ts, thread, who, text string
				rows.Scan(&ch, &chName, &ts, &thread, &who, &text)
				fmt.Fprintf(&b, "[%s] #%s %s: %s  (channel=%s ts=%s", tsTime(ts), chName, who, short(text, 500), ch, ts)
				if thread != "" && thread != ts {
					fmt.Fprintf(&b, " thread=%s", thread)
				}
				b.WriteString(")\n")
				n++
			}
			if n == 0 {
				return "No messages found.", nil
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_post", Category: "slack", Emoji: "💬", Available: ok,
		Description: "Posts a message in a Slack channel (or thread). To answer the person talking to you, just reply normally: this tool is for writing ELSEWHERE.",
		Params: core.Obj(map[string]any{
			"channel":   core.Str("Channel ID or #name."),
			"text":      core.Str("Message (Slack mrkdwn)."),
			"thread_ts": core.Str("Thread timestamp (optional)."),
		}, "channel", "text"),
		Summarize: summarizeField("Message in ", "channel", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Channel  string `json:"channel"`
				Text     string `json:"text"`
				ThreadTS string `json:"thread_ts"`
			}](args)
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			opts := []slack.MsgOption{slack.MsgOptionText(a.Text, false)}
			if a.ThreadTS != "" {
				opts = append(opts, slack.MsgOptionTS(a.ThreadTS))
			}
			_, ts, err := d.Slack.PostMessageContext(ctx, ch, opts...)
			if err != nil {
				if strings.Contains(err.Error(), "not_in_channel") {
					d.Slack.JoinConversationContext(ctx, ch)
					_, ts, err = d.Slack.PostMessageContext(ctx, ch, opts...)
				}
				if err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("Message posted (channel=%s ts=%s).", ch, ts), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_dm", Category: "slack", Emoji: "✉️", Available: ok,
		Description: "Sends a direct message (DM) to a Slack user.",
		Params: core.Obj(map[string]any{
			"user": core.Str("User's Slack ID (e.g. U0123)."),
			"text": core.Str("Message."),
		}, "user", "text"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ User, Text string }](args)
			user := strings.Trim(strings.TrimPrefix(a.User, "<@"), ">")
			ch, _, _, err := d.Slack.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{user}})
			if err != nil {
				return "", err
			}
			_, ts, err := d.Slack.PostMessageContext(ctx, ch.ID, slack.MsgOptionText(a.Text, false))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("DM sent (channel=%s ts=%s).", ch.ID, ts), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_react", Category: "slack", Emoji: "😄", Available: ok,
		Description: "Adds an emoji reaction to a Slack message.",
		Params: core.Obj(map[string]any{
			"channel": core.Str("Channel ID (empty = current)."),
			"ts":      core.Str("Message timestamp (empty = current message)."),
			"emoji":   core.Str("Emoji name without colons (e.g. tada)."),
		}, "emoji"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Channel, Ts, Emoji string }](args)
			if a.Channel == "" {
				a.Channel = rc.Channel
			}
			if a.Ts == "" {
				a.Ts = rc.MessageTS
			}
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			err = d.Slack.AddReactionContext(ctx, strings.Trim(a.Emoji, ":"), slack.ItemRef{Channel: ch, Timestamp: a.Ts})
			if err != nil && !strings.Contains(err.Error(), "already_reacted") {
				return "", err
			}
			return "Reaction added.", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_delete_message", Category: "slack", Emoji: "🗑️", Risk: core.RiskSensitive, Available: ok,
		Description: "Deletes a Slack message posted by the bot (needs a token with chat:write). Only deletes the message whose channel and timestamp are given.",
		Params: core.Obj(map[string]any{
			"channel": core.Str("Channel ID (C…) where the message was posted."),
			"ts":      core.Str("Timestamp of the message to delete (e.g. 1793325976.730319)."),
		}, "channel", "ts"),
		Summarize: summarizeField("Delete message in ", "channel", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Channel string `json:"channel"`
				Ts      string `json:"ts"`
			}](args)
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			if a.Ts == "" {
				return "", errors.New("missing ts")
			}
			if _, _, err := d.Slack.DeleteMessageContext(ctx, ch, a.Ts); err != nil {
				return "", err
			}
			return fmt.Sprintf("Message %s deleted from %s.", a.Ts, ch), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_channels", Category: "slack", Emoji: "📋", Available: ok,
		Description: "Lists Slack channels (name, ID, members, topic, whether you're a member).",
		Params:      core.Obj(map[string]any{"query": core.Str("Name filter (optional).")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			q := strings.ToLower(field(args, "query"))
			var b strings.Builder
			cursor := ""
			for i := 0; i < 10; i++ {
				chans, next, err := d.Slack.GetConversationsContext(ctx, &slack.GetConversationsParameters{Cursor: cursor, Limit: 200, ExcludeArchived: true, Types: []string{"public_channel", "private_channel"}})
				if err != nil {
					return "", err
				}
				for _, c := range chans {
					if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
						continue
					}
					member := ""
					if c.IsMember {
						member = " ✅ member"
					}
					fmt.Fprintf(&b, "#%s (%s) — %d members%s — %s\n", c.Name, c.ID, c.NumMembers, member, short(c.Topic.Value, 80))
				}
				if next == "" {
					break
				}
				cursor = next
			}
			return Clip(b.String(), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_join", Category: "slack", Emoji: "🚪", Available: ok,
		Description: "Joins a public Slack channel so you can read and write in it.",
		Params:      core.Obj(map[string]any{"channel": core.Str("ID or #name.")}, "channel"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			ch, err := d.resolveChannel(ctx, field(args, "channel"))
			if err != nil {
				return "", err
			}
			if _, _, _, err := d.Slack.JoinConversationContext(ctx, ch); err != nil {
				return "", err
			}
			return "Joined channel: " + ch, nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_user", Category: "slack", Emoji: "🪪", Available: ok,
		Description: "Info about a Slack user: name, title, timezone, email, and your notes about them.",
		Params:      core.Obj(map[string]any{"user": core.Str("Slack ID (U…).")}, "user"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			id := strings.Trim(strings.TrimPrefix(field(args, "user"), "<@"), ">")
			u, err := d.Slack.GetUserInfoContext(ctx, id)
			if err != nil {
				return "", err
			}
			p, _ := d.Mem.GetPerson(id)
			return JSON(map[string]any{"id": u.ID, "name": u.Name, "real_name": u.RealName, "title": u.Profile.Title, "email": u.Profile.Email,
				"tz": u.TZ, "is_admin": u.IsAdmin, "is_bot": u.IsBot, "status": u.Profile.StatusText + " " + u.Profile.StatusEmoji, "mes_notes": p.Notes}), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "slack_upload", Category: "slack", Emoji: "📎", Available: ok,
		Description: "Uploads a workspace file to Slack (current channel by default).",
		Params: core.Obj(map[string]any{
			"path":      core.Str("File path."),
			"channel":   core.Str("Channel (empty = current)."),
			"thread_ts": core.Str("Thread (empty = current thread)."),
			"title":     core.Str("Title."),
			"comment":   core.Str("Message to go with the file."),
		}, "path"),
		Summarize: summarizeField("Uploading file ", "path", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Path, Channel, Title, Comment string
				ThreadTS                      string `json:"thread_ts"`
			}](args)
			p, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			fi, err := os.Stat(p)
			if err != nil {
				return "", err
			}
			if a.Channel == "" {
				a.Channel = rc.Channel
				if a.ThreadTS == "" {
					a.ThreadTS = firstNonEmpty(rc.ThreadTS, rc.MessageTS)
				}
			}
			ch, err := d.resolveChannel(ctx, a.Channel)
			if err != nil {
				return "", err
			}
			name := fi.Name()
			if a.Title == "" {
				a.Title = name
			}
			_, err = d.Slack.UploadFileContext(ctx, slack.UploadFileParameters{File: p, FileSize: int(fi.Size()), Filename: name, Title: a.Title, Channel: ch, ThreadTimestamp: a.ThreadTS, InitialComment: a.Comment})
			if err != nil {
				return "", err
			}
			return "File uploaded: " + name, nil
		},
	})
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
