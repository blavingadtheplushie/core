package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Anthropic Messages API (and gateways that speak it: Snowflake Cortex,
// LiteLLM, Bedrock proxies…). The harness keeps its OpenAI-shaped history;
// this file translates it both ways.

type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
}

type anthMessage struct {
	Role    string      `json:"role"`
	Content []anthBlock `json:"content"`
}

type anthTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// toAnthropic converts OpenAI-style messages: system prompts are pulled out,
// tool calls become tool_use blocks, tool results become tool_result blocks
// in a user turn, consecutive same-role turns are merged (the API requires
// strict alternation) and the conversation always starts with a user turn.
func toAnthropic(msgs []Message) (string, []anthMessage) {
	var system []string
	var out []anthMessage
	push := func(role string, blocks ...anthBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anthMessage{Role: role, Content: blocks})
	}
	for _, m := range msgs {
		switch m.Role {
		case "system":
			if strings.TrimSpace(m.Content) != "" {
				system = append(system, m.Content)
			}
		case "assistant":
			var blocks []anthBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if !json.Valid(input) || strings.TrimSpace(tc.Function.Arguments) == "" {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
			}
			push("assistant", blocks...)
		case "tool":
			content := m.Content
			if content == "" {
				content = "(no output)"
			}
			push("user", anthBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content})
		default: // user
			if strings.TrimSpace(m.Content) != "" {
				push("user", anthBlock{Type: "text", Text: m.Content})
			}
		}
	}
	if len(out) == 0 || out[0].Role != "user" {
		out = append([]anthMessage{{Role: "user", Content: []anthBlock{{Type: "text", Text: "(conversation start)"}}}}, out...)
	}
	return strings.Join(system, "\n\n"), out
}

type anthResponse struct {
	Content    []anthBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) chatAnthropic(ctx context.Context, base string, req Request) (*Response, error) {
	system, msgs := toAnthropic(req.Messages)
	if req.JSONMode {
		// No response_format in this API: ask for it in the system prompt.
		system += "\n\nReply with a single valid JSON object and nothing else."
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192 // required by the API
	}
	body := map[string]any{"model": req.Model, "max_tokens": maxTokens, "messages": msgs}
	if strings.TrimSpace(system) != "" {
		body["system"] = strings.TrimSpace(system)
	}
	// if req.Temperature != nil {
	// 	body["temperature"] = min(*req.Temperature, 1) // Anthropic caps at 1
	// }
	if len(req.Tools) > 0 {
		tools := make([]anthTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, anthTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: t.Function.Parameters})
		}
		body["tools"] = tools
	}
	var raw anthResponse
	data, err := c.post(ctx, base+"/messages", StyleAnthropic, body, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Error != nil {
		return nil, fmt.Errorf("%s: %s", raw.Error.Type, raw.Error.Message)
	}
	if len(raw.Content) == 0 && raw.StopReason == "" {
		return nil, errors.New("the model returned an empty response")
	}
	msg := Message{Role: "assistant"}
	var text, thinking []string
	for i, b := range raw.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "thinking":
			thinking = append(thinking, b.Thinking)
		case "tool_use":
			args := string(b.Input)
			if strings.TrimSpace(args) == "" || args == "null" {
				args = "{}"
			}
			id := b.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", i)
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: b.Name, Arguments: args}})
		}
	}
	msg.Content = strings.TrimSpace(strings.Join(text, "\n"))
	finish := raw.StopReason
	if finish == "tool_use" {
		finish = "tool_calls"
	}
	return &Response{Message: msg, Reasoning: strings.TrimSpace(strings.Join(thinking, "\n")), FinishReason: finish,
		Usage: Usage{PromptTokens: raw.Usage.InputTokens, CompletionTokens: raw.Usage.OutputTokens}, Raw: truncate(string(data), 3000)}, nil
}
