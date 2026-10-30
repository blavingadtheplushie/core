package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/store"
)

var reToolName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)

func (d *Deps) evolution(rc *core.RunContext, kind, summary, details string) {
	id := store.NewID("evo")
	d.St.DB.Exec(`INSERT INTO evolutions(id, kind, summary, details, status, run_id, created_at) VALUES(?,?,?,?,?,?,?)`,
		id, kind, summary, details, "live", rc.RunID, store.Now())
	d.Bus.Publish("evolution", map[string]string{"id": id, "kind": kind, "summary": summary, "status": "live"})
}

func registerSelf(d *Deps) {
	selfOK := func() bool { return d.Self != nil }

	d.Reg.Add(&core.Tool{
		Name: "self_info", Category: "self", Emoji: "🪞", Available: selfOK,
		Description: "Info about your own code: version, source folder (" + d.Cfg.SourceDir + ", a Go git repo), modified files, latest commits, rollback availability. Also read AGENTS.md in that folder before changing your code.",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			return JSON(d.Self.Info(ctx)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "self_diff", Category: "self", Emoji: "🧾", Available: selfOK,
		Description: "Shows the diff of your uncommitted changes to your own code.",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			return Clip(d.Self.Diff(ctx), 30000), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "self_build", Category: "self", Emoji: "🏗️", Available: selfOK,
		Description: "Builds and tests your modified code (go mod tidy, go vet, go build, go test, smoke test) WITHOUT deploying it. Use it to iterate until it passes.",
		Params:      core.Obj(map[string]any{"tests": core.Bool("Run go test (default true).")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Tests *bool }](args)
			tests := a.Tests == nil || *a.Tests
			bin, log, err := d.Self.Build(ctx, tests)
			if bin != "" {
				os.Remove(bin)
			}
			if err != nil {
				return "", fmt.Errorf("%v\n\n%s", err, Clip(log, 12000))
			}
			return "✅ Build and tests OK.\n\n" + Clip(log, 6000), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "self_upgrade", Category: "self", Emoji: "🧬", Available: selfOK,
		Description: "Deploys your new version: build + tests, commit, push to your GitHub repo, then restart on the new binary. If the new version crashes on startup, it automatically rolls back to the old one. Your final message is sent before the restart.",
		Params:      core.Obj(map[string]any{"summary": core.Str("Summary of the improvement (becomes the commit message).")}, "summary"),
		Summarize:   summarizeField("Self-upgrade: ", "summary", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			summary := strings.TrimSpace(field(args, "summary"))
			if summary == "" {
				return "", errors.New("summary is required")
			}
			res, err := d.Self.Upgrade(ctx, rc, summary)
			if err != nil {
				return "", fmt.Errorf("%v\n\n%s", err, Clip(res.BuildLog, 10000))
			}
			pushed := "not pushed (no SELF_REPO)"
			if res.Pushed {
				pushed = "pushed to " + d.Cfg.SelfRepo
			}
			return fmt.Sprintf("🧬 New version ready (commit %s, %s). I'll restart as soon as this reply ends and announce when it's stable.", res.Commit, pushed), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "self_rollback", Category: "self", Emoji: "⏪", Available: selfOK,
		Description: "Reverts to your previous version (binary + git revert of the last commit) and restarts.",
		Params:      core.Obj(map[string]any{"reason": core.Str("Why.")}, "reason"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			out, err := d.Self.Rollback(ctx, rc, field(args, "reason"))
			if err != nil {
				return "", err
			}
			return "⏪ Rollback scheduled, restarting shortly.\n" + out, nil
		},
	})

	// ---------- persona ----------

	d.Reg.Add(&core.Tool{
		Name: "persona_get", Category: "self", Emoji: "🎭",
		Description: "Shows your current persona (personality, tone, values) as injected into your prompt.",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			return core.CurrentPersona(d.St, d.Cfg.AgentName), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "persona_update", Category: "self", Emoji: "🎭",
		Description: "Rewrites your persona (full text, markdown). Do it when asked to change your tone/behavior or when you spot a lasting improvement. All versions are kept.",
		Params: core.Obj(map[string]any{
			"content": core.Str("Full new persona."),
			"reason":  core.Str("Why this change."),
		}, "content", "reason"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Content, Reason string }](args)
			if len(strings.TrimSpace(a.Content)) < 40 {
				return "", errors.New("persona too short")
			}
			d.St.DB.Exec(`INSERT INTO persona_versions(content, reason, author, created_at) VALUES(?,?,?,?)`, a.Content, a.Reason, rc.Actor(), store.Now())
			d.evolution(rc, "persona", "Persona: "+a.Reason, a.Content)
			return "🎭 Persona updated — takes effect from the next message.", nil
		},
	})

	// ---------- skills ----------

	d.Reg.Add(&core.Tool{
		Name: "skill_save", Category: "self", Emoji: "📚",
		Description: "Creates or updates a skill: a reusable markdown procedure (steps, commands, pitfalls) you can load again later. Create one whenever you've solved a non-trivial problem that is likely to come back.",
		Params: core.Obj(map[string]any{
			"name":        core.Str("Short kebab-case identifier (e.g. deploy-nextjs-cloudflare)."),
			"description": core.Str("One sentence: WHEN to use this skill."),
			"content":     core.Str("The full markdown content."),
		}, "name", "description", "content"),
		Summarize: summarizeField("Skill ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Name, Description, Content string }](args)
			name := slugify(a.Name)
			if name == "" || a.Content == "" {
				return "", errors.New("name and content are required")
			}
			now := store.Now()
			_, err := d.St.DB.Exec(`INSERT INTO skills(id, name, description, content, author, created_at, updated_at) VALUES(?,?,?,?,?,?,?)
				ON CONFLICT(name) DO UPDATE SET description = excluded.description, content = excluded.content, updated_at = excluded.updated_at`,
				store.NewID("skl"), name, a.Description, a.Content, rc.Actor(), now, now)
			if err != nil {
				return "", err
			}
			d.evolution(rc, "skill", "Skill \""+name+"\": "+a.Description, a.Content)
			return "📚 Skill \"" + name + "\" saved.", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "skill_read", Category: "self", Emoji: "📖",
		Description: "Loads the full content of a skill (see the list in your prompt).",
		Params:      core.Obj(map[string]any{"name": core.Str("Skill name.")}, "name"),
		Summarize:   summarizeField("Skill ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			name := slugify(field(args, "name"))
			var desc, content string
			if err := d.St.DB.QueryRow(`SELECT description, content FROM skills WHERE name = ? AND enabled = 1`, name).Scan(&desc, &content); err != nil {
				return "", fmt.Errorf("skill \"%s\" not found", name)
			}
			d.St.DB.Exec(`UPDATE skills SET uses = uses + 1 WHERE name = ?`, name)
			return "# " + name + "\n_" + desc + "_\n\n" + content, nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "skill_delete", Category: "self", Emoji: "🗑️",
		Description: "Deletes an outdated skill.",
		Params:      core.Obj(map[string]any{"name": core.Str("Name.")}, "name"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			name := slugify(field(args, "name"))
			d.St.DB.Exec(`DELETE FROM skills WHERE name = ?`, name)
			d.evolution(rc, "skill", "Skill \""+name+"\" deleted", "")
			return "Skill deleted.", nil
		},
	})

	// ---------- custom tools ----------

	d.Reg.Add(&core.Tool{
		Name: "tool_create", Category: "self", Emoji: "🛠️",
		Description: "Creates (or replaces) a NEW TOOL for yourself, available immediately without restarting. The script receives its arguments as JSON on stdin (and in $TOOL_ARGS) and must write its result to stdout. Great for wrapping an API or a routine you use often.",
		Params: core.Obj(map[string]any{
			"name":        core.Str("snake_case name (e.g. city_weather)."),
			"description": core.Str("What the tool does and when to use it."),
			"parameters":  core.Str("JSON Schema (object) of the arguments, serialized. E.g. {\"type\":\"object\",\"properties\":{\"city\":{\"type\":\"string\"}},\"required\":[\"city\"]}"),
			"runtime":     core.Enum("Interpreter.", "bash", "python", "node"),
			"code":        core.Str("Script source code."),
		}, "name", "description", "parameters", "runtime", "code"),
		Summarize: summarizeField("New tool ", "name", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Name, Description, Parameters, Runtime, Code string }](args)
			if !reToolName.MatchString(a.Name) {
				return "", errors.New("invalid name (snake_case, 3-40 characters)")
			}
			if t, ok := d.Reg.Get(a.Name); ok && !t.Custom {
				return "", errors.New("this name is already taken by a built-in tool")
			}
			var schema map[string]any
			if err := json.Unmarshal([]byte(a.Parameters), &schema); err != nil || schema["type"] != "object" {
				return "", errors.New("parameters must be a JSON Schema of type object")
			}
			if _, ok := runtimes[a.Runtime]; !ok {
				return "", errors.New("unknown runtime")
			}
			now := store.Now()
			_, err := d.St.DB.Exec(`INSERT INTO custom_tools(id, name, description, parameters, runtime, code, author, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)
				ON CONFLICT(name) DO UPDATE SET description=excluded.description, parameters=excluded.parameters, runtime=excluded.runtime, code=excluded.code, enabled=1, updated_at=excluded.updated_at`,
				store.NewID("ctl"), a.Name, a.Description, a.Parameters, a.Runtime, a.Code, rc.Actor(), now, now)
			if err != nil {
				return "", err
			}
			d.addCustomTool(a.Name, a.Description, schema, a.Runtime, a.Code)
			d.evolution(rc, "tool", "New tool \""+a.Name+"\": "+a.Description, a.Code)
			return "🛠️ Tool \"" + a.Name + "\" created and loaded. You can call it right away (it requires approval by default; an admin can switch it to auto).", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "tool_delete", Category: "self", Emoji: "🗑️",
		Description: "Deletes a tool you created.",
		Params:      core.Obj(map[string]any{"name": core.Str("Name.")}, "name"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			name := field(args, "name")
			if t, ok := d.Reg.Get(name); !ok || !t.Custom {
				return "", errors.New("not a custom tool")
			}
			d.St.DB.Exec(`DELETE FROM custom_tools WHERE name = ?`, name)
			d.Reg.Remove(name)
			d.evolution(rc, "tool", "Tool \""+name+"\" deleted", "")
			return "Tool deleted.", nil
		},
	})
}

var runtimes = map[string][2]string{
	"bash":   {"bash", ".sh"},
	"python": {"python3", ".py"},
	"node":   {"node", ".mjs"},
}

func (d *Deps) addCustomTool(name, desc string, schema map[string]any, runtime, code string) {
	rt := runtimes[runtime]
	dir := filepath.Join(d.Cfg.DataDir, "tools")
	os.MkdirAll(dir, 0o755)
	script := filepath.Join(dir, name+rt[1])
	os.WriteFile(script, []byte(code), 0o755)
	d.Reg.Add(&core.Tool{
		Name: name, Description: desc + " [tool created by you]", Category: "custom", Emoji: "🧩",
		Params: schema, Risk: core.RiskSensitive, Custom: true,
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, rt[0], script)
			cmd.Dir = d.Cfg.WorkspaceDir
			cmd.Env = append(append(os.Environ(), d.toolEnv()...), "TOOL_ARGS="+string(args), "RUN_ID="+rc.RunID)
			cmd.Stdin = strings.NewReader(string(args))
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("%v\n%s", err, Clip(string(out), 6000))
			}
			return Clip(string(out), 0), nil
		},
	})
}

func LoadCustomTools(d *Deps) {
	rows, err := d.St.DB.Query(`SELECT name, description, parameters, runtime, code FROM custom_tools WHERE enabled = 1`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, desc, params, runtime, code string
		rows.Scan(&name, &desc, &params, &runtime, &code)
		var schema map[string]any
		if json.Unmarshal([]byte(params), &schema) != nil {
			continue
		}
		if _, ok := runtimes[runtime]; ok {
			d.addCustomTool(name, desc, schema, runtime, code)
		}
	}
}
