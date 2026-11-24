package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/slack-go/slack"

	"blavingad/internal/agent"
	"blavingad/internal/textutil"
)

// liveMessage is one reply. Two paths only:
//
//   - native stream (stream.go): chat.startStream, tool calls stream as
//     task cards and the answer text streams as it is written;
//   - fallback, when Slack refuses or loses the stream: nothing is edited
//     live, the final answer is posted (or the stream message replaced)
//     once at the end, with the timeline as a collapsed `plan` block on top.
type liveMessage struct {
	b       *Bot
	channel string
	thread  string
	ts      string
	runID   string

	mu        sync.Mutex
	events    []*event
	iteration int
	draft     string // reply written so far in the current model turn
	stop      chan struct{}

	// Native Slack streaming (stream.go).
	user        string // recipient of the stream (the person who asked)
	native      bool
	closed      bool
	nmu         sync.Mutex // serialises stream API calls
	sent        map[int]string
	textIter    int
	streamed    string
	textBroken  bool
	anyText     bool
	earlierText bool // text of a previous turn was streamed
}

type event struct {
	kind     string // recall | thought | tool
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

// section renders answer text. expand=true stops Slack from folding long
// text behind "Show more" (notably in AI-app threads).
func section(text string) slack.Block {
	return slack.NewSectionBlock(mrkdwn(text), nil, nil, slack.SectionBlockOptionExpand(true))
}

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

// start opens the native stream. If Slack refuses it, nothing is posted
// until the answer is ready (no live chat.update edits).
func (l *liveMessage) start(ctx context.Context) {
	l.startNative(ctx)
}

func (l *liveMessage) progress(p agent.Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch p.Kind {
	case "thinking":
		l.iteration = p.Iteration
	case "draft":
		if p.Text == l.draft {
			return
		}
		l.draft = p.Text
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
	return textutil.Clip(title, 140)
}

func (l *liveMessage) task(i int, e *event) map[string]any {
	t := map[string]any{"task_id": fmt.Sprintf("t%d", i+1), "title": e.taskTitle(), "status": e.status}
	if d := strings.TrimSpace(e.details); d != "" {
		t["details"] = richText(textutil.Clip(d, 1500), e.kind == "tool")
	}
	out := strings.TrimSpace(e.output)
	if e.kind == "tool" && privateOutput[e.category] && !e.denied && out != "" {
		out = "(result hidden in Slack — see the trace)"
	}
	if out != "" {
		t["output"] = richText(textutil.Clip(out, 1200), e.kind == "tool" && !e.denied && !strings.HasPrefix(out, "("))
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

// planBlock renders the timeline.
func (l *liveMessage) planBlock(title string) slack.Block {
	events := l.events
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
	return rawBlock{kind: "plan", id: fmt.Sprintf("plan-%s", l.runID), body: map[string]any{"title": title, "tasks": tasks}}
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

// finalBlocks lays out the answer: the collapsed plan on top, then the text.
// plan=false (workspace rejects plan blocks) keeps only the text.
func (l *liveMessage) finalBlocks(res agent.Result, chunks []string, plan bool) []slack.Block {
	l.mu.Lock()
	defer l.mu.Unlock()
	var blocks []slack.Block
	if plan && (len(l.events) > 0 || res.Status != "done") {
		tools, thoughts := l.countTools()
		blocks = append(blocks, l.planBlock(doneTitle(tools, thoughts, res.Status)))
	}
	for _, c := range chunks {
		blocks = append(blocks, section(c))
	}
	return blocks
}

// ---------- finish ----------

// finalChunkSize caps the text of one final section block (Slack limit is 3000).
// Slack folds long text blocks behind "Show more", so keep sections short.
const finalChunkSize = 1200

// finish posts the answer and returns the ts of the message carrying it.
func (l *liveMessage) finish(ctx context.Context, res agent.Result) string {
	close(l.stop)
	if l.native {
		switch l.finishNative(ctx, res.Output, res.Status == "done") {
		case nativeComplete:
			// The answer is already fully streamed: touching the message
			// again would duplicate it (Slack keeps streamed text).
			return l.ts
		case nativeRepost:
			// Always repost as a new message: if the delete fails, updating
			// the old one would fail too (or stack on the streamed text).
			if _, _, err := l.b.API.DeleteMessageContext(ctx, l.channel, l.ts); err != nil {
				slog.Warn("could not delete the streamed message before reposting", "err", err)
			}
			l.ts = ""
		}
	}
	chunks := Chunk(ToMrkdwn(res.Output), finalChunkSize)
	if len(chunks) == 0 {
		chunks = []string{"🤷"}
	}
	// Slack caps a message at 50 blocks: long answers spill over into
	// follow-up messages; the plan stays on the first one.
	const perMessage = 40
	var firstTS string
	for i := 0; i < len(chunks); i += perMessage {
		part := chunks[i:min(i+perMessage, len(chunks))]
		first := i == 0
		build := func(plan bool) []slack.Block {
			if first {
				return l.finalBlocks(res, part, plan)
			}
			var blocks []slack.Block
			for _, c := range part {
				blocks = append(blocks, section(c))
			}
			return blocks
		}
		fallback := slack.MsgOptionText(textutil.Short(res.Output, 300), false)
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
	return firstTS
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}
