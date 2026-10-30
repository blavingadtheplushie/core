// Package scheduler runs recurring (cron) and one-shot agent tasks.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"blavingad/internal/store"
)

type Schedule struct {
	ID        string
	Name      string
	Cron      string
	RunAt     string
	Prompt    string
	Channel   string
	ThreadTS  string
	CreatedBy string
	// Profile is the access profile the task runs with ("" = full).
	Profile string
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Next computes the next fire time after `from` in the given location.
func Next(cronExpr, runAt string, from time.Time, loc *time.Location) (time.Time, error) {
	if strings.TrimSpace(cronExpr) != "" {
		sched, err := parser.Parse(strings.TrimSpace(cronExpr))
		if err != nil {
			return time.Time{}, err
		}
		return sched.Next(from.In(loc)), nil
	}
	if runAt != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04"} {
			if t, err := time.ParseInLocation(layout, runAt, loc); err == nil {
				return t, nil
			}
		}
		return time.Time{}, errors.New("invalid date (expected ISO 8601)")
	}
	return time.Time{}, errors.New("cron or date is required")
}

type Scheduler struct {
	st      *store.Store
	loc     *time.Location
	trigger func(Schedule)
	wake    chan struct{}
}

func New(st *store.Store, loc *time.Location, trigger func(Schedule)) *Scheduler {
	return &Scheduler{st: st, loc: loc, trigger: trigger, wake: make(chan struct{}, 1)}
}

func (s *Scheduler) Reload() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		s.tick()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

func (s *Scheduler) tick() {
	now := time.Now().UTC()
	rows, err := s.st.DB.Query(`SELECT id, name, COALESCE(cron,''), COALESCE(run_at,''), prompt, COALESCE(channel,''), COALESCE(thread_ts,''), COALESCE(created_by,''), COALESCE(profile,'')
		FROM schedules WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?`, now.Format(time.RFC3339))
	if err != nil {
		return
	}
	var due []Schedule
	for rows.Next() {
		var sc Schedule
		rows.Scan(&sc.ID, &sc.Name, &sc.Cron, &sc.RunAt, &sc.Prompt, &sc.Channel, &sc.ThreadTS, &sc.CreatedBy, &sc.Profile)
		due = append(due, sc)
	}
	rows.Close()
	for _, sc := range due {
		if sc.Cron != "" {
			next, err := Next(sc.Cron, "", now, s.loc)
			if err != nil {
				s.st.DB.Exec(`UPDATE schedules SET enabled = 0, last_status = ? WHERE id = ?`, "invalid cron", sc.ID)
				continue
			}
			s.st.DB.Exec(`UPDATE schedules SET last_run_at = ?, next_run_at = ? WHERE id = ?`, store.Now(), next.UTC().Format(time.RFC3339), sc.ID)
		} else {
			s.st.DB.Exec(`UPDATE schedules SET last_run_at = ?, next_run_at = NULL, enabled = 0 WHERE id = ?`, store.Now(), sc.ID)
		}
		slog.Info("schedule fired", "id", sc.ID, "name", sc.Name)
		go s.trigger(sc)
	}
}

// Create inserts a schedule and computes its first fire time.
func Create(st *store.Store, loc *time.Location, sc Schedule) (string, time.Time, error) {
	next, err := Next(sc.Cron, sc.RunAt, time.Now(), loc)
	if err != nil {
		return "", next, err
	}
	if sc.Cron == "" && next.Before(time.Now().Add(-time.Minute)) {
		return "", next, errors.New("that date is already in the past")
	}
	id := store.NewID("sch")
	_, err = st.DB.Exec(`INSERT INTO schedules(id, name, cron, run_at, prompt, channel, thread_ts, created_by, profile, next_run_at, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, sc.Name, sc.Cron, sc.RunAt, sc.Prompt, sc.Channel, sc.ThreadTS, sc.CreatedBy, sc.Profile, next.UTC().Format(time.RFC3339), store.Now())
	return id, next, err
}
