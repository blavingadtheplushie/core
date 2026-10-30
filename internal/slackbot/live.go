package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"blavingad/internal/agent"
)

// liveMessage is the Block Kit reply that streams the harness while it works
// and then turns into the final answer.
//
// Everything the agent did — recalled memories, each thinking step, each
// tool call with its arguments and result, and the post-run reflection — is
// kept as a chronological timeline and rendered as a native Slack `plan`
// block (task cards, collapsible) at the top of the message, so the answer
// comes first visually and the details stay out of the way. Workspaces that
// reject plan blocks get a compact classic layout instead.
type liveMessage struct {
	b       *Bot
	channel string
	thread  string
	ts      string
	runID   string

	mu        sync.Mutex
	events    []*event
	iteration int
	version   int
	dirty     bool
	stop      chan struct{}
}

type event struct {
	kind     string // recall | thought | tool | reflection
	key      string
	tool     string
	category string
	emoji    string
	title    string
	details  string
	output   string
	status   string // in_progress | complete | error
	denied   bool
}

// Results of these categories are not shown in the plan: in a channel they
// would expose memories, secrets, emails or the whitelist to everyone.
var privateOutput = map[string]bool{"memory": true, "vault": true, "email": true, "access": true}

func newLiveMessage(b *Bot, channel, thread string) *liveMessage {
	return &liveMessage{b: b, channel: channel, thread: thread, stop: make(chan struct{})}
}

// ---------- small block helpers ----------

func mrkdwn(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, text, false, false)
}

func plain(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, text, true, false)
}

func section(text string) slack.Block { return slack.NewSectionBlock(mrkdwn(text), nil, nil) }

func contextLine(texts ...string) slack.Block {
	var els []slack.MixedElement
	for _, t := range texts {
		if t != "" {
			els = append(els, mrkdwn(t))
		}
	}
	return slack.NewContextBlock("", els...)
}

func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// rawBlock lets us emit blocks exactly as documented (plan tasks have no
// "type" field, which slack-go's TaskCardBlock always sets).
type rawBlock struct {
	kind string
	id   string
	body map[string]any
}

func (r rawBlock) BlockType() slack.MessageBlockType { return slack.MessageBlockType(r.kind) }
func (r rawBlock) ID() string                        { return r.id }
func (r rawBlock) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": r.kind}
	if r.id != "" {
		m["block_id"] = r.id
	}
	for k, v := range r.body {
		m[k] = v
	}
	return json.Marshal(m)
}

// richText builds a rich_text object: plain paragraphs, or a preformatted
// block for code-like content (arguments, tool output).
func richText(text string, pre bool) map[string]any {
	kind := "rich_text_section"
	if pre {
		kind = "rich_text_preformatted"
	}
	return map[string]any{"type": "rich_text", "elements": []any{
		map[string]any{"type": kind, "elements": []any{map[string]any{"type": "text", "text": text}}},
	}}
}

// ---------- progress ----------

func (l *liveMessage) start(ctx context.Context) {
	opts := []slack.MsgOption{slack.MsgOptionText("🤖 Working on it…", false)}
	if l.thread != "" {
		opts = append(opts, slack.MsgOptionTS(l.thread))
	}
	if _, ts, err := l.b.send(ctx, l.channel, "", func(plan bool) []slack.Block { return l.workingBlocks(plan) }, opts...); err == nil {
		l.ts = ts
	}
	go func() {
		t := time.NewTicker(1500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				l.mu.Lock()
				dirty := l.dirty
				l.dirty = false
				l.mu.Unlock()
				if dirty && l.ts != "" {
					l.b.send(context.Background(), l.channel, l.ts, func(plan bool) []slack.Block { return l.workingBlocks(plan) }, slack.MsgOptionText("🤖 Working on it…", false))
				}
			}
		}
	}()
}

func (l *liveMessage) progress(p agent.Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch p.Kind {
	case "thinking":
		l.iteration = p.Iteration
	case "recall":
		if p.Count > 0 {
			l.events = append(l.events, &event{kind: "recall", emoji: "🧠", status: "complete",
				title: fmt.Sprintf("Recalled %d memor%s", p.Count, map[bool]string{true: "ies", false: "y"}[p.Count > 1])})
		}
	case "thought":
		l.events = append(l.events, &event{kind: "thought", emoji: "💭", title: "Thinking", details: p.Text, status: "complete"})
	case "tool_start":
		l.events = append(l.events, &event{kind: "tool", key: p.Tool + "|" + p.Label, tool: p.Tool, category: p.Category, emoji: p.Emoji,
			title: p.Label, details: p.Args, status: "in_progress"})
	case "tool_end":
		for i := len(l.events) - 1; i >= 0; i-- {
			if e := l.events[i]; e.kind == "tool" && e.key == p.Tool+"|"+p.Label && e.status == "in_progress" {
				e.status = "error"
				if p.OK {
					e.status = "complete"
				}
				e.output = p.Text
				break
			}
		}
	case "tool_denied":
		l.events = append(l.events, &event{kind: "tool", tool: p.Tool, emoji: p.Emoji, title: p.Label, status: "error", denied: true,
			output: "Not allowed for your access profile (" + p.Text + ")"})
	case "note":
		// Approval waits, sub-agent steps…: attach to the running tool.
		for i := len(l.events) - 1; i >= 0; i-- {
			if e := l.events[i]; e.kind == "tool" && e.status == "in_progress" {
				e.output = strings.TrimSpace(e.output + "\n" + p.Label)
				break
			}
		}
	}
	l.dirty = true
}

// ---------- plan rendering ----------

func (e *event) taskTitle() string {
	title := e.title
	if title == "" || title == e.tool {
		title = e.tool
	}
	if e.emoji != "" {
		title = e.emoji + " " + title
	}
	if e.denied {
		title = "🚫 " + title
	}
	return clipRunes(title, 140)
}

func (l *liveMessage) task(i int, e *event) map[string]any {
	t := map[string]any{"task_id": fmt.Sprintf("t%d", i+1), "title": e.taskTitle(), "status": e.status}
	if d := strings.TrimSpace(e.details); d != "" {
		t["details"] = richText(clipRunes(d, 1500), e.kind == "tool")
	}
	out := strings.TrimSpace(e.output)
	if e.kind == "tool" && privateOutput[e.category] && !e.denied && out != "" {
		out = "(result hidden in Slack — see the trace)"
	}
	if out != "" {
		t["output"] = richText(clipRunes(out, 1200), e.kind == "tool" && !e.denied && !strings.HasPrefix(out, "("))
	}
	return t
}

func (l *liveMessage) countTools() (tools, thoughts int) {
	for _, e := range l.events {
		switch e.kind {
		case "tool":
			tools++
		case "thought":
			thoughts++
		}
	}
	return
}

// planBlock renders the timeline; extra events (the reflection) go last.
func (l *liveMessage) planBlock(title string, extra ...*event) slack.Block {
	events := append(append([]*event{}, l.events...), extra...)
	var tasks []any
	if len(events) > 50 {
		// Plans hold 50 tasks: fold the oldest ones into a single line.
		folded := len(events) - 49
		tasks = append(tasks, map[string]any{"task_id": "t0", "title": fmt.Sprintf("… %d earlier steps", folded), "status": "complete"})
		events = events[folded:]
	}
	for i, e := range events {
		tasks = append(tasks, l.task(i, e))
	}
	if len(tasks) == 0 {
		tasks = append(tasks, map[string]any{"task_id": "t1", "title": "💭 Thinking…", "status": "in_progress"})
	}
	// Stable ID per run: Slack keeps the reflection block open across updates.
	return rawBlock{kind: "plan", id: fmt.Sprintf("plan-%s", l.runID), body: map[string]any{"title": title, "tasks": tasks}}
}

func (l *liveMessage) workingBlocks(plan bool) []slack.Block {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !plan {
		return l.legacyWorking()
	}
	title := "Working on it…"
	if l.iteration > 1 {
		title = fmt.Sprintf("Working on it… (step %d)", l.iteration)
	}
	return []slack.Block{l.planBlock(title)}
}

func doneTitle(tools, thoughts int, status string) string {
	switch status {
	case "error":
		return "Stopped on an error"
	case "cancelled":
		return "Stopped"
	}
	var parts []string
	if thoughts > 0 {
		parts = append(parts, fmt.Sprintf("%d thought%s", thoughts, plural(thoughts)))
	}
	if tools > 0 {
		parts = append(parts, fmt.Sprintf("%d tool call%s", tools, plural(tools)))
	}
	if len(parts) == 0 {
		return "Thought process"
	}
	return "Thought process · " + strings.Join(parts, " · ")
}

// reflectionEvent describes the background reflection: nil when none runs.
func reflectionEvent(reflecting bool, done bool, lines []string) *event {
	if !reflecting {
		return nil
	}
	e := &event{kind: "reflection", emoji: "💭", title: "Reflection", status: "in_progress", output: "Reflecting on this conversation…"}
	if done {
		e.status = "complete"
		if len(lines) == 0 {
			e.output = "Nothing new worth remembering."
		} else {
			var b strings.Builder
			fmt.Fprintf(&b, "Remembered %d thing%s:", len(lines), plural(len(lines)))
			for _, l := range lines {
				b.WriteString("\n• " + clipRunes(l, 220))
			}
			e.output = b.String()
		}
	}
	return e
}

// finalBlocks lays out the answer: the collapsed plan on top, then the text.
func (l *liveMessage) finalBlocks(res agent.Result, chunks []string, refl *event, plan bool) []slack.Block {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !plan {
		return l.legacyFinal(res, chunks)
	}
	var blocks []slack.Block
	tools, thoughts := l.countTools()
	if len(l.events) > 0 || refl != nil || res.Status != "done" {
		var extra []*event
		if refl != nil {
			extra = append(extra, refl)
		}
		blocks = append(blocks, l.planBlock(doneTitle(tools, thoughts, res.Status), extra...))
	}
	for _, c := range chunks {
		blocks = append(blocks, section(c))
	}
	return blocks
}

// ---------- classic layout (workspaces without plan blocks) ----------

func (l *liveMessage) legacyWorking() []slack.Block {
	blocks := []slack.Block{contextLine(fmt.Sprintf("🤖  *%s* is working on it…", l.b.cfg.AgentName))}
	events := l.events
	if len(events) > 8 {
		blocks = append(blocks, contextLine(fmt.Sprintf("_… %d earlier steps_", len(events)-8)))
		events = events[len(events)-8:]
	}
	for _, e := range events {
		icon := map[string]string{"in_progress": "⏳", "complete": "✅", "error": "⚠️"}[e.status]
		if e.denied {
			icon = "🚫"
		}
		line := icon + "  " + escape(e.taskTitle())
		if e.kind == "thought" {
			line = "💭  _" + escape(clipRunes(e.details, 200)) + "_"
		}
		blocks = append(blocks, contextLine(line))
	}
	return blocks
}

func (l *liveMessage) legacyFinal(res agent.Result, chunks []string) []slack.Block {
	var blocks []slack.Block
	switch res.Status {
	case "error":
		blocks = append(blocks, contextLine("⚠️  *Something went wrong* — details in the trace."))
	case "cancelled":
		blocks = append(blocks, contextLine("⏹  *Stopped* before the end."))
	}
	for _, c := range chunks {
		blocks = append(blocks, section(c))
	}
	if tools, _ := l.countTools(); tools > 0 {
		blocks = append(blocks, contextLine(fmt.Sprintf("🛠️ %d tool call%s", tools, plural(tools))))
	}
	return blocks
}

// ---------- finish ----------

// finish replaces the live layout with the answer and returns the ts of
// the message carrying the plan (the one to update after reflection).
func (l *liveMessage) finish(ctx context.Context, res agent.Result) string {
	close(l.stop)
	chunks := Chunk(ToMrkdwn(res.Output), 2900)
	if len(chunks) == 0 {
		chunks = []string{"🤷"}
	}
	refl := reflectionEvent(res.Reflecting, false, nil)
	// Slack caps a message at 50 blocks: long answers spill over into
	// follow-up messages; the plan stays on the first one.
	const perMessage = 40
	var firstTS string
	for i := 0; i < len(chunks); i += perMessage {
		end := min(i+perMessage, len(chunks))
		part := chunks[i:end]
		first := i == 0
		build := func(plan bool) []slack.Block {
			if first {
				return l.finalBlocks(res, part, refl, plan)
			}
			var blocks []slack.Block
			for _, c := range part {
				blocks = append(blocks, section(c))
			}
			return blocks
		}
		fallback := slack.MsgOptionText(short(res.Output, 300), false)
		if first && l.ts != "" {
			if _, _, err := l.b.send(ctx, l.channel, l.ts, build, fallback); err == nil {
				firstTS = l.ts
				continue
			}
		}
		opts := []slack.MsgOption{fallback}
		if l.thread != "" {
			opts = append(opts, slack.MsgOptionTS(l.thread))
		}
		if _, ts, err := l.b.send(ctx, l.channel, "", build, opts...); err == nil && first {
			firstTS = ts
		}
	}
	if res.Reflecting && firstTS != "" && l.b.plansOK() {
		l.b.rememberFinal(res.RunID, &finalState{live: l, res: res, chunks: chunks[:min(perMessage, len(chunks))], channel: l.channel, ts: firstTS})
	}
	return firstTS
}

// finalState lets the bot re-render a finished answer when its reflection
// completes.
type finalState struct {
	live    *liveMessage
	res     agent.Result
	chunks  []string
	channel string
	ts      string
	at      time.Time
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}
