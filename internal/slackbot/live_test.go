package slackbot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"blavingad/internal/agent"
	"blavingad/internal/config"
)

type planJSON struct {
	Type    string `json:"type"`
	BlockID string `json:"block_id"`
	Title   string `json:"title"`
	Tasks   []struct {
		Type    string          `json:"type"`
		TaskID  string          `json:"task_id"`
		Title   string          `json:"title"`
		Status  string          `json:"status"`
		Details json.RawMessage `json:"details"`
		Output  json.RawMessage `json:"output"`
	} `json:"tasks"`
}

// encode marshals blocks like slack-go does, and enforces the Block Kit
// limits Slack rejects whole messages for.
func encode(t *testing.T, blocks []slack.Block) (string, []planJSON) {
	t.Helper()
	if len(blocks) > 50 {
		t.Errorf("%d blocks (max 50)", len(blocks))
	}
	var plans []planJSON
	for _, bl := range blocks {
		switch v := bl.(type) {
		case *slack.SectionBlock:
			if v.Text != nil && len(v.Text.Text) > 3000 {
				t.Errorf("section text %d chars (max 3000)", len(v.Text.Text))
			}
			if !v.Expand {
				t.Errorf("section blocks must set expand=true (no \"Show more\")")
			}
		case *slack.ContextBlock:
			if n := len(v.ContextElements.Elements); n == 0 || n > 10 {
				t.Errorf("context with %d elements", n)
			}
		case *slack.ActionBlock:
			t.Errorf("answers must not carry buttons")
		case rawBlock:
			raw, _ := json.Marshal(v)
			var p planJSON
			json.Unmarshal(raw, &p)
			if len(p.Tasks) == 0 || len(p.Tasks) > 50 {
				t.Errorf("plan with %d tasks (1-50)", len(p.Tasks))
			}
			seen := map[string]bool{}
			for _, task := range p.Tasks {
				if task.Type != "" {
					t.Errorf("plan tasks must not carry a type: %+v", task)
				}
				if seen[task.TaskID] || task.Title == "" || task.Status == "" {
					t.Errorf("bad task %+v", task)
				}
				seen[task.TaskID] = true
			}
			plans = append(plans, p)
		}
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(blocks); err != nil {
		t.Fatal(err)
	}
	return buf.String(), plans
}

func newTestLive() *liveMessage {
	bot := &Bot{cfg: &config.Config{AgentName: "BLAVINGAD", PublicURL: "https://blv.example"}}
	l := newLiveMessage(bot, "C1", "123.4")
	l.runID = "run_1"
	return l
}

func TestTimelinePlan(t *testing.T) {
	l := newTestLive()
	_, plans := encode(t, []slack.Block{l.planBlock("Working on it…")})
	if len(plans) != 1 || plans[0].Tasks[0].Status != "in_progress" {
		t.Fatalf("initial plan: %+v", plans)
	}
	firstID := plans[0].BlockID

	l.progress(agent.Progress{Kind: "thinking", Iteration: 2})
	l.progress(agent.Progress{Kind: "recall", Count: 3})
	l.progress(agent.Progress{Kind: "thought", Text: "The user wants the latest Go release; I'll search."})
	l.progress(agent.Progress{Kind: "tool_start", Tool: "web_search", Category: "web", Emoji: "🔎", Label: "Web search: go release", Args: `{"query":"go release"}`})
	l.progress(agent.Progress{Kind: "tool_end", Tool: "web_search", Category: "web", Emoji: "🔎", Label: "Web search: go release", OK: true, Text: "[1] Go 1.27 released"})
	l.progress(agent.Progress{Kind: "tool_start", Tool: "memory_search", Category: "memory", Emoji: "🔍", Label: "Memories: alice"})
	l.progress(agent.Progress{Kind: "tool_end", Tool: "memory_search", Category: "memory", Emoji: "🔍", Label: "Memories: alice", OK: true, Text: "#4 Alice's salary is …"})
	l.progress(agent.Progress{Kind: "tool_denied", Tool: "fs_write", Emoji: "📝", Label: "Write x.go", Text: "👋 Guest"})
	l.progress(agent.Progress{Kind: "thought", Text: "Now I can answer."})
	l.progress(agent.Progress{Kind: "tool_start", Tool: "shell", Category: "code", Emoji: "🐚", Label: "$ go version"})

	js, plans := encode(t, []slack.Block{l.planBlock("Working on it…")})
	p := plans[0]
	if p.BlockID != firstID {
		t.Errorf("block_id must stay stable across updates: first %q, now %q", firstID, p.BlockID)
	}
	var titles, statuses []string
	for _, task := range p.Tasks {
		titles = append(titles, task.Title)
		statuses = append(statuses, task.Status)
	}
	wantOrder := []string{"🧠 Recalled 3 memories", "💭 Thinking", "🔎 Web search: go release", "🔍 Memories: alice", "🚫 📝 Write x.go", "💭 Thinking", "🐚 $ go version"}
	if strings.Join(titles, "|") != strings.Join(wantOrder, "|") {
		t.Errorf("not chronological:\n got %v\nwant %v", titles, wantOrder)
	}
	if strings.Join(statuses, ",") != "complete,complete,complete,complete,error,complete,in_progress" {
		t.Errorf("statuses: %v", statuses)
	}
	for _, want := range []string{`{\"query\":\"go release\"}`, "Go 1.27 released", "I'll search", "Not allowed for your access profile (👋 Guest)", "rich_text_preformatted"} {
		if !strings.Contains(js, want) {
			t.Errorf("working plan misses %q", want)
		}
	}
	if strings.Contains(js, "salary") {
		t.Error("memory results must not be shown in Slack")
	}

	// Final: plan first, then the answer, nothing else.
	res := agent.Result{RunID: "run_1", Status: "done", ToolCalls: 4, Output: "Go 1.27 is out 🎉"}
	blocks := l.finalBlocks(res, []string{"*Go 1.27* is out 🎉"}, true)
	js, plans = encode(t, blocks)
	if _, ok := blocks[0].(rawBlock); !ok || len(blocks) != 2 {
		t.Fatalf("final layout must be [plan, answer]: %s", js)
	}
	if !strings.Contains(plans[0].Title, "2 thoughts") || !strings.Contains(plans[0].Title, "4 tool calls") {
		t.Errorf("final plan title %q", plans[0].Title)
	}
	for _, banned := range []string{"Reflection", "⏱", "tokens", "gpt", "feedback"} {
		if strings.Contains(js, banned) {
			t.Errorf("final layout must not show %q", banned)
		}
	}
	// No plan blocks allowed: text only.
	if bl := l.finalBlocks(res, []string{"a"}, false); len(bl) != 1 {
		t.Errorf("no-plan layout: %d blocks", len(bl))
	}
}

func TestPlanFoldsAt50(t *testing.T) {
	l := newTestLive()
	for i := 0; i < 70; i++ {
		l.progress(agent.Progress{Kind: "thought", Text: "step"})
	}
	_, plans := encode(t, l.finalBlocks(agent.Result{Status: "done"}, []string{"ok"}, true))
	if n := len(plans[0].Tasks); n != 50 || !strings.Contains(plans[0].Tasks[0].Title, "21 earlier steps") {
		t.Errorf("tasks = %d, first = %q", n, plans[0].Tasks[0].Title)
	}
}

func TestBareAnswer(t *testing.T) {
	l := newTestLive()
	if blocks := l.finalBlocks(agent.Result{Status: "done"}, []string{"Hi! 👋"}, true); len(blocks) != 1 {
		t.Errorf("a plain answer needs no plan: %d blocks", len(blocks))
	}
}

func TestStreamMarkdown(t *testing.T) {
	cases := map[string]string{
		"*bold* and _it_":          "**bold** and _it_",
		"`a*b` *x*":                "`a*b` **x**",
		"```\n*no*\n``` *yes*":     "```\n*no*\n``` **yes**",
		"**already**":              "**already**",
		"<https://x.y|link> :yay:": "<https://x.y|link> :yay:",
	}
	for in, want := range cases {
		if got := streamMarkdown(in); got != want {
			t.Errorf("streamMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
	// Append-only: a longer draft keeps the shorter one's conversion as prefix.
	a, b := streamMarkdown("hello *wor"), streamMarkdown("hello *world* `c*`")
	if !strings.HasPrefix(b, a) {
		t.Errorf("not append-only: %q / %q", a, b)
	}
}

func TestNativeChunksDelta(t *testing.T) {
	l := newTestLive()
	l.sent = map[int]string{}
	l.progress(agent.Progress{Kind: "thinking", Iteration: 1})
	l.progress(agent.Progress{Kind: "tool_start", Tool: "web_search", Category: "web", Emoji: "🔎", Label: "Web search: x"})
	if c := l.nativeChunks(); len(c) != 1 {
		t.Fatalf("want 1 task chunk, got %d", len(c))
	}
	if c := l.nativeChunks(); len(c) != 0 {
		t.Fatalf("unchanged state must send nothing, got %d", len(c))
	}
	l.progress(agent.Progress{Kind: "tool_end", Tool: "web_search", Category: "web", Emoji: "🔎", Label: "Web search: x", OK: true, Text: "ok"})
	l.progress(agent.Progress{Kind: "thinking", Iteration: 2})
	l.progress(agent.Progress{Kind: "draft", Text: "Intro.\n\nVoici *la"})
	c := l.nativeChunks()
	if len(c) != 2 {
		t.Fatalf("want task update + text, got %d", len(c))
	}
	l.progress(agent.Progress{Kind: "draft", Text: "Intro.\n\nVoici *la réponse*"})
	c = l.nativeChunks()
	if len(c) != 1 || c[0].(slack.MarkdownTextChunk).Text != " réponse**" {
		t.Fatalf("bad text delta: %+v", c)
	}
}

// Narration written before a tool call must not leak into the streamed
// answer: it goes to the plan as a Thinking card instead.
func TestNativeNarrationGoesToPlan(t *testing.T) {
	l := newTestLive()
	l.sent = map[int]string{}
	l.progress(agent.Progress{Kind: "thinking", Iteration: 1})
	l.progress(agent.Progress{Kind: "draft", Text: "Code de vérif envoyé, je vais le"})
	if c := l.nativeChunks(); len(c) != 0 {
		t.Fatalf("short narration must not be streamed, got %+v", c)
	}
	l.progress(agent.Progress{Kind: "draft", Text: ""})
	l.progress(agent.Progress{Kind: "thought", Text: "Code de vérif envoyé, je vais le lire."})
	c := l.nativeChunks()
	if len(c) != 1 {
		t.Fatalf("want 1 thinking card, got %+v", c)
	}
	if tc, ok := c[0].(slack.TaskUpdateChunk); !ok || !strings.Contains(tc.Title, "Thinking") {
		t.Fatalf("want a Thinking task card, got %+v", c[0])
	}
	l.progress(agent.Progress{Kind: "thinking", Iteration: 2})
	long := strings.Repeat("réponse finale ", 20)
	l.progress(agent.Progress{Kind: "draft", Text: long})
	c = l.nativeChunks()
	if len(c) != 1 {
		t.Fatalf("long final answer must stream, got %+v", c)
	}
}

func TestNativeFinishModesPrefix(t *testing.T) {
	l := newTestLive()
	l.sent = map[int]string{}
	l.progress(agent.Progress{Kind: "thinking", Iteration: 1})
	long := strings.Repeat("réponse finale ", 20)
	l.progress(agent.Progress{Kind: "draft", Text: long})
	if c := l.nativeChunks(); len(c) != 1 {
		t.Fatalf("want text chunk, got %+v (streamed %q)", c, l.streamed)
	}
	if l.streamed == "" || !strings.HasPrefix(streamMarkdown(long+"fin."), l.streamed) || l.earlierText {
		t.Fatalf("streamed text should be a prefix of the final answer")
	}
	l.progress(agent.Progress{Kind: "thinking", Iteration: 2})
	l.nativeChunks()
	if !l.earlierText {
		t.Fatalf("text of an earlier turn must be tracked")
	}
}

// When Slack drops the stream, nothing is edited live and finish() must
// repost if text was already streamed (it can't be removed by chat.update).
func TestNativeDroppedStream(t *testing.T) {
	l := newTestLive()
	l.native, l.closed, l.sent = true, true, map[int]string{}
	if m := l.finishNative(context.Background(), "answer", true); m != nativeUpdate {
		t.Errorf("no text streamed: want update, got %d", m)
	}
	l.streamed = "partial"
	if m := l.finishNative(context.Background(), "answer", true); m != nativeRepost {
		t.Errorf("text streamed: want repost, got %d", m)
	}
	l.flushNative(context.Background()) // closed: must not call Slack (nil API would panic)
}
