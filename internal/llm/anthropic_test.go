package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToAnthropic(t *testing.T) {
	system, msgs := toAnthropic([]Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "assistant", Content: "Earlier answer"}, // history starting with the bot
		{Role: "user", Content: "Search two things"},
		{Role: "assistant", Content: "On it.", ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: FunctionCall{Name: "web_search", Arguments: `{"query":"a"}`}},
			{ID: "c2", Type: "function", Function: FunctionCall{Name: "web_search", Arguments: ``}},
		}},
		{Role: "tool", ToolCallID: "c1", Name: "web_search", Content: "result a"},
		{Role: "tool", ToolCallID: "c2", Name: "web_search", Content: ""},
		{Role: "user", Content: "[System] step limit"},
	})
	if system != "You are a bot." {
		t.Errorf("system = %q", system)
	}
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user,assistant,user" {
		t.Fatalf("roles must alternate starting with user: %v", roles)
	}
	toolTurn := msgs[3]
	if len(toolTurn.Content) != 3 || toolTurn.Content[1].Type != "tool_use" || string(toolTurn.Content[2].Input) != "{}" {
		t.Errorf("assistant tool turn: %+v", toolTurn.Content)
	}
	results := msgs[4].Content
	if len(results) != 3 || results[0].Type != "tool_result" || results[0].ToolUseID != "c1" || results[1].Content != "(no output)" || results[2].Type != "text" {
		t.Errorf("tool results must be merged into one user turn with the follow-up text: %+v", results)
	}
}

func TestChatAnthropic(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":"Let me search."},{"type":"text","text":"Searching…"},
			{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"go 1.27"}}],
			"stop_reason":"tool_use","usage":{"input_tokens":120,"output_tokens":30}}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/v1", "sk-test")
	c.Endpoint = func() (string, string) { return StyleAnthropic, "" }
	temp := 1.4
	res, err := c.Chat(context.Background(), Request{Model: "claude-x", MaxTokens: 500, Temperature: &temp, Messages: []Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"},
	}, Tools: []ToolDef{{Type: "function", Function: FunctionDef{Name: "web_search", Description: "d", Parameters: map[string]any{"type": "object"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["system"] != "sys" || body["max_tokens"] != float64(500) {
		t.Errorf("request body: %v", body)
	}
	// Temperature is deliberately not sent in Anthropic mode (recent models
	// and some gateways reject it).
	if _, ok := body["temperature"]; ok {
		t.Errorf("temperature must not be sent: %v", body)
	}
	tools := body["tools"].([]any)[0].(map[string]any)
	if tools["name"] != "web_search" || tools["input_schema"] == nil {
		t.Errorf("tools: %v", tools)
	}
	if hdr.Get("anthropic-version") == "" || hdr.Get("x-api-key") != "sk-test" || hdr.Get("Accept") != "application/json" {
		t.Errorf("headers: %v", hdr)
	}
	if res.Message.Content != "Searching…" || res.Reasoning != "Let me search." || res.FinishReason != "tool_calls" {
		t.Errorf("response: %+v", res)
	}
	if len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].Function.Arguments != `{"query":"go 1.27"}` || res.Message.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("tool calls: %+v", res.Message.ToolCalls)
	}
	if res.Usage.PromptTokens != 120 || res.Usage.CompletionTokens != 30 {
		t.Errorf("usage: %+v", res.Usage)
	}
}

func TestModelsAndRuntimeEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alt/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"zeta"},{"id":"alpha"}]}`))
	}))
	defer srv.Close()
	c := New("http://unused.invalid", "k")
	c.Endpoint = func() (string, string) { return "", srv.URL + "/alt/" }
	ids, err := c.Models(context.Background())
	if err != nil || strings.Join(ids, ",") != "alpha,zeta" {
		t.Errorf("models = %v, %v", ids, err)
	}
}
