package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sseSample = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`

func TestAnthropicStream(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseSample))
	}))
	defer srv.Close()
	c := New(srv.URL, "key")
	c.Endpoint = func() (string, string) { return StyleAnthropic, srv.URL }
	var deltas []string
	resp, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}},
		OnText: func(d string) { deltas = append(deltas, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"stream":true`) {
		t.Errorf("request not streamed: %s", gotBody)
	}
	if strings.Join(deltas, "") != "Hello world" || resp.Message.Content != "Hello world" {
		t.Errorf("deltas=%q content=%q", deltas, resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Arguments != `{"command":"ls"}` || resp.Message.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 30 {
		t.Errorf("finish=%s usage=%+v", resp.FinishReason, resp.Usage)
	}
}

func TestAnthropicStreamFallbackJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"plain"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "key")
	c.Endpoint = func() (string, string) { return StyleAnthropic, srv.URL }
	var got string
	resp, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, OnText: func(d string) { got += d }})
	if err != nil || resp.Message.Content != "plain" || got != "plain" {
		t.Fatalf("err=%v content=%q got=%q", err, resp.Message.Content, got)
	}
}
