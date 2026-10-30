package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/memory"
	"blavingad/internal/store"
)

// fakeLLM always tries to call shell + memory_save first, then answers.
type fakeLLM struct {
	mu        sync.Mutex
	toolNames [][]string
	systems   []string
}

func (f *fakeLLM) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []llm.Message `json:"messages"`
		Tools    []llm.ToolDef `json:"tools"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	var names []string
	for _, t := range req.Tools {
		names = append(names, t.Function.Name)
	}
	f.mu.Lock()
	if len(req.Tools) > 0 {
		f.toolNames = append(f.toolNames, names)
		f.systems = append(f.systems, req.Messages[0].Content)
	}
	f.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	msg := map[string]any{"role": "assistant", "content": "Done! 🎉"}
	if last.Role == "user" && len(req.Tools) > 0 {
		msg = map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{
			{"id": "a", "type": "function", "function": map[string]string{"name": "shell", "arguments": `{"command":"rm -rf /"}`}},
			{"id": "b", "type": "function", "function": map[string]string{"name": "memory_save", "arguments": `{"content":"secret","kind":"fact"}`}},
		}}
	}
	if len(req.Tools) == 0 { // reflection call
		msg["content"] = `{"memories":[{"kind":"fact","content":"should not be saved"}],"person_notes":[]}`
	}
	json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": "stop"}}})
}

func setup(t *testing.T) (*Runner, *store.Store, *core.Access, *fakeLLM) {
	t.Helper()
	f := &fakeLLM{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	cfg := &config.Config{AgentName: "TEST", LLMBaseURL: srv.URL, LLMModel: "fake", LLMFastModel: "fake", LLMContextLimit: 100000,
		WorkspaceDir: t.TempDir(), DataDir: t.TempDir(), Timezone: time.UTC, TZName: "UTC", Language: "English"}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry(st)
	bus := core.NewBus()
	access := core.NewAccess(st, nil)
	mem := memory.New(st, nil, "")
	r := NewRunner(cfg, st, mem, llm.New(srv.URL, ""), reg, bus, core.NewApprovals(st, bus, reg), access)
	ran := map[string]bool{}
	for _, name := range []string{"shell", "memory_save", "web_search"} {
		name := name
		cat := map[string]string{"shell": "code", "memory_save": "memory", "web_search": "web"}[name]
		reg.Add(&core.Tool{Name: name, Category: cat, Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			ran[name] = true
			return "fait", nil
		}})
	}
	t.Cleanup(func() {
		if ran["shell"] && t.Name() == "TestGuestIsRestricted" {
			t.Error("shell handler executed for a guest")
		}
	})
	mem.Save(context.Background(), memory.Memory{Kind: "fact", Content: "The wifi password is hunter2"})
	return r, st, access, f
}

func TestGuestIsRestricted(t *testing.T) {
	r, st, access, f := setup(t)
	guest, _ := access.Resolve("UGUEST1")
	res := r.Run(context.Background(), Request{Source: "slack", Channel: "C1", UserID: "UGUEST1", UserName: "Visitor", Text: "wifi?", Profile: &guest})
	if res.Status != "done" {
		t.Fatalf("status %s: %s", res.Status, res.Output)
	}
	if len(f.toolNames) == 0 {
		t.Fatal("model never called")
	}
	for _, n := range f.toolNames[0] {
		if n == "shell" || n == "memory_save" {
			t.Errorf("guest was offered %s", n)
		}
	}
	if strings.Contains(f.systems[0], "hunter2") {
		t.Error("memories leaked into a no-memory profile's prompt")
	}
	if !strings.Contains(f.systems[0], "Guest") {
		t.Error("prompt does not tell the model about the access profile")
	}
	denied := st.Count(`SELECT COUNT(*) FROM run_steps WHERE run_id = ? AND kind = 'tool' AND status = 'denied'`, res.RunID)
	if denied != 2 {
		t.Errorf("expected 2 denied tool calls, got %d", denied)
	}
	if n := st.Count(`SELECT COUNT(*) FROM memories`); n != 1 {
		t.Errorf("guest run wrote memories (count=%d)", n)
	}
}

func TestMemberGetsMemory(t *testing.T) {
	r, _, access, f := setup(t)
	access.SetMember("UALICE1", "member", "", "test")
	p, _ := access.Resolve("UALICE1")
	r.Run(context.Background(), Request{Source: "slack", Channel: "C1", UserID: "UALICE1", UserName: "Alice", Text: "the wifi?", Profile: &p})
	if len(f.systems) == 0 || !strings.Contains(f.systems[0], "hunter2") {
		t.Error("member should get recalled memories")
	}
	offered := strings.Join(f.toolNames[0], ",")
	if !strings.Contains(offered, "shell") || !strings.Contains(offered, "memory_save") {
		t.Errorf("member tools: %s", offered)
	}
}
