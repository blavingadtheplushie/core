package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/scheduler"
)

func registerSchedule(d *Deps) {
	d.Reg.Add(&core.Tool{
		Name: "schedule_create", Category: "system", Emoji: "⏰",
		Description: "Schedules a task for yourself: a one-off reminder or a recurring task (cron). When it fires, you'll receive the `prompt` instruction and can use all your tools; your answer is posted in the given channel (default: the current conversation).",
		Params: core.Obj(map[string]any{
			"name":       core.Str("Short name."),
			"prompt":     core.Str("Instruction you'll execute (write it for your future self, with all the context)."),
			"cron":       core.Str("5-field cron expression, timezone " + d.Cfg.TZName + " (e.g. '0 9 * * 1-5'). For a recurring task."),
			"at":         core.Str("Local ISO date/time for a one-off task (e.g. 2026-11-02T14:30)."),
			"in_minutes": core.Int("Alternative to 'at': in N minutes."),
			"channel":    core.Str("Channel to post in (default: current conversation)."),
		}, "name", "prompt"),
		Summarize: summarizeField("Scheduling: ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Name, Prompt, Cron, At, Channel string
				InMinutes                       int `json:"in_minutes"`
			}](args)
			sc := scheduler.Schedule{Name: a.Name, Prompt: a.Prompt, Cron: a.Cron, RunAt: a.At, Channel: a.Channel, CreatedBy: rc.Actor()}
			if rc.Profile != nil && !rc.Profile.IsAdmin() {
				sc.Profile = rc.Profile.Name
			}
			if a.InMinutes > 0 {
				sc.RunAt = time.Now().In(d.Cfg.Timezone).Add(time.Duration(a.InMinutes) * time.Minute).Format(time.RFC3339)
			}
			if sc.Cron == "" && sc.RunAt == "" {
				return "", errors.New("provide cron, at or in_minutes")
			}
			if sc.Channel == "" {
				sc.Channel = rc.Channel
				sc.ThreadTS = firstNonEmpty(rc.ThreadTS, rc.MessageTS)
			} else if d.Slack != nil {
				if ch, err := d.resolveChannel(ctx, sc.Channel); err == nil {
					sc.Channel = ch
				}
			}
			id, next, err := scheduler.Create(d.St, d.Cfg.Timezone, sc)
			if err != nil {
				return "", err
			}
			if d.Sched != nil {
				d.Sched.Reload()
			}
			d.St.Audit(rc.Actor(), "schedule.create", id, map[string]string{"name": a.Name})
			return fmt.Sprintf("⏰ Task %s scheduled — next run: %s", id, next.In(d.Cfg.Timezone).Format("Mon Jan 2 2006 15:04")), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "schedule_list", Category: "system", Emoji: "📅",
		Description: "Lists your active scheduled tasks.",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			rows, err := d.St.Rows(`SELECT id, name, COALESCE(cron,'') AS cron, COALESCE(next_run_at,'') AS next, prompt FROM schedules WHERE enabled = 1 ORDER BY next_run_at`)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "No scheduled tasks.", nil
			}
			var b strings.Builder
			for _, r := range rows {
				next := fmt.Sprint(r["next"])
				if t, err := time.Parse(time.RFC3339, next); err == nil {
					next = t.In(d.Cfg.Timezone).Format("Jan 2 15:04")
				}
				fmt.Fprintf(&b, "- %v \"%v\" cron=%v next=%s — %s\n", r["id"], r["name"], r["cron"], next, short(fmt.Sprint(r["prompt"]), 100))
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "schedule_delete", Category: "system", Emoji: "🗑️",
		Description: "Cancels a scheduled task.",
		Params:      core.Obj(map[string]any{"id": core.Str("Task ID (sch_…).")}, "id"),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			id := field(args, "id")
			res, _ := d.St.DB.Exec(`DELETE FROM schedules WHERE id = ?`, id)
			if n, _ := res.RowsAffected(); n == 0 {
				return "", errors.New("task not found")
			}
			return "Task cancelled.", nil
		},
	})
}
