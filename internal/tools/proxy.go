package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"blavingad/internal/core"
	"blavingad/internal/proxy"
)

var errProxyAdmin = errors.New("only admins can manage the LLM proxy, politely decline")

func registerProxy(d *Deps) {
	if d.Proxy == nil {
		return
	}
	px := d.Proxy
	admin := func(rc *core.RunContext) error {
		if rc != nil && !rc.CanManageAccess() {
			return errProxyAdmin
		}
		return nil
	}

	d.Reg.Add(&core.Tool{
		Name: "llm_proxy_status", Category: "llm", Emoji: "🔀",
		Description: "Shows the LLM proxy (OpenAI/Anthropic-compatible gateway with fallback): listen address, upstreams in priority order with health/cooldown, model aliases, API keys (prefix only) and usage over the last 24 h.",
		Params:      core.Obj(map[string]any{"hours": core.Int("Usage window in hours (default 24).")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := admin(rc); err != nil {
				return "", err
			}
			var a struct{ Hours int }
			json.Unmarshal(args, &a)
			var b strings.Builder
			fmt.Fprintf(&b, "Listening on %s · endpoints /v1/chat/completions (openai), /v1/messages (anthropic), /v1/models\n\nUpstreams:\n", d.Cfg.ProxyAddr)
			health := map[string]proxy.Health{}
			for _, h := range px.Health() {
				health[h.Name] = h
			}
			for _, u := range px.Upstreams() {
				st := "on"
				if !u.Enabled {
					st = "OFF"
				} else if h := health[u.Name]; h.Down {
					st = "cooling down until " + h.Until
				}
				models := "any model"
				if len(u.Models) > 0 {
					models = strings.Join(u.Models, ", ")
				}
				fmt.Fprintf(&b, "• [%d] %s (%s) %s · key %s · %s · %s\n", u.Priority, u.Name, u.Style, u.BaseURL, keyLabel(u.KeyRef), models, st)
				if e := health[u.Name].LastError; e != "" {
					fmt.Fprintf(&b, "    last error: %s\n", e)
				}
			}
			b.WriteString("\nAliases:\n")
			for _, m := range px.Mappings() {
				fmt.Fprintf(&b, "• %s → %s on %s\n", m.Alias, m.Model, m.Upstream)
			}
			b.WriteString("\nKeys:\n")
			for _, k := range px.Keys() {
				st := "active"
				if !k.Enabled {
					st = "revoked"
				}
				fmt.Fprintf(&b, "• %s %s… (%s) %s · %d req · %d in / %d out tokens · last used %s\n", k.Name, k.Prefix, k.ID, st, k.Requests, k.InputTokens, k.OutputTokens, orDash(k.LastUsedAt))
			}
			if rows, err := px.Stats(a.Hours); err == nil && len(rows) > 0 {
				b.WriteString("\nUsage:\n" + JSON(rows))
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "llm_proxy_upstream", Category: "llm", Emoji: "🔀", Risk: core.RiskSensitive,
		Description: "Adds/updates (action=save) or deletes (action=delete) an LLM proxy upstream. Lower priority = tried first; on 429/402/401/5xx/quota errors the proxy falls back to the next one. key_ref must be a vault://Item/field reference (or \"config\" for the agent's own LLM key, or empty) — NEVER a raw key: store new keys with vault_save first.",
		Params: core.Obj(map[string]any{
			"action":   core.Enum("save or delete", "save", "delete"),
			"name":     core.Str("Identifier (lowercase), e.g. devanshx."),
			"style":    core.Enum("API style it speaks", "openai", "anthropic"),
			"base_url": core.Str("Base URL including /v1, e.g. https://api.openai.com/v1"),
			"key_ref":  core.Str("vault://Item/field, \"config\" or empty."),
			"priority": core.Int("Lower = first (default 100)."),
			"enabled":  core.Bool("Default true."),
			"models":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Models it accepts as-is (empty = any)."},
			"headers":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Extra headers (non-secret)."},
		}, "action", "name"),
		Summarize: func(args json.RawMessage) string {
			var a struct{ Action, Name string }
			json.Unmarshal(args, &a)
			return "llm upstream " + a.Action + " " + a.Name
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := admin(rc); err != nil {
				return "", err
			}
			var a struct {
				Action, Name, Style, BaseURL, KeyRef string
				Priority                             *int
				Enabled                              *bool
				Models                               []string
				Headers                              map[string]string
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Action == "delete" {
				if err := px.DeleteUpstream(a.Name); err != nil {
					return "", err
				}
				return "Deleted upstream " + a.Name, nil
			}
			u := proxy.Upstream{Name: a.Name, Style: a.Style, BaseURL: a.BaseURL, KeyRef: a.KeyRef, Models: a.Models, Headers: a.Headers, Priority: 100, Enabled: true}
			for _, old := range px.Upstreams() { // partial update
				if old.Name == strings.ToLower(a.Name) {
					u = old
					if a.Style != "" {
						u.Style = a.Style
					}
					if a.BaseURL != "" {
						u.BaseURL = a.BaseURL
					}
					if a.KeyRef != "" {
						u.KeyRef = a.KeyRef
					}
					if a.Models != nil {
						u.Models = a.Models
					}
					if a.Headers != nil {
						u.Headers = a.Headers
					}
				}
			}
			if a.Priority != nil {
				u.Priority = *a.Priority
			}
			if a.Enabled != nil {
				u.Enabled = *a.Enabled
			}
			if err := px.SaveUpstream(u); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "llm_proxy.upstream", u.Name, map[string]any{"base_url": u.BaseURL, "style": u.Style, "priority": u.Priority})
			return fmt.Sprintf("Saved upstream %s (%s, priority %d, key %s).", u.Name, u.Style, u.Priority, keyLabel(u.KeyRef)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "llm_proxy_model_map", Category: "llm", Emoji: "🔀",
		Description: "Maps a public model alias (what clients send, e.g. opus) to the real model name on an upstream (upstream=\"*\" = every upstream). action=set or delete. Once an alias has mappings, only the mapped upstreams serve it, in priority order.",
		Params: core.Obj(map[string]any{
			"action":   core.Enum("set or delete", "set", "delete"),
			"alias":    core.Str("Public model name."),
			"upstream": core.Str("Upstream name or * (delete: empty = all)."),
			"model":    core.Str("Real model on that upstream (set)."),
		}, "action", "alias"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := admin(rc); err != nil {
				return "", err
			}
			var a struct{ Action, Alias, Upstream, Model string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Action == "delete" {
				return "Deleted mapping " + a.Alias, px.DeleteMapping(a.Alias, a.Upstream)
			}
			if err := px.SetMapping(proxy.Mapping{Alias: a.Alias, Upstream: a.Upstream, Model: a.Model}); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s → %s on %s", a.Alias, a.Model, a.Upstream), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "llm_proxy_key", Category: "llm", Emoji: "🔑", Risk: core.RiskSensitive,
		Description: "Manages client API keys of the LLM proxy. action=create returns the sk-blv-… key ONCE (only its hash is stored): give it to the requester privately (DM), never in a public channel. action=revoke disables a key (by id, name or prefix), action=delete removes it.",
		Params: core.Obj(map[string]any{
			"action": core.Enum("create, revoke or delete", "create", "revoke", "delete"),
			"name":   core.Str("Key name (create) or id/name/prefix (revoke/delete)."),
			"models": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Restrict to these model aliases (empty = all)."},
		}, "action", "name"),
		Summarize: func(args json.RawMessage) string {
			var a struct{ Action, Name string }
			json.Unmarshal(args, &a)
			return "llm key " + a.Action + " " + a.Name
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := admin(rc); err != nil {
				return "", err
			}
			var a struct {
				Action, Name string
				Models       []string
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			switch a.Action {
			case "create":
				k, plain, err := px.CreateKey(a.Name, rc.Actor(), a.Models)
				if err != nil {
					return "", err
				}
				d.St.Audit(rc.Actor(), "llm_proxy.key.create", k.ID, map[string]any{"name": k.Name})
				return fmt.Sprintf("Created key %s (%s). Shown once, cannot be recovered:\n%s", k.Name, k.ID, plain), nil
			case "revoke", "delete":
				if err := px.RevokeKey(a.Name, a.Action == "delete"); err != nil {
					return "", err
				}
				d.St.Audit(rc.Actor(), "llm_proxy.key."+a.Action, a.Name, nil)
				return a.Action + "d key " + a.Name, nil
			}
			return "", errors.New("action must be create, revoke or delete")
		},
	})
}

func keyLabel(ref string) string {
	if ref == "" {
		return "none"
	}
	return ref
}

func orDash(s string) string {
	if s == "" {
		return "never"
	}
	return s
}
