package slackbot

import (
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
	_, plans := encode(t, l.workingBlocks(true))
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

	js, plans := encode(t, l.workingBlocks(true))
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
	for _, want := range []string{"step 2", `{\"query\":\"go release\"}`, "Go 1.27 released", "I'll search", "Not allowed for your access profile (👋 Guest)", "rich_text_preformatted"} {
		if !strings.Contains(js, want) {
			t.Errorf("working plan misses %q", want)
		}
	}
	if strings.Contains(js, "salary") {
		t.Error("memory results must not be shown in Slack")
	}

	// Final: plan first (with the reflection task), then the answer, nothing else.
	res := agent.Result{RunID: "run_1", Status: "done", ToolCalls: 4, Reflecting: true, Output: "Go 1.27 is out 🎉"}
	blocks := l.finalBlocks(res, []string{"*Go 1.27* is out 🎉"}, reflectionEvent(true, false, nil), true)
	js, plans = encode(t, blocks)
	if _, ok := blocks[0].(rawBlock); !ok || len(blocks) != 2 {
		t.Fatalf("final layout must be [plan, answer]: %s", js)
	}
	last := plans[0].Tasks[len(plans[0].Tasks)-1]
	if !strings.Contains(plans[0].Title, "2 thoughts") || !strings.Contains(plans[0].Title, "4 tool calls") || last.Title != "💭 Reflection" || last.Status != "in_progress" {
		t.Errorf("final plan: title %q, last task %+v", plans[0].Title, last)
	}
	for _, banned := range []string{"Remembered", "⏱", "tokens", "gpt", "feedback"} {
		if strings.Contains(js, banned) {
			t.Errorf("final layout must not show %q", banned)
		}
	}
	done := reflectionEvent(true, true, []string{"+ #1 [fact] Go 1.27 is out", "👤 <@U1>: likes Go"})
	js, _ = encode(t, l.finalBlocks(res, []string{"answer"}, done, true))
	if !strings.Contains(js, "Remembered 2 things") || !strings.Contains(js, `"status":"complete"`) {
		t.Errorf("reflection result must land in the plan: %s", js)
	}
	if js, _ := encode(t, l.finalBlocks(res, []string{"a"}, reflectionEvent(true, true, nil), true)); !strings.Contains(js, "Nothing new worth remembering") {
		t.Error("empty reflection")
	}
}

func TestPlanFoldsAt50(t *testing.T) {
	l := newTestLive()
	for i := 0; i < 70; i++ {
		l.progress(agent.Progress{Kind: "thought", Text: "step"})
	}
	_, plans := encode(t, l.finalBlocks(agent.Result{Status: "done"}, []string{"ok"}, reflectionEvent(true, false, nil), true))
	if n := len(plans[0].Tasks); n != 50 || !strings.Contains(plans[0].Tasks[0].Title, "22 earlier steps") {
		t.Errorf("tasks = %d, first = %q", n, plans[0].Tasks[0].Title)
	}
}

func TestBareAnswerAndLegacy(t *testing.T) {
	l := newTestLive()
	blocks := l.finalBlocks(agent.Result{Status: "done"}, []string{"Hi! 👋"}, nil, true)
	if len(blocks) != 1 {
		t.Errorf("a plain answer needs no plan: %d blocks", len(blocks))
	}
	l.progress(agent.Progress{Kind: "tool_start", Tool: "shell", Emoji: "🐚", Label: "$ ls"})
	js, plans := encode(t, l.workingBlocks(false))
	if len(plans) != 0 || !strings.Contains(js, "$ ls") {
		t.Errorf("legacy working layout: %s", js)
	}
	js, plans = encode(t, l.finalBlocks(agent.Result{Status: "error"}, []string{"😵 Oops"}, nil, false))
	if len(plans) != 0 || !strings.Contains(js, "Something went wrong") || !strings.Contains(js, "1 tool call") {
		t.Errorf("legacy final layout: %s", js)
	}
}
