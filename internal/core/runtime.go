package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"blavingad/internal/store"
)

// RunContext is what a tool sees about the run invoking it.
type RunContext struct {
	RunID       string
	Source      string // slack | web | email | schedule | self | subagent
	Channel     string
	ChannelName string
	ThreadTS    string
	MessageTS   string
	UserID      string
	UserName    string
	Depth       int
	// Profile is the access profile of the person behind the run; nil means
	// a system run with full access.
	Profile *Profile
	// Allowed is the resolved tool allowlist, re-checked at execution time.
	Allowed map[string]bool
	// Progress lets long tools stream a status line to the surface.
	Progress func(line string)
}

// CanManageAccess is true for runs allowed to change the whitelist:
// admin-level profiles and web-dashboard sessions, never autonomous runs.
func (rc *RunContext) CanManageAccess() bool {
	if rc.Source == "self" || rc.Source == "email" {
		return false
	}
	if rc.Source == "web" {
		return true
	}
	return rc.Profile != nil && rc.Profile.IsAdmin()
}

func (rc *RunContext) Actor() string {
	if rc.UserName != "" {
		return rc.UserName
	}
	if rc.UserID != "" {
		return rc.UserID
	}
	return rc.Source
}

// ---------- event bus ----------

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
	At   string `json:"at"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

func (b *Bus) Publish(typ string, data any) {
	ev := Event{Type: typ, Data: data, At: store.Now()}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the agent
		}
	}
}

func (b *Bus) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// ---------- approvals ----------

type Approval struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	Tool        string `json:"tool"`
	Args        string `json:"args"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	RequestedBy string `json:"requested_by"`
	RequesterID string `json:"-"`
	DecidedBy   string `json:"decided_by"`
	Channel     string `json:"-"`
	ThreadTS    string `json:"-"`
	CreatedAt   string `json:"created_at"`
}

// ApprovalNotifier is implemented by surfaces that can show approval
// prompts (Slack posts interactive buttons).
type ApprovalNotifier interface {
	NotifyApproval(a *Approval) (channel, ts string)
	ResolveApproval(a *Approval, channel, ts string)
}

var ErrDenied = errors.New("action denied by a human")

type Approvals struct {
	st       *store.Store
	bus      *Bus
	reg      *Registry
	mu       sync.Mutex
	waiters  map[string]chan string
	notifier ApprovalNotifier
	Timeout  time.Duration
}

func NewApprovals(st *store.Store, bus *Bus, reg *Registry) *Approvals {
	a := &Approvals{st: st, bus: bus, reg: reg, waiters: map[string]chan string{}, Timeout: 30 * time.Minute}
	// Anything pending at boot belonged to a previous process.
	st.DB.Exec(`UPDATE approvals SET status = 'expired', decided_at = ? WHERE status = 'pending'`, store.Now())
	return a
}

func (a *Approvals) SetNotifier(n ApprovalNotifier) { a.notifier = n }

// Request blocks until a human decides, the timeout elapses or ctx ends.
func (a *Approvals) Request(ctx context.Context, rc *RunContext, tool *Tool, args json.RawMessage) error {
	ap := &Approval{
		ID: store.NewID("apr"), RunID: rc.RunID, Tool: tool.Name, Args: string(args),
		Summary: tool.Label(args), Status: "pending", RequestedBy: rc.Actor(), RequesterID: rc.UserID,
		Channel: rc.Channel, ThreadTS: rc.ThreadTS, CreatedAt: store.Now(),
	}
	if rc.ThreadTS == "" {
		ap.ThreadTS = rc.MessageTS
	}
	ch := make(chan string, 1)
	a.mu.Lock()
	a.waiters[ap.ID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.waiters, ap.ID)
		a.mu.Unlock()
	}()
	if _, err := a.st.DB.Exec(`INSERT INTO approvals(id, run_id, tool, args, summary, status, requested_by, requester_id, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		ap.ID, ap.RunID, ap.Tool, ap.Args, ap.Summary, ap.Status, ap.RequestedBy, ap.RequesterID, ap.CreatedAt); err != nil {
		return err
	}
	var slackChannel, slackTS string
	if a.notifier != nil {
		slackChannel, slackTS = a.notifier.NotifyApproval(ap)
		if slackTS != "" {
			a.st.DB.Exec(`UPDATE approvals SET slack_channel = ?, slack_ts = ? WHERE id = ?`, slackChannel, slackTS, ap.ID)
		}
	}
	a.bus.Publish("approval.created", ap)
	if rc.Progress != nil {
		rc.Progress(fmt.Sprintf("✋ Waiting for approval: %s", ap.Summary))
	}

	var decision string
	timer := time.NewTimer(a.Timeout)
	defer timer.Stop()
	select {
	case decision = <-ch:
	case <-timer.C:
		decision = "expired"
		a.st.DB.Exec(`UPDATE approvals SET status = 'expired', decided_at = ? WHERE id = ? AND status = 'pending'`, store.Now(), ap.ID)
	case <-ctx.Done():
		decision = "expired"
		a.st.DB.Exec(`UPDATE approvals SET status = 'expired', decided_at = ? WHERE id = ? AND status = 'pending'`, store.Now(), ap.ID)
	}
	ap.Status = decision
	if a.notifier != nil && slackTS != "" {
		var by string
		a.st.DB.QueryRow(`SELECT COALESCE(decided_by,'') FROM approvals WHERE id = ?`, ap.ID).Scan(&by)
		ap.DecidedBy = by
		a.notifier.ResolveApproval(ap, slackChannel, slackTS)
	}
	a.bus.Publish("approval.decided", ap)
	switch decision {
	case "approved":
		return nil
	case "expired":
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("approval expired (nobody answered)")
	default:
		return ErrDenied
	}
}

// Tool returns the tool name of an approval ("" if unknown).
func (a *Approvals) Tool(id string) string {
	var tool string
	a.st.DB.QueryRow(`SELECT tool FROM approvals WHERE id = ?`, id).Scan(&tool)
	return tool
}

// Decide records a decision. "always" approves and flips the tool policy to
// auto for future calls.
func (a *Approvals) Decide(id, decision, by string) error {
	status := "denied"
	switch decision {
	case "approve", "approved":
		status = "approved"
	case "always":
		status = "approved"
	}
	res, err := a.st.DB.Exec(`UPDATE approvals SET status = ?, decided_by = ?, decided_at = ? WHERE id = ? AND status = 'pending'`, status, by, store.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("this approval is no longer pending")
	}
	if decision == "always" {
		var tool string
		a.st.DB.QueryRow(`SELECT tool FROM approvals WHERE id = ?`, id).Scan(&tool)
		if err := a.reg.SetPolicy(tool, PolicyAuto); err != nil {
			slog.Warn("set policy", "err", err)
		}
		a.st.Audit(by, "policy.auto", tool, map[string]string{"via": "approval"})
	}
	a.st.Audit(by, "approval."+status, id, nil)
	a.mu.Lock()
	ch, ok := a.waiters[id]
	a.mu.Unlock()
	if ok {
		ch <- status
	}
	return nil
}

// Requester returns the Slack id of whoever triggered the run (used to
// decide who may click when no admins are configured).
func (a *Approvals) Requester(id string) string {
	var requester string
	a.st.DB.QueryRow(`SELECT COALESCE(requester_id,'') FROM approvals WHERE id = ?`, id).Scan(&requester)
	return requester
}
