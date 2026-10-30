package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"blavingad/internal/core"
	"blavingad/internal/memory"
)

func registerMemory(d *Deps) {
	kinds := memory.Kinds

	d.Reg.Add(&core.Tool{
		Name: "memory_save", Category: "memory", Emoji: "🧠",
		Description: "Durably remembers information useful later: a fact, someone's preference, a project, a lesson learned, a procedure. Write precise, self-contained sentences (who, what, when). Duplicates are merged automatically.",
		Params: core.Obj(map[string]any{
			"content":    core.Str("The memory, as a complete, self-contained sentence."),
			"kind":       core.Enum("Memory type.", kinds...),
			"subject":    core.Str("Short subject (person, project, tool…)."),
			"tags":       core.Str("Comma-separated keywords."),
			"importance": core.Int("1 (trivial) to 5 (critical). Default 3."),
		}, "content", "kind"),
		Summarize: summarizeField("Remembering: ", "content", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[memory.Memory](args)
			if err != nil {
				return "", err
			}
			a.Source = rc.Source + ":" + rc.RunID
			res, err := d.Mem.Save(ctx, a)
			if err != nil {
				return "", err
			}
			if res.Merged {
				return fmt.Sprintf("Memory #%d updated (merged with a similar memory).", res.ID), nil
			}
			return fmt.Sprintf("Memory #%d saved.", res.ID), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "memory_search", Category: "memory", Emoji: "🔍",
		Description: "Searches your long-term memory (hybrid semantic + keyword search). Use it before saying you don't know.",
		Params: core.Obj(map[string]any{
			"query": core.Str("What you're looking for."),
			"kind":  core.Enum("Filter by type (optional).", kinds...),
			"limit": core.Int("Max results (default 10)."),
		}, "query"),
		Summarize: summarizeField("Memories: ", "query", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Query, Kind string
				Limit       int
			}](args)
			if a.Limit <= 0 || a.Limit > 30 {
				a.Limit = 10
			}
			res := d.Mem.Search(ctx, a.Query, a.Kind, a.Limit)
			if len(res) == 0 {
				return "Nothing found in memory.", nil
			}
			var b strings.Builder
			for _, m := range res {
				fmt.Fprintf(&b, "#%d [%s%s] %s  (importance %d, updated %s)\n", m.ID, m.Kind, subj(m.Subject), m.Content, m.Importance, strings.Split(m.UpdatedAt, "T")[0])
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "memory_update", Category: "memory", Emoji: "✏️",
		Description: "Corrects or completes an existing memory (by its #id).",
		Params: core.Obj(map[string]any{
			"id":         core.Int("Memory ID."),
			"content":    core.Str("New content."),
			"importance": core.Int("New importance 1-5."),
			"tags":       core.Str("New tags."),
		}, "id"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[memory.Memory](args)
			if err != nil {
				return "", err
			}
			if err := d.Mem.Update(ctx, a.ID, a); err != nil {
				return "", err
			}
			return fmt.Sprintf("Memory #%d updated.", a.ID), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "memory_forget", Category: "memory", Emoji: "🫥",
		Description: "Forgets a wrong or outdated memory (by its #id), or when someone asks you to forget something.",
		Params:      core.Obj(map[string]any{"id": core.Int("Memory ID.")}, "id"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ ID int64 }](args)
			if err := d.Mem.Delete(a.ID); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "memory.forget", fmt.Sprint(a.ID), nil)
			return fmt.Sprintf("Memory #%d forgotten.", a.ID), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "person_note", Category: "memory", Emoji: "👤",
		Description: "Adds a note to your profile of a person (role, preferences, projects, communication style). Your notes are recalled automatically when they talk to you.",
		Params: core.Obj(map[string]any{
			"user":    core.Str("Slack ID (U…). Empty = the person talking to you."),
			"note":    core.Str("The note to add."),
			"replace": core.Str("Optional: rewrites ALL notes with this text (to clean up)."),
		}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ User, Note, Replace string }](args)
			user := strings.Trim(strings.TrimPrefix(a.User, "<@"), ">")
			if user == "" {
				user = rc.UserID
			}
			if user == "" {
				return "", fmt.Errorf("unknown person")
			}
			if a.Replace != "" {
				return "Profile rewritten.", d.Mem.SetPersonNotes(user, a.Replace)
			}
			return "Note added to the profile.", d.Mem.AppendPersonNote(user, a.Note)
		},
	})
}

func subj(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}
