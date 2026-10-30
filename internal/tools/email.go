package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"blavingad/internal/core"
	"blavingad/internal/mail"
)

func splitAddrs(s []string) []string {
	var out []string
	for _, a := range s {
		for _, p := range strings.Split(a, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func registerEmail(d *Deps) {
	inOK := func() bool { return d.Mail != nil && d.Cfg.IMAPConfigured() }
	outOK := func() bool { return d.Mail != nil && d.Cfg.SMTPConfigured() }

	d.Reg.Add(&core.Tool{
		Name: "email_list", Category: "email", Emoji: "📬", Available: func() bool { return d.Mail != nil && (inOK() || outOK()) },
		Description: "Lists the latest emails in your mailbox (" + d.Cfg.EmailFrom + "), received and sent. refresh=true checks the mailbox first.",
		Params: core.Obj(map[string]any{
			"query":     core.Str("Text filter on sender/subject/body (optional)."),
			"direction": core.Enum("Received or sent.", "in", "out"),
			"limit":     core.Int("Max (default 15)."),
			"refresh":   core.Bool("Check the IMAP mailbox first."),
		}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Query, Direction string
				Limit            int
				Refresh          bool
			}](args)
			if a.Refresh && inOK() {
				if _, err := d.Mail.Fetch(ctx); err != nil {
					return "", err
				}
			}
			if a.Limit <= 0 || a.Limit > 50 {
				a.Limit = 15
			}
			q := `SELECT id, direction, from_addr, to_addr, subject, substr(body, 1, 160), created_at FROM emails WHERE 1=1`
			var params []any
			if a.Direction != "" {
				q += " AND direction = ?"
				params = append(params, a.Direction)
			}
			if a.Query != "" {
				q += " AND (from_addr LIKE ? OR subject LIKE ? OR body LIKE ? OR to_addr LIKE ?)"
				like := "%" + a.Query + "%"
				params = append(params, like, like, like, like)
			}
			q += " ORDER BY id DESC LIMIT ?"
			params = append(params, a.Limit)
			rows, err := d.St.DB.Query(q, params...)
			if err != nil {
				return "", err
			}
			defer rows.Close()
			var b strings.Builder
			for rows.Next() {
				var id int64
				var dir, from, to, subj, preview, at string
				rows.Scan(&id, &dir, &from, &to, &subj, &preview, &at)
				arrow := "📥 from " + from
				if dir == "out" {
					arrow = "📤 to " + to
				}
				fmt.Fprintf(&b, "#%d %s %s — \"%s\"\n   %s\n", id, strings.Split(at, ".")[0], arrow, subj, short(preview, 150))
			}
			if b.Len() == 0 {
				return "Mailbox is empty.", nil
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "email_read", Category: "email", Emoji: "📨", Available: func() bool { return d.Mail != nil },
		Description: "Reads a full email by its #id.",
		Params:      core.Obj(map[string]any{"id": core.Int("Email ID.")}, "id"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ ID int64 }](args)
			e, err := d.Mail.Get(a.ID)
			if err != nil {
				return "", fmt.Errorf("email #%d not found", a.ID)
			}
			return Clip(fmt.Sprintf("From: %s\nTo: %s\nCc: %s\nDate: %s\nSubject: %s\nMessage-ID: %s\n\n%s", e.From, e.To, e.Cc, e.CreatedAt, e.Subject, e.MessageID, e.Body), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "email_send", Category: "email", Emoji: "📤", Risk: core.RiskSensitive, Available: outOK,
		Description: "Sends a plain-text email from your address " + d.Cfg.EmailFrom + ". Sign as " + d.Cfg.AgentName + ".",
		Params: core.Obj(map[string]any{
			"to":      core.StrList("Recipients."),
			"cc":      core.StrList("CC (optional)."),
			"subject": core.Str("Subject."),
			"body":    core.Str("Message body in plain text."),
		}, "to", "subject", "body"),
		Summarize: func(args json.RawMessage) string {
			a, _ := core.Parse[struct {
				To      []string
				Subject string
			}](args)
			return fmt.Sprintf("Email to %s: \"%s\"", strings.Join(a.To, ", "), short(a.Subject, 60))
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				To, Cc        []string
				Subject, Body string
			}](args)
			e, err := d.Mail.Send(ctx, splitAddrs(a.To), splitAddrs(a.Cc), a.Subject, a.Body, "")
			if err != nil {
				return "", err
			}
			d.St.DB.Exec(`UPDATE emails SET run_id = ? WHERE id = ?`, rc.RunID, e.ID)
			d.St.Audit(rc.Actor(), "email.send", e.To, map[string]string{"subject": a.Subject})
			return fmt.Sprintf("Email #%d sent to %s.", e.ID, e.To), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "email_reply", Category: "email", Emoji: "↩️", Risk: core.RiskSensitive, Available: outOK,
		Description: "Replies to a received email (#id), keeping the thread.",
		Params: core.Obj(map[string]any{
			"id":   core.Int("ID of the email to reply to."),
			"body": core.Str("Reply in plain text."),
			"cc":   core.StrList("CC (optional)."),
		}, "id", "body"),
		Summarize: func(args json.RawMessage) string { return "Reply to email #" + field(args, "id") },
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				ID   int64
				Body string
				Cc   []string
			}](args)
			orig, err := d.Mail.Get(a.ID)
			if err != nil {
				return "", fmt.Errorf("email #%d not found", a.ID)
			}
			subject := orig.Subject
			if !strings.HasPrefix(strings.ToLower(subject), "re:") {
				subject = "Re: " + subject
			}
			quoted := "\n\n" + "On " + strings.Split(orig.CreatedAt, "T")[0] + ", " + orig.From + " wrote:\n> " + strings.ReplaceAll(trunc(orig.Body, 3000), "\n", "\n> ")
			e, err := d.Mail.Send(ctx, []string{mail.ExtractAddress(orig.From)}, splitAddrs(a.Cc), subject, a.Body+quoted, orig.MessageID)
			if err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "email.reply", e.To, map[string]string{"subject": subject})
			return fmt.Sprintf("Reply #%d sent to %s.", e.ID, e.To), nil
		},
	})
}
