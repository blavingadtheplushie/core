// Package agent is BLAVINGAD's harness: prompt assembly, the tool-calling
// loop, approvals, parallel tool execution, context compaction, run tracing,
// sub-agents and post-run reflection.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/memory"
	"blavingad/internal/store"
)

type Runner struct {
	cfg       *config.Config
	st        *store.Store
	mem       *memory.Service
	llm       *llm.Client
	reg       *core.Registry
	bus       *core.Bus
	approvals *core.Approvals
	access    *core.Access

	mu     sync.Mutex
	active map[string]*activeRun
	wg     sync.WaitGroup
}

type activeRun struct {
	cancel   context.CancelFunc
	channel  string
	threadTS string
}

type Progress struct {
	// thinking | thought | recall | tool_start | tool_end | note
	Kind       string `json:"kind"`
	Tool       string `json:"tool,omitempty"`
	Emoji      string `json:"emoji,omitempty"`
	Label      string `json:"label,omitempty"`
	Text       string `json:"text,omitempty"` // thought: reasoning · tool_end: result · tool_denied: profile
	Args       string `json:"args,omitempty"` // tool_start: compact arguments
	Category   string `json:"category,omitempty"`
	Count      int    `json:"count,omitempty"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Iteration  int    `json:"iteration"`
}

type Request struct {
	Source      string
	Channel     string
	ChannelName string
	ThreadTS    string
	MessageTS   string
	UserID      string
	UserName    string
	Text        string
	History     []llm.Message
	Extra       string
	ParentID    string
	Depth       int
	AllowTools  []string
	// Profile restricts tools/memory for the person behind the run (nil =
	// full access, for system runs).
	Profile    *core.Profile
	NoReflect  bool
	OnProgress func(Progress)
	// OnStart receives the run id as soon as it exists.
	OnStart func(runID string)
}

type Result struct {
	RunID     string
	Output    string
	Status    string
	ToolCalls int
	Usage     llm.Usage
	Memories  int // memories recalled into the prompt
	Model     string
	// Reflecting is true when a background reflection was started; it ends
	// with a run.reflected event.
	Reflecting bool
}

func NewRunner(cfg *config.Config, st *store.Store, mem *memory.Service, client *llm.Client, reg *core.Registry, bus *core.Bus, ap *core.Approvals, access *core.Access) *Runner {
	r := &Runner{cfg: cfg, st: st, mem: mem, llm: client, reg: reg, bus: bus, approvals: ap, access: access, active: map[string]*activeRun{}}
	// Runs interrupted by a restart can never finish.
	st.DB.Exec(`UPDATE runs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, store.Now())
	r.registerDelegate()
	return r
}

func (r *Runner) Settings() Settings { return LoadSettings(r.st, r.cfg) }

func (r *Runner) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

// Wait blocks until all runs finish or the timeout elapses.
func (r *Runner) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (r *Runner) Cancel(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.active[runID]; ok {
		a.cancel()
		return true
	}
	return false
}

func (r *Runner) CancelThread(channel, thread string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.active {
		if a.channel == channel && (a.threadTS == thread || thread == "") {
			a.cancel()
			n++
		}
	}
	return n
}

func (r *Runner) step(runID, kind, name, input, output, status string, dur time.Duration, tokens int) {
	input, output = core.Secrets.Redact(input), core.Secrets.Redact(output)
	r.st.DB.Exec(`INSERT INTO run_steps(run_id, kind, name, input, output, status, duration_ms, tokens, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		runID, kind, name, input, output, status, dur.Milliseconds(), tokens, store.Now())
	r.bus.Publish("run.step", map[string]any{"run_id": runID, "kind": kind, "name": name, "status": status, "duration_ms": dur.Milliseconds()})
}

// Run executes one agent turn end-to-end.
func (r *Runner) Run(parent context.Context, req Request) (res Result) {
	s := r.Settings()
	res.RunID = store.NewID("run")
	res.Status = "running"
	r.wg.Add(1)
	defer r.wg.Done()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	r.mu.Lock()
	r.active[res.RunID] = &activeRun{cancel: cancel, channel: req.Channel, threadTS: firstNonEmpty(req.ThreadTS, req.MessageTS)}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.active, res.RunID)
		r.mu.Unlock()
	}()

	r.st.DB.Exec(`INSERT INTO runs(id, parent_id, source, channel, channel_name, thread_ts, message_ts, user_id, user_name, input, status, model, started_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, res.RunID, nullable(req.ParentID), req.Source, req.Channel, req.ChannelName, req.ThreadTS, req.MessageTS,
		req.UserID, req.UserName, req.Text, "running", s.Model, store.Now())
	r.bus.Publish("run.started", map[string]any{"id": res.RunID, "source": req.Source, "user": req.UserName, "input": short(req.Text, 200), "parent_id": req.ParentID})
	if req.OnStart != nil {
		req.OnStart(res.RunID)
	}
	progress := func(p Progress) {
		if req.OnProgress != nil {
			req.OnProgress(p)
		}
		r.bus.Publish("run.progress", map[string]any{"run_id": res.RunID, "progress": p})
	}

	prof := req.Profile
	if prof == nil {
		full := r.access.Full()
		prof = &full
	}
	rc := &core.RunContext{RunID: res.RunID, Source: req.Source, Channel: req.Channel, ChannelName: req.ChannelName, ThreadTS: req.ThreadTS,
		MessageTS: req.MessageTS, UserID: req.UserID, UserName: req.UserName, Depth: req.Depth, Profile: prof,
		Progress: func(line string) { progress(Progress{Kind: "note", Label: line}) }}

	var runErr error
	defer func() {
		if p := recover(); p != nil {
			runErr = fmt.Errorf("panic: %v", p)
			slog.Error("run panic", "err", p, "stack", string(debug.Stack()))
		}
		status := "done"
		errText := ""
		switch {
		case errors.Is(runErr, context.Canceled) || (runErr == nil && ctx.Err() != nil):
			status = "cancelled"
			if res.Output == "" {
				res.Output = "🛑 Stopped."
			}
		case runErr != nil:
			status = "error"
			errText = runErr.Error()
			if res.Output == "" {
				res.Output = "😵 Oops, I hit an error: " + short(errText, 400)
			}
		}
		res.Status = status
		res.Output = core.Secrets.Redact(res.Output)
		r.st.DB.Exec(`UPDATE runs SET output = ?, status = ?, error = ?, tool_calls = ?, prompt_tokens = ?, completion_tokens = ?, finished_at = ? WHERE id = ?`,
			res.Output, status, nullable(errText), res.ToolCalls, res.Usage.PromptTokens, res.Usage.CompletionTokens, store.Now(), res.RunID)
		r.bus.Publish("run.finished", map[string]any{"id": res.RunID, "status": status, "output": short(res.Output, 300), "tool_calls": res.ToolCalls})
	}()

	if !r.cfg.LLMConfigured() {
		runErr = errors.New("no model configured (LLM_BASE_URL / LLM_MODEL)")
		return
	}

	query := req.Text
	if len(req.History) > 0 {
		query = lastUserText(req.History) + "\n" + req.Text
	}
	sys, mems := r.systemPrompt(ctx, rc, query, req.Extra, s)
	res.Memories, res.Model = len(mems), s.Model
	if len(mems) > 0 {
		ids := make([]string, len(mems))
		for i, m := range mems {
			ids[i] = fmt.Sprint(m.ID)
		}
		r.step(res.RunID, "recall", "memory", query, strings.Join(ids, ","), "ok", 0, 0)
		progress(Progress{Kind: "recall", Count: len(mems)})
	}
	msgs := []llm.Message{{Role: "system", Content: sys}}
	msgs = append(msgs, req.History...)
	msgs = append(msgs, llm.Message{Role: "user", Content: req.Text})

	var requested map[string]bool
	if req.AllowTools != nil {
		requested = map[string]bool{}
		for _, t := range req.AllowTools {
			requested[t] = true
		}
	}
	deny := map[string]bool{}
	if req.Depth >= 2 {
		deny = map[string]bool{"delegate": true, "self_upgrade": true, "self_rollback": true}
	}
	rc.Allowed = map[string]bool{}
	for _, t := range r.reg.All() {
		if prof.Allows(t) && (requested == nil || requested[t.Name]) && !deny[t.Name] {
			rc.Allowed[t.Name] = true
		}
	}
	defs := r.reg.Definitions(rc.Allowed, nil)
	if !prof.IsAdmin() {
		r.step(res.RunID, "access", prof.Name, prof.Emoji+" "+prof.Label, fmt.Sprintf("%d tools allowed · memory %s", len(defs), prof.Memory), "ok", 0, 0)
	}

	maxIter := s.MaxIterations
	if req.Depth > 0 && maxIter > 20 {
		maxIter = 20
	}
	if prof.MaxIterations > 0 && prof.MaxIterations < maxIter {
		maxIter = prof.MaxIterations
	}
	temp := s.Temperature
	narration := "" // last text the model wrote next to tool calls
	for iter := 1; iter <= maxIter; iter++ {
		if ctx.Err() != nil {
			runErr = ctx.Err()
			return
		}
		r.st.DB.Exec(`UPDATE runs SET iterations = ? WHERE id = ?`, iter, res.RunID)
		msgs = r.compact(ctx, res.RunID, msgs, s)
		progress(Progress{Kind: "thinking", Iteration: iter})
		start := time.Now()
		resp, err := r.llm.Chat(ctx, llm.Request{Model: s.Model, Messages: msgs, Tools: defs, Temperature: &temp, MaxTokens: r.cfg.LLMMaxTokens})
		if err != nil {
			r.step(res.RunID, "llm", s.Model, "", err.Error(), "error", time.Since(start), 0)
			runErr = err
			return
		}
		res.Usage.PromptTokens += resp.Usage.PromptTokens
		res.Usage.CompletionTokens += resp.Usage.CompletionTokens
		var names []string
		for _, tc := range resp.Message.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		r.step(res.RunID, "llm", s.Model, strings.Join(names, ", "), resp.Message.Content, "ok", time.Since(start), resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
		resp.Message.Content = core.Secrets.Redact(resp.Message.Content)
		msgs = append(msgs, resp.Message)
		// Surface the model's thinking: native reasoning when the provider
		// exposes it, otherwise the narration it writes next to tool calls.
		if thought := firstNonEmpty(resp.Reasoning, map[bool]string{true: resp.Message.Content}[len(resp.Message.ToolCalls) > 0]); thought != "" {
			thought = core.Secrets.Redact(thought)
			r.step(res.RunID, "thought", "thinking", "", thought, "ok", 0, 0)
			progress(Progress{Kind: "thought", Text: thought, Iteration: iter})
		}

		if len(resp.Message.ToolCalls) > 0 && strings.TrimSpace(resp.Message.Content) != "" {
			narration = resp.Message.Content
		}
		if len(resp.Message.ToolCalls) == 0 {
			res.Output = strings.TrimSpace(resp.Message.Content)
			if res.Output == "" {
				res.Output = r.recoverEmpty(ctx, res.RunID, s, msgs[:len(msgs)-1], resp, narration, &res.Usage)
			}
			break
		}
		results := r.execTools(ctx, rc, resp.Message.ToolCalls, progress, iter)
		res.ToolCalls += len(results)
		for i, tc := range resp.Message.ToolCalls {
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: results[i]})
		}
		if iter == maxIter {
			msgs = append(msgs, llm.Message{Role: "user", Content: "[System] Step limit reached. Answer now without calling any tool: summarize what you did, the result, and what is left to do."})
			resp, err := r.llm.Chat(ctx, llm.Request{Model: s.Model, Messages: msgs, Temperature: &temp, MaxTokens: r.cfg.LLMMaxTokens})
			if err != nil {
				runErr = err
				return
			}
			res.Usage.PromptTokens += resp.Usage.PromptTokens
			res.Usage.CompletionTokens += resp.Usage.CompletionTokens
			res.Output = strings.TrimSpace(core.Secrets.Redact(resp.Message.Content))
			if res.Output == "" {
				res.Output = r.recoverEmpty(ctx, res.RunID, s, msgs, resp, narration, &res.Usage)
			}
		}
	}

	if s.Reflection && !req.NoReflect && req.Depth == 0 && req.Source != "self" && prof.Memory == "full" {
		res.Reflecting = true
		go r.reflect(res.RunID, rc, msgs)
	}
	return
}

// recoverEmpty handles a final reply with no text. Models do this when their
// token budget went into reasoning (finish_reason length/max_tokens), when
// Claude decides it already "said" everything before its tool calls, or when
// a gateway puts the text somewhere unexpected. The raw reply is recorded in
// the trace, then the model is asked once more — without tools, with a
// bigger budget if it ran out — before falling back to its reasoning or its
// last narration.
func (r *Runner) recoverEmpty(ctx context.Context, runID string, s Settings, history []llm.Message, empty *llm.Response, narration string, usage *llm.Usage) string {
	finish := firstNonEmpty(empty.FinishReason, "unknown")
	diag := fmt.Sprintf("finish_reason=%s · reasoning=%d chars · completion_tokens=%d\n\nraw response:\n%s",
		finish, len(empty.Reasoning), empty.Usage.CompletionTokens, empty.Raw)
	r.step(runID, "llm", "empty reply", "", diag, "error", 0, 0)
	slog.Warn("empty model reply, asking again", "run", runID, "model", s.Model, "finish_reason", finish, "raw", core.Secrets.Redact(clip(empty.Raw, 1500)))

	maxTokens := r.cfg.LLMMaxTokens
	if finish == "length" || finish == "max_tokens" {
		maxTokens = min(max(maxTokens, 4096)*2, 64000)
	}
	msgs := append(append([]llm.Message{}, history...), llm.Message{Role: "user",
		Content: "[System] Your last reply was empty. Write your final answer to the user now, as plain text, based on what you did above. Do not call any tool."})
	temp := s.Temperature
	start := time.Now()
	resp, err := r.llm.Chat(ctx, llm.Request{Model: s.Model, Messages: msgs, Temperature: &temp, MaxTokens: maxTokens})
	if err == nil {
		usage.PromptTokens += resp.Usage.PromptTokens
		usage.CompletionTokens += resp.Usage.CompletionTokens
		out := core.Secrets.Redact(strings.TrimSpace(resp.Message.Content))
		r.step(runID, "llm", s.Model+" (retry)", "", out, map[bool]string{true: "ok", false: "error"}[out != ""], time.Since(start), resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
		if out != "" {
			return out
		}
		if strings.TrimSpace(resp.Reasoning) != "" {
			return core.Secrets.Redact(strings.TrimSpace(resp.Reasoning))
		}
	} else {
		r.step(runID, "llm", s.Model+" (retry)", "", err.Error(), "error", time.Since(start), 0)
	}
	if strings.TrimSpace(empty.Reasoning) != "" {
		return core.Secrets.Redact(strings.TrimSpace(empty.Reasoning))
	}
	if strings.TrimSpace(narration) != "" {
		return strings.TrimSpace(narration)
	}
	return fmt.Sprintf("🤷 The model finished without writing an answer (finish reason: %s). The raw reply is in the trace.", finish)
}

// execTools runs a batch of tool calls. Calls that need no approval run in
// parallel; approval-gated calls run one by one so prompts stay readable.
func (r *Runner) execTools(ctx context.Context, rc *core.RunContext, calls []llm.ToolCall, progress func(Progress), iter int) []string {
	results := make([]string, len(calls))
	var wg sync.WaitGroup
	var gated []int
	for i, tc := range calls {
		if t, ok := r.reg.Get(tc.Function.Name); ok && r.needsApproval(t, json.RawMessage(tc.Function.Arguments)) {
			gated = append(gated, i)
			continue
		}
		wg.Add(1)
		go func(i int, tc llm.ToolCall) {
			defer wg.Done()
			results[i] = r.execTool(ctx, rc, tc, progress, iter)
		}(i, tc)
	}
	wg.Wait()
	for _, i := range gated {
		results[i] = r.execTool(ctx, rc, calls[i], progress, iter)
	}
	return results
}

func (r *Runner) needsApproval(t *core.Tool, args json.RawMessage) bool {
	if r.reg.Policy(t) != core.PolicyApprove {
		return false
	}
	return t.NeedsApproval == nil || t.NeedsApproval(args)
}

func (r *Runner) execTool(ctx context.Context, rc *core.RunContext, tc llm.ToolCall, progress func(Progress), iter int) (out string) {
	name := tc.Function.Name
	args := json.RawMessage(tc.Function.Arguments)
	start := time.Now()
	t, ok := r.reg.Get(name)
	if !ok || !t.IsAvailable() {
		out = fmt.Sprintf("Error: the tool %q does not exist or is not configured.", name)
		r.step(rc.RunID, "tool", name, string(args), out, "error", 0, 0)
		return
	}
	if rc.Allowed != nil && !rc.Allowed[name] {
		out = fmt.Sprintf("Denied: the tool %q is not allowed for this person's access profile (%s). This is an access rule, not a filesystem or vault permission problem: say so plainly, do without it, or suggest they ask an admin for access.", name, rc.Profile.Label)
		r.step(rc.RunID, "tool", name, string(args), out, "denied", 0, 0)
		progress(Progress{Kind: "tool_denied", Tool: name, Emoji: t.Emoji, Label: t.Label(args), Text: rc.Profile.Emoji + " " + rc.Profile.Label, Iteration: iter})
		return
	}
	label := core.Secrets.Redact(t.Label(args))
	progress(Progress{Kind: "tool_start", Tool: name, Emoji: t.Emoji, Label: label, Category: t.Category, Args: core.Secrets.Redact(compactArgs(args)), Iteration: iter})
	status := "ok"
	defer func() {
		if p := recover(); p != nil {
			out = fmt.Sprintf("Internal tool error: %v", p)
			status = "error"
			slog.Error("tool panic", "tool", name, "err", p, "stack", string(debug.Stack()))
		}
		// Whatever a tool printed, known secrets never reach the model.
		out = core.Secrets.Redact(out)
		r.step(rc.RunID, "tool", name, string(args), out, status, time.Since(start), 0)
		progress(Progress{Kind: "tool_end", Tool: name, Emoji: t.Emoji, Label: label, Category: t.Category, OK: status == "ok", Text: clip(out, 1500), DurationMs: time.Since(start).Milliseconds(), Iteration: iter})
	}()

	if r.reg.Policy(t) == core.PolicyOff {
		status = "error"
		return "This tool has been disabled by an admin."
	}
	if r.needsApproval(t, args) {
		astart := time.Now()
		err := r.approvals.Request(ctx, rc, t, args)
		decision, note := "approved", ""
		if err != nil {
			decision, note = "denied", err.Error()
		}
		r.step(rc.RunID, "approval", name, string(args), note, decision, time.Since(astart), 0)
		if err != nil {
			status = "denied"
			if errors.Is(err, core.ErrDenied) {
				return "❌ A human denied this action. Don't try to work around it; offer an alternative or ask what they prefer."
			}
			return "❌ Action not approved: " + err.Error()
		}
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	result, err := t.Handler(tctx, rc, args)
	if err != nil {
		status = "error"
		return "Error: " + clip(err.Error(), 12000)
	}
	if strings.TrimSpace(result) == "" {
		result = "(ok, no output)"
	}
	return clip(result, 16000)
}

// compact keeps the conversation inside the context window: first old tool
// outputs are shrunk, then the middle of the conversation is summarised.
func (r *Runner) compact(ctx context.Context, runID string, msgs []llm.Message, s Settings) []llm.Message {
	limit := int(float64(r.cfg.LLMContextLimit) * 0.7)
	if llm.EstimateTokens(msgs) < limit {
		return msgs
	}
	for i := 1; i < len(msgs)-6; i++ {
		if msgs[i].Role == "tool" && len(msgs[i].Content) > 1500 {
			msgs[i].Content = clip(msgs[i].Content, 1500) + "\n[output shortened to save context]"
		}
	}
	if llm.EstimateTokens(msgs) < limit || len(msgs) < 12 {
		return msgs
	}
	cut := len(msgs) - 8
	for cut > 2 && msgs[cut].Role == "tool" {
		cut--
	}
	middle := msgs[1:cut]
	var b strings.Builder
	for _, m := range middle {
		switch {
		case m.Role == "tool":
			fmt.Fprintf(&b, "[result %s] %s\n", m.Name, clip(m.Content, 600))
		case len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "[call %s] %s\n", tc.Function.Name, clip(tc.Function.Arguments, 300))
			}
			if m.Content != "" {
				fmt.Fprintf(&b, "[assistant] %s\n", clip(m.Content, 800))
			}
		default:
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, clip(m.Content, 1500))
		}
	}
	start := time.Now()
	summary := clip(b.String(), 12000)
	resp, err := r.llm.Chat(ctx, llm.Request{Model: s.FastModel, MaxTokens: 1500, Temperature: llm.F(0.2), Messages: []llm.Message{
		{Role: "system", Content: "Faithfully summarize this part of an AI agent's conversation: goals, decisions, facts discovered, important files/URLs/IDs, what is left to do. Dense bullet list."},
		{Role: "user", Content: summary},
	}})
	if err == nil && resp.Message.Content != "" {
		summary = resp.Message.Content
	}
	r.step(runID, "compact", "contexte", fmt.Sprintf("%d messages", len(middle)), summary, "ok", time.Since(start), 0)
	out := []llm.Message{msgs[0], {Role: "user", Content: "[Automatic summary of the previous exchanges]\n" + summary}}
	if msgs[cut].Role == "assistant" {
		out = append(out, llm.Message{Role: "user", Content: "(continued)"})
	}
	return append(out, msgs[cut:]...)
}

func (r *Runner) registerDelegate() {
	r.reg.Add(&core.Tool{
		Name: "delegate", Category: "system", Emoji: "🧑‍🚀",
		Description: "Hand a subtask to a sub-agent that has the same tools as you, works autonomously and then reports back. Call it several times in the same turn to parallelize (e.g. research on several topics).",
		Params: core.Obj(map[string]any{
			"task":  core.Str("Complete, self-contained instructions for the sub-agent (it does not see your conversation)."),
			"tools": core.StrList("Optional: restrict it to these tools."),
		}, "task"),
		Summarize: func(args json.RawMessage) string {
			var a struct{ Task string }
			json.Unmarshal(args, &a)
			return "Sub-agent: " + short(a.Task, 70)
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Task  string   `json:"task"`
				Tools []string `json:"tools"`
			}](args)
			if err != nil {
				return "", err
			}
			if rc.Depth >= 2 {
				return "", errors.New("maximum delegation depth reached")
			}
			// The sub-agent never gets more than its parent.
			var allowed []string
			for name := range rc.Allowed {
				if len(a.Tools) == 0 || slices.Contains(a.Tools, name) {
					allowed = append(allowed, name)
				}
			}
			res := r.Run(ctx, Request{
				Source: "subagent", Channel: rc.Channel, ChannelName: rc.ChannelName, ThreadTS: rc.ThreadTS, MessageTS: rc.MessageTS,
				UserID: rc.UserID, UserName: rc.UserName, Text: a.Task, ParentID: rc.RunID, Depth: rc.Depth + 1,
				AllowTools: allowed, Profile: rc.Profile, NoReflect: true,
				OnProgress: func(p Progress) {
					if p.Kind == "tool_start" && rc.Progress != nil {
						rc.Progress("↳ " + p.Emoji + " " + p.Label)
					}
				},
			})
			if res.Status != "done" {
				return "", fmt.Errorf("the sub-agent failed (%s): %s", res.Status, res.Output)
			}
			return fmt.Sprintf("Sub-agent report (%d tools used):\n\n%s", res.ToolCalls, res.Output), nil
		},
	})
}

// compactArgs renders tool arguments on one line for humans.
func compactArgs(args json.RawMessage) string {
	var v any
	if json.Unmarshal(args, &v) != nil {
		return clip(string(args), 600)
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(v)
	return clip(string(b), 600)
}

func lastUserText(h []llm.Message) string {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "user" {
			return h[i].Content
		}
	}
	return ""
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func short(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := max * 2 / 3
	return s[:head] + fmt.Sprintf("\n…[%d characters omitted]…\n", len(s)-max) + s[len(s)-(max-head):]
}
