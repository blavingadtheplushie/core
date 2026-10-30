package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"blavingad/internal/core"
)

var errNotAdmin = errors.New("only admins can change access — the person talking to you is not one, politely decline")

func (d *Deps) personLabel(id string) string {
	var name string
	d.St.DB.QueryRow(`SELECT COALESCE(NULLIF(real_name,''), name, '') FROM people WHERE slack_id = ?`, id).Scan(&name)
	if name == "" {
		return "<@" + id + ">"
	}
	return fmt.Sprintf("%s (<@%s>)", name, id)
}

func (d *Deps) describeProfile(p core.Profile) string {
	reply := "replies"
	if !p.Reply {
		reply = "IGNORED (no reply)"
	}
	steps := "unlimited"
	if p.MaxIterations > 0 {
		steps = fmt.Sprint(p.MaxIterations)
	}
	n := len(d.Access.AllowedTools(d.Reg, &p))
	return fmt.Sprintf("%s %s [%s] — %s\n   tools: %s (%d effective) · memory: %s · approval: %s · max steps: %s · %s",
		p.Emoji, p.Label, p.Name, p.Description, strings.Join(p.Tools, " "), n, p.Memory, p.Approve, steps, reply)
}

func registerAccess(d *Deps) {
	if d.Access == nil {
		return
	}
	guard := func(rc *core.RunContext) error {
		if !rc.CanManageAccess() {
			return errNotAdmin
		}
		return nil
	}

	d.Reg.Add(&core.Tool{
		Name: "access_overview", Category: "access", Emoji: "🛂",
		Description: "Shows the whitelist: access profiles (tools, memory, approval), the default profile for unlisted people, and who has which profile.",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			var b strings.Builder
			def := d.Access.DefaultProfile()
			b.WriteString("Profiles:\n")
			for _, p := range d.Access.Profiles() {
				mark := ""
				if p.Name == def {
					mark = "  ← default profile (unlisted people)"
				}
				b.WriteString("- " + d.describeProfile(p) + mark + "\n")
			}
			b.WriteString("\nWhitelist:\n")
			members := d.Access.Members()
			if len(members) == 0 {
				b.WriteString("(empty — everyone gets the default profile)\n")
			}
			for _, m := range members {
				fmt.Fprintf(&b, "- %s → %s%s\n", d.personLabel(m.SlackID), m.Profile, map[bool]string{true: " (SLACK_ADMINS)", false: ""}[m.EnvAdmin])
			}
			b.WriteString("\nTool syntax: \"*\" = everything, \"cat:web\" = a category (web, slack, memory, code, github, email, deploy, self, system, access, vault, custom), \"tool_name\", and \"-…\" to exclude.")
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_check", Category: "access", Emoji: "🔍",
		Description: "Tells which profile applies to a person and the exact list of tools they can use.",
		Params:      core.Obj(map[string]any{"user": core.Str("Slack ID (U…) or mention.")}, "user"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			id := core.NormalizeSlackID(field(args, "user"))
			p, listed := d.Access.Resolve(id)
			status := "on the whitelist"
			if !listed {
				status = "not on the whitelist (default profile)"
			}
			return fmt.Sprintf("%s: %s\n%s\nTools: %s", d.personLabel(id), status, d.describeProfile(p), strings.Join(d.Access.AllowedTools(d.Reg, &p), ", ")), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_assign", Category: "access", Emoji: "✅",
		Description: "Adds a person to the whitelist (or changes their profile). Admins only.",
		Params: core.Obj(map[string]any{
			"user":    core.Str("Slack ID (U…) or mention <@U…>."),
			"profile": core.Str("Profile name (see access_overview), e.g. member."),
			"note":    core.Str("Why / who is this (optional)."),
		}, "user", "profile"),
		Summarize: func(args json.RawMessage) string {
			return "Access " + field(args, "profile") + " for " + field(args, "user")
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := guard(rc); err != nil {
				return "", err
			}
			a, _ := core.Parse[struct{ User, Profile, Note string }](args)
			if err := d.Access.SetMember(a.User, a.Profile, a.Note, rc.Actor()); err != nil {
				return "", err
			}
			id := core.NormalizeSlackID(a.User)
			d.St.Audit(rc.Actor(), "access.assign", id, map[string]string{"profile": a.Profile})
			d.Bus.Publish("access", map[string]string{"user": id, "profile": a.Profile})
			return fmt.Sprintf("✅ %s now has the profile \"%s\".", d.personLabel(id), a.Profile), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_remove", Category: "access", Emoji: "➖",
		Description: "Removes a person from the whitelist: they fall back to the default profile. Admins only.",
		Params:      core.Obj(map[string]any{"user": core.Str("Slack ID or mention.")}, "user"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := guard(rc); err != nil {
				return "", err
			}
			id := core.NormalizeSlackID(field(args, "user"))
			if err := d.Access.RemoveMember(id); err != nil {
				return "", errors.New("this person is not on the whitelist")
			}
			d.St.Audit(rc.Actor(), "access.remove", id, nil)
			d.Bus.Publish("access", map[string]string{"user": id})
			return fmt.Sprintf("➖ %s removed: now on the default profile \"%s\".", d.personLabel(id), d.Access.DefaultProfile()), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_set_default", Category: "access", Emoji: "🚪",
		Description: "Sets the profile applied to everyone not on the whitelist (e.g. guest, or blocked to ignore everyone except the list). Admins only.",
		Params:      core.Obj(map[string]any{"profile": core.Str("Profile name.")}, "profile"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := guard(rc); err != nil {
				return "", err
			}
			name := field(args, "profile")
			if name == "admin" {
				return "", errors.New("giving the admin profile to everyone is not allowed")
			}
			if err := d.Access.SetDefault(name); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "access.default", name, nil)
			d.Bus.Publish("access", map[string]string{"default": name})
			return "🚪 Default profile: " + name, nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_profile_save", Category: "access", Emoji: "🎚️",
		Description: "Creates or updates an access profile. Only the provided fields change. tools: \"*\", \"cat:<category>\", \"tool_name\", \"-…\" to exclude. memory: full (reads and remembers), read (reads without remembering), none (no memory). approve: any (approves everything), own (their own requests), none. Admins only.",
		Params: core.Obj(map[string]any{
			"name":           core.Str("Identifier (lowercase, dashes), e.g. client."),
			"label":          core.Str("Display name."),
			"emoji":          core.Str("Emoji."),
			"description":    core.Str("One sentence."),
			"tools":          core.StrList("Tool tokens."),
			"memory":         core.Enum("Memory mode.", "full", "read", "none"),
			"reply":          core.Bool("Reply to these people (false = ignored)."),
			"approve":        core.Enum("Approval rights.", "any", "own", "none"),
			"max_iterations": core.Int("Max steps per request (0 = global setting)."),
			"extra_prompt":   core.Str("Specific instructions when you talk to this profile."),
		}, "name"),
		Summarize: summarizeField("Access profile ", "name", 30),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := guard(rc); err != nil {
				return "", err
			}
			a, err := core.Parse[struct {
				Name          string    `json:"name"`
				Label         *string   `json:"label"`
				Emoji         *string   `json:"emoji"`
				Description   *string   `json:"description"`
				Tools         *[]string `json:"tools"`
				Memory        *string   `json:"memory"`
				Reply         *bool     `json:"reply"`
				Approve       *string   `json:"approve"`
				MaxIterations *int      `json:"max_iterations"`
				ExtraPrompt   *string   `json:"extra_prompt"`
			}](args)
			if err != nil {
				return "", err
			}
			p, exists := d.Access.Profile(strings.ToLower(a.Name))
			if !exists {
				p = core.Profile{Name: a.Name, Tools: []string{"cat:web"}, Memory: "none", Reply: true, Approve: "none"}
			}
			set := func(dst *string, src *string) {
				if src != nil {
					*dst = *src
				}
			}
			set(&p.Label, a.Label)
			set(&p.Emoji, a.Emoji)
			set(&p.Description, a.Description)
			set(&p.Memory, a.Memory)
			set(&p.Approve, a.Approve)
			set(&p.ExtraPrompt, a.ExtraPrompt)
			if a.Tools != nil {
				p.Tools = *a.Tools
			}
			if a.Reply != nil {
				p.Reply = *a.Reply
			}
			if a.MaxIterations != nil {
				p.MaxIterations = *a.MaxIterations
			}
			if err := d.Access.SaveProfile(p); err != nil {
				return "", err
			}
			saved, _ := d.Access.Profile(strings.ToLower(a.Name))
			d.St.Audit(rc.Actor(), "access.profile", saved.Name, saved)
			d.Bus.Publish("access", map[string]string{"profile": saved.Name})
			verb := "updated"
			if !exists {
				verb = "created"
			}
			return fmt.Sprintf("🎚️ Profile %s:\n%s", verb, d.describeProfile(saved)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "access_profile_delete", Category: "access", Emoji: "🗑️",
		Description: "Deletes an access profile (its members fall back to the default profile). Admins only.",
		Params:      core.Obj(map[string]any{"name": core.Str("Profile name.")}, "name"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			if err := guard(rc); err != nil {
				return "", err
			}
			name := field(args, "name")
			if err := d.Access.DeleteProfile(name); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "access.profile.delete", name, nil)
			d.Bus.Publish("access", map[string]string{"profile": name})
			return "Profile \"" + name + "\" deleted.", nil
		},
	})
}
