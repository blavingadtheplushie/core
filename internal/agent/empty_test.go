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

// emptyLLM: calls a tool, then replies with no text; the retry answers
// unless retryEmpty is set.
type emptyLLM struct {
	mu          sync.Mutex
	retryEmpty  bool
	retryTokens int
}

func (e *emptyLLM) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages  []llm.Message `json:"messages"`
		Tools     []any         `json:"tools"`
		MaxTokens int           `json:"max_completion_tokens"`
		MaxOld    int           `json:"max_tokens"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	last := req.Messages[len(req.Messages)-1]
	reply := func(msg map[string]any, finish string) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": finish}}})
	}
	switch {
	case strings.Contains(last.Content, "Your last reply was empty"):
		e.mu.Lock()
		e.retryTokens = max(req.MaxTokens, req.MaxOld)
		e.mu.Unlock()
		if e.retryEmpty {
			reply(map[string]any{"role": "assistant", "content": ""}, "stop")
			return
		}
		reply(map[string]any{"role": "assistant", "content": "Here is your answer ✅"}, "stop")
	case last.Role == "user" && len(req.Tools) > 0:
		reply(map[string]any{"role": "assistant", "content": "Let me check that for you.", "tool_calls": []map[string]any{
			{"id": "a", "type": "function", "function": map[string]string{"name": "noop", "arguments": "{}"}}}}, "tool_calls")
	default: // after the tool result: nothing written
		reply(map[string]any{"role": "assistant", "content": nil}, "length")
	}
}

func emptyRunner(t *testing.T, e *emptyLLM) (*Runner, *store.Store) {
	srv := httptest.NewServer(http.HandlerFunc(e.handler))
	t.Cleanup(srv.Close)
	cfg := &config.Config{AgentName: "T", LLMBaseURL: srv.URL, LLMModel: "m", LLMFastModel: "m", LLMContextLimit: 100000, LLMMaxTokens: 4000,
		WorkspaceDir: t.TempDir(), DataDir: t.TempDir(), Timezone: time.UTC, TZName: "UTC", Language: "English"}
	st, _ := store.Open(cfg.DataDir)
	st.SetSetting("agent.reflection", "false")
	reg, bus := core.NewRegistry(st), core.NewBus()
	reg.Add(&core.Tool{Name: "noop", Category: "system", Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
		return "done", nil
	}})
	return NewRunner(cfg, st, memory.New(st, nil, ""), llm.New(srv.URL, ""), reg, bus, core.NewApprovals(st, bus, reg), core.NewAccess(st, nil)), st
}

func TestEmptyReplyIsRetried(t *testing.T) {
	e := &emptyLLM{}
	r, st := emptyRunner(t, e)
	res := r.Run(context.Background(), Request{Source: "web", Text: "do it"})
	if res.Output != "Here is your answer ✅" {
		t.Fatalf("output = %q", res.Output)
	}
	if e.retryTokens != 8192 {
		t.Errorf("budget ran out (finish=length): retry should double max tokens, got %d", e.retryTokens)
	}
	if st.Count(`SELECT COUNT(*) FROM run_steps WHERE run_id = ? AND name = 'empty reply' AND output LIKE '%finish_reason=length%'`, res.RunID) != 1 {
		t.Error("the empty reply must be recorded in the trace with its finish reason")
	}
}

func TestEmptyReplyFallsBackToNarration(t *testing.T) {
	r, _ := emptyRunner(t, &emptyLLM{retryEmpty: true})
	res := r.Run(context.Background(), Request{Source: "web", Text: "do it"})
	if res.Output != "Let me check that for you." {
		t.Fatalf("output = %q", res.Output)
	}
}
