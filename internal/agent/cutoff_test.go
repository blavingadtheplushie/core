package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"blavingad/internal/llm"
)

// cutLLM always cuts its reply at the token limit, except when told to
// continue (when stopAfter > 0, it finishes on the n-th call).
type cutLLM struct {
	mu        sync.Mutex
	calls     int
	stopAfter int
}

func (c *cutLLM) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []llm.Message `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	finish := "length"
	if c.stopAfter > 0 && n >= c.stopAfter {
		finish = "stop"
	}
	if n > 1 && !strings.Contains(req.Messages[len(req.Messages)-1].Content, "cut off by the output token limit") {
		finish = "bad"
	}
	json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "part" + string(rune('0'+n)) + " "}, "finish_reason": finish}}})
}

func TestCutOffReplyIsResumed(t *testing.T) {
	c := &cutLLM{stopAfter: 3}
	r, _ := emptyRunner(t, &emptyLLM{})
	r.llm = llm.New(serve(t, c.handler), "")
	res := r.Run(context.Background(), Request{Source: "web", Text: "write a lot"})
	if strings.TrimSpace(res.Output) != "part1 part2 part3" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestCutOffGivesUpAfterRetries(t *testing.T) {
	c := &cutLLM{}
	r, st := emptyRunner(t, &emptyLLM{})
	st.SetSetting("agent.llm_retries", "2")
	r.llm = llm.New(serve(t, c.handler), "")
	res := r.Run(context.Background(), Request{Source: "web", Text: "write a lot"})
	if c.calls != 3 {
		t.Fatalf("calls = %d, want 1 + 2 retries", c.calls)
	}
	if strings.TrimSpace(res.Output) != "part1 part2 part3" {
		t.Fatalf("output = %q", res.Output)
	}
}

func serve(t *testing.T, h http.HandlerFunc) string {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
