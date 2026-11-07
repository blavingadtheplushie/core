package tools

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"blavingad/internal/core"
	"blavingad/internal/vault"
)

var reAskName = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{1,80}$`)

func (d *Deps) askReady(rc *core.RunContext) error {
	if d.Ask == nil {
		return errors.New("Slack is not connected: forms are unavailable")
	}
	if rc.UserID == "" || !strings.HasPrefix(rc.UserID, "U") {
		return errors.New("no Slack user behind this run to ask (forms need a Slack user; Signal/email/web runs without a linked Slack user can't use them)")
	}
	return nil
}

func registerAsk(d *Deps) {
	slackOK := func() bool { return d.Ask != nil }

	d.Reg.Add(&core.Tool{
		Name: "ask_secret", Category: "vault", Emoji: "🔑", Risk: core.RiskSafe,
		Available: func() bool { return slackOK() && d.vaultReady() },
		Description: "Asks the person talking to you for a secret (API key, token, password) through a Slack form: a 'Fill in' button opens a modal, and the value goes STRAIGHT into Vaultwarden as vault://<name>/password. You never see it. Blocks until answered (max 15 min). Use it whenever a task needs a credential you don't have, instead of asking for it in chat. Then use the reference normally.",
		Params: core.Obj(map[string]any{
			"name":  core.Str("Vault item name to store it as, e.g. LASTFM_API_KEY."),
			"label": core.Str("Field label shown to the human, e.g. 'last.fm API key'."),
			"hint":  core.Str("Optional help: where to get it (a URL is fine)."),
			"uri":   core.Str("Optional URL saved with the vault item."),
		}, "name", "label"),
		Summarize: summarizeField("Ask for secret ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := d.vaultAllowed(rc); err != nil {
				return "", err
			}
			if err := d.askReady(rc); err != nil {
				return "", err
			}
			a, err := core.Parse[struct{ Name, Label, Hint, URI string }](args)
			if err != nil {
				return "", err
			}
			a.Name = strings.TrimSpace(a.Name)
			if !reAskName.MatchString(a.Name) {
				return "", errors.New("invalid name: letters, digits, space, _ . - only")
			}
			v, err := d.Ask(ctx, rc, a.Label, a.Hint, a.Name, true)
			if err != nil {
				return "", err
			}
			core.Secrets.Add(v)
			item, updated, err := d.Vault.Save(ctx, vault.SaveInput{Name: a.Name, Password: v, URI: a.URI, Notes: "Entered by " + rc.Actor() + " via a Slack form."})
			if err != nil {
				return "", errors.New("the human answered but saving to the vault failed: " + core.Secrets.Redact(err.Error()))
			}
			verb := "created"
			if updated {
				verb = "updated"
			}
			d.St.Audit(rc.Actor(), "vault.ask", a.Name, map[string]string{"from": rc.UserID})
			return "Secret received from " + rc.Actor() + " and " + verb + " in the vault (item " + item.Name + "). Use it by reference: vault://" + a.Name + "/password. You never see its value.", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "ask_input", Category: "slack", Emoji: "📝", Risk: core.RiskSafe, Available: slackOK,
		Description: "Asks the person talking to you a short NON-secret question through a Slack form (button → modal) and returns their answer. Blocks until answered (max 15 min). For secrets use ask_secret instead. Prefer just asking in chat for normal conversation; use this when you need one precise value mid-task.",
		Params: core.Obj(map[string]any{
			"label": core.Str("The question / field label."),
			"hint":  core.Str("Optional help text."),
		}, "label"),
		Summarize: summarizeField("Ask: ", "label", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := d.askReady(rc); err != nil {
				return "", err
			}
			a, err := core.Parse[struct{ Label, Hint string }](args)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Label) == "" {
				return "", errors.New("label is required")
			}
			v, err := d.Ask(ctx, rc, a.Label, a.Hint, "", false)
			if err != nil {
				return "", err
			}
			return "Answer from " + rc.Actor() + ": " + v, nil
		},
	})
}
