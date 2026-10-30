package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"blavingad/internal/core"
	"blavingad/internal/vault"
)

var rePlaceholder = regexp.MustCompile(`\{\{\s*(vault://[^}]+?)\s*\}\}`)

func hasPlaceholder(s string) bool { return rePlaceholder.MatchString(s) }

func (d *Deps) vaultReady() bool { return d.Vault != nil && d.Vault.Enabled() }

// vaultAllowed: the person's profile must grant the vault category.
func (d *Deps) vaultAllowed(rc *core.RunContext) error {
	if !d.vaultReady() {
		return errors.New("no Vaultwarden vault is configured")
	}
	if rc.Allowed != nil && !rc.Allowed["vault_search"] {
		return errors.New("this person's access profile does not allow using the vault")
	}
	return nil
}

func (d *Deps) resolveRef(ctx context.Context, ref string) (string, error) {
	v, err := d.Vault.Resolve(ctx, strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	core.Secrets.Add(v)
	return v, nil
}

// secretEnv resolves {"ENV": "vault://Item/field"} into KEY=value pairs.
func (d *Deps) secretEnv(ctx context.Context, rc *core.RunContext, refs map[string]string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if err := d.vaultAllowed(rc); err != nil {
		return nil, err
	}
	reEnv := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var env []string
	for _, k := range keys {
		if !reEnv.MatchString(k) {
			return nil, fmt.Errorf("invalid variable name: %s", k)
		}
		v, err := d.resolveRef(ctx, refs[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		env = append(env, k+"="+v)
	}
	d.St.Audit(rc.Actor(), "vault.use", strings.Join(keys, ","), refs)
	return env, nil
}

// expand replaces {{vault://Item/field}} placeholders with their values.
func (d *Deps) expand(ctx context.Context, rc *core.RunContext, s string) (string, error) {
	if !hasPlaceholder(s) {
		return s, nil
	}
	if err := d.vaultAllowed(rc); err != nil {
		return "", err
	}
	var firstErr error
	out := rePlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		ref := rePlaceholder.FindStringSubmatch(m)[1]
		v, err := d.resolveRef(ctx, ref)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	})
	if firstErr == nil {
		d.St.Audit(rc.Actor(), "vault.use", "http_request", rePlaceholder.FindAllString(s, -1))
	}
	return out, firstErr
}

func registerVault(d *Deps) {
	ok := d.vaultReady

	d.Reg.Add(&core.Tool{
		Name: "vault_search", Category: "vault", Emoji: "🔐", Available: ok,
		Description: "Searches the Vaultwarden vault (folder \"" + d.vaultFolder() + "\" and the rest): returns names, usernames, URLs, available fields and the REFERENCE of each secret, never its value. To use a secret, pass its vault://Item/field reference to the shell tool's `secrets` parameter, or write {{vault://Item/field}} in an http_request: the value is injected without you seeing it. Fields: password (default), username, notes, uri, totp (current code) or the name of a custom field.",
		Params:      core.Obj(map[string]any{"query": core.Str("Text to search for (empty = list everything)."), "limit": core.Int("Max (default 25).")}),
		Summarize:   summarizeField("Vault: ", "query", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := d.vaultAllowed(rc); err != nil {
				return "", err
			}
			a, _ := core.Parse[struct {
				Query string
				Limit int
			}](args)
			if a.Limit <= 0 || a.Limit > 100 {
				a.Limit = 25
			}
			items := d.Vault.Search(a.Query, a.Limit)
			var b strings.Builder
			fmt.Fprintf(&b, "✍️ vault_save writes new items to: %s\n\n", d.Vault.Status().Writes)
			if len(items) == 0 {
				return b.String() + "Nothing found in the vault.", nil
			}
			for _, m := range items {
				var has []string
				if m.HasPassword {
					has = append(has, "password")
				}
				if m.Username != "" {
					has = append(has, "username="+m.Username)
				}
				if m.HasTOTP {
					has = append(has, "totp")
				}
				if m.HasNotes {
					has = append(has, "notes")
				}
				if len(m.Fields) > 0 {
					has = append(has, "fields: "+strings.Join(m.Fields, ", "))
				}
				folder := m.Folder
				if folder == "" {
					folder = "—"
				}
				where := "folder " + folder
				if len(m.Collections) > 0 {
					where += ", collection " + strings.Join(m.Collections, "/")
				}
				fmt.Fprintf(&b, "- %s [%s, %s] %s\n  %s\n  ref: %s\n", m.Name, m.Type, where, m.URI, strings.Join(has, " · "), m.Reference)
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "vault_reveal", Category: "vault", Emoji: "👁️", Risk: core.RiskSensitive, Available: ok,
		Description: "Shows a secret's value TO THE PERSON TALKING TO YOU, as an ephemeral Slack message only they can see. You never see the value. Only use it when a human really needs it (e.g. to log into a site).",
		Params: core.Obj(map[string]any{
			"reference": core.Str("vault://Item/field"),
			"reason":    core.Str("Why this person needs it."),
		}, "reference", "reason"),
		Summarize: func(args json.RawMessage) string {
			return "Reveal " + field(args, "reference") + " to the person (" + short(field(args, "reason"), 50) + ")"
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := d.vaultAllowed(rc); err != nil {
				return "", err
			}
			if d.Reveal == nil || rc.UserID == "" {
				return "", errors.New("not possible here: revealing goes through an ephemeral Slack message. From the web, open Vaultwarden directly")
			}
			ref := field(args, "reference")
			v, err := d.resolveRef(ctx, ref)
			if err != nil {
				return "", err
			}
			if err := d.Reveal(rc, ref, v); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "vault.reveal", ref, map[string]string{"to": rc.UserID, "reason": field(args, "reason")})
			return "The value was sent as an ephemeral message to " + rc.Actor() + " (visible only to them, never to you).", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "vault_save", Category: "vault", Emoji: "🗝️", Risk: core.RiskSensitive, Available: ok,
		Description: "Stores a secret in Vaultwarden (creates it, or updates the item with the same name in the same place). You CAN write to the vault with this tool. kind=login (default: username/password/URL) or kind=note (a secure note; the secret goes into a hidden field). generate=true creates a strong password without you seeing it. Values can reference another secret with {{vault://…}}. Items go to folder \"" + d.vaultFolder() + "\" of the default write target (see vault_search); pass collection=<name> to write into an organization collection instead. Use it as soon as you get or create a token/password: never keep it in memory, a file or a message. If it fails, report the exact error: it says what to change in Vaultwarden.",
		Params: core.Obj(map[string]any{
			"name":       core.Str("Item name (e.g. CLOUDFLARE_API_TOKEN, or \"Postgres prod\")."),
			"username":   core.Str("Username (optional)."),
			"password":   core.Str("Password / token (optional)."),
			"generate":   core.Bool("Generate a strong password instead."),
			"length":     core.Int("Length of the generated password (default 32)."),
			"uri":        core.Str("Associated URL (optional)."),
			"notes":      core.Str("Notes (optional)."),
			"fields":     map[string]any{"type": "object", "description": "Hidden custom fields (name → value).", "additionalProperties": map[string]any{"type": "string"}},
			"kind":       core.Enum("Item type.", "login", "note"),
			"collection": core.Str("Organization collection (name or id) to write into, or \"personal\". Empty = default write target."),
		}, "name"),
		Summarize: summarizeField("Store in vault: ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := d.vaultAllowed(rc); err != nil {
				return "", err
			}
			a, err := core.Parse[struct {
				Name, Username, Password, URI, Notes, Kind, Collection string
				Generate                                               bool
				Length                                                 int
				Fields                                                 map[string]string
			}](args)
			if err != nil {
				return "", err
			}
			if a.Password, err = d.expand(ctx, rc, a.Password); err != nil {
				return "", err
			}
			for k, v := range a.Fields {
				if a.Fields[k], err = d.expand(ctx, rc, v); err != nil {
					return "", err
				}
			}
			if a.Generate {
				if a.Password, err = d.Vault.Generate(ctx, a.Length); err != nil {
					return "", err
				}
			}
			core.Secrets.Add(a.Password)
			for _, v := range a.Fields {
				core.Secrets.Add(v)
			}
			item, updated, err := d.Vault.Save(ctx, vault.SaveInput{Name: a.Name, Kind: a.Kind, Username: a.Username, Password: a.Password,
				Notes: a.Notes, URI: a.URI, Fields: a.Fields, Collection: a.Collection})
			if err != nil {
				return "", err
			}
			where := "the personal vault"
			if item.OrgID != "" {
				where = "an organization collection"
				for _, m := range d.Vault.Search(item.Name, 0) {
					if m.ID == item.ID && len(m.Collections) > 0 {
						where = "collection " + strings.Join(m.Collections, "/")
					}
				}
			}
			d.St.Audit(rc.Actor(), "vault.save", a.Name, map[string]any{"updated": updated, "generated": a.Generate})
			verb := "created"
			if updated {
				verb = "updated"
			}
			ref := "vault://" + item.Name
			out := fmt.Sprintf("🗝️ \"%s\" %s in %s (folder %s). Reference: %s", item.Name, verb, where, d.vaultFolder(), ref)
			if a.Generate {
				out += " (password generated — use " + ref + "/password to inject it)"
			}
			return out, nil
		},
	})
}

func (d *Deps) vaultFolder() string {
	if d.Vault == nil {
		return "BLAVINGAD"
	}
	return d.Vault.Folder()
}
