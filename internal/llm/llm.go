// Package llm is a small client for OpenAI-compatible chat/embeddings APIs
// (OpenAI, OpenRouter, Ollama, vLLM, LiteLLM, Groq, Mistral, ...).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// MarshalJSON keeps `content` present (some servers reject a missing field)
// but sends null for assistant tool-call turns with no text.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message
	out := struct {
		alias
		Content any `json:"content"`
	}{alias: alias(m), Content: m.Content}
	if m.Content == "" && len(m.ToolCalls) > 0 {
		out.Content = nil
	}
	return json.Marshal(out)
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	JSONMode    bool      `json:"-"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type Response struct {
	Message Message
	// Reasoning is the model's thinking, for providers that expose it
	// (DeepSeek/vLLM `reasoning_content`, OpenRouter `reasoning`). It is shown
	// to humans but never sent back to the model.
	Reasoning    string
	FinishReason string
	Usage        Usage
	// Raw is the (truncated) response body, kept to diagnose odd replies.
	Raw string
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// Headers are extra headers some providers require (e.g. Snowflake's
	// X-Snowflake-Authorization-Token-Type).
	Headers map[string]string
	// Endpoint, when set, is consulted on every call so the API style and
	// base URL can be switched at runtime (from the dashboard settings).
	Endpoint func() (style, baseURL string)
}

// API styles.
const (
	StyleOpenAI    = "openai"    // POST {base}/chat/completions
	StyleAnthropic = "anthropic" // POST {base}/messages
)

func New(baseURL, apiKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Client) endpoint() (string, string) {
	style, base := StyleOpenAI, c.BaseURL
	if c.Endpoint != nil {
		if s, b := c.Endpoint(); s != "" || b != "" {
			if s != "" {
				style = s
			}
			if b != "" {
				base = strings.TrimRight(b, "/")
			}
		}
	}
	return style, base
}

type rawResponse struct {
	Choices []struct {
		Message struct {
			Role             string          `json:"role"`
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ToolCalls        []ToolCall      `json:"tool_calls"`
			Refusal          string          `json:"refusal"`
			// Non-standard shapes seen on some gateways.
			ContentList json.RawMessage `json:"content_list"`
		} `json:"message"`
		Text         string `json:"text"` // legacy completions shape
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends a chat completion request with retry on rate limits and
// transient server errors.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	style, base := c.endpoint()
	if style == StyleAnthropic {
		return c.chatAnthropic(ctx, base, req)
	}
	body := map[string]any{"model": req.Model, "messages": req.Messages}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
		if req.ToolChoice != nil {
			body["tool_choice"] = req.ToolChoice
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_completion_tokens"] = req.MaxTokens
	}
	if req.JSONMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	var raw rawResponse
	data, err := c.post(ctx, base+"/chat/completions", StyleOpenAI, body, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Error != nil {
		return nil, errors.New(raw.Error.Message)
	}
	if len(raw.Choices) == 0 {
		return nil, errors.New("the model returned no choices")
	}
	ch := raw.Choices[0]
	msg := Message{Role: "assistant", ToolCalls: ch.Message.ToolCalls, Content: decodeContent(ch.Message.Content)}
	if msg.Content == "" {
		msg.Content = decodeContent(ch.Message.ContentList)
	}
	if msg.Content == "" {
		msg.Content = ch.Text
	}
	if msg.Content == "" && ch.Message.Refusal != "" {
		msg.Content = "🙅 " + ch.Message.Refusal
	}
	for i := range msg.ToolCalls {
		if msg.ToolCalls[i].Type == "" {
			msg.ToolCalls[i].Type = "function"
		}
		if msg.ToolCalls[i].ID == "" {
			msg.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}
		if strings.TrimSpace(msg.ToolCalls[i].Function.Arguments) == "" {
			msg.ToolCalls[i].Function.Arguments = "{}"
		}
	}
	reasoning := ch.Message.ReasoningContent
	if reasoning == "" {
		reasoning = ch.Message.Reasoning
	}
	return &Response{Message: msg, Reasoning: strings.TrimSpace(reasoning), FinishReason: ch.FinishReason, Usage: raw.Usage, Raw: truncate(string(data), 3000)}, nil
}

// decodeContent accepts both the classic string content and the newer
// array-of-parts shape.
func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "thinking" || p.Type == "reasoning" {
				continue
			}
			b.WriteString(firstNonEmpty(p.Text, p.Content))
		}
		return b.String()
	}
	return string(raw)
}

func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	var raw struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	// Embeddings are always OpenAI-style (Anthropic has no embeddings API).
	if _, err := c.post(ctx, c.BaseURL+"/embeddings", StyleOpenAI, map[string]any{"model": model, "input": inputs}, &raw); err != nil {
		return nil, err
	}
	out := make([][]float32, len(inputs))
	for _, d := range raw.Data {
		if d.Index < len(out) {
			out[d.Index] = normalize(d.Embedding)
		}
	}
	return out, nil
}

// headers sets content negotiation, authentication for the API style and
// the extra configured headers.
func (c *Client) headers(req *http.Request, style string) {
	req.Header.Set("Accept", "application/json") // some gateways (Snowflake…) require it
	if c.APIKey != "" {
		if style == StyleAnthropic {
			req.Header.Set("x-api-key", c.APIKey)
			// Gateways speaking the Anthropic format (LiteLLM, Snowflake,
			// proxies) usually authenticate with a bearer token instead.
			if req.URL.Host != "api.anthropic.com" {
				req.Header.Set("Authorization", "Bearer "+c.APIKey)
			}
		} else {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
	}
	if style == StyleAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	req.Header.Set("HTTP-Referer", "https://github.com/blavingad")
	req.Header.Set("X-Title", "BLAVINGAD")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
}

// Models lists the model ids the endpoint advertises (GET {base}/models,
// same shape for OpenAI and Anthropic).
func (c *Client) Models(ctx context.Context) ([]string, error) {
	style, base := c.endpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, style)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unreadable model list: %w", err)
	}
	var ids []string
	for _, m := range raw.Data {
		ids = append(ids, m.ID)
	}
	for _, m := range raw.Models { // Ollama-style
		ids = append(ids, m.Name)
	}
	sort.Strings(ids)
	return ids, nil
}

// post sends the request (with retries) and returns the raw body.
func (c *Client) post(ctx context.Context, url, style string, body any, out any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			wait := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		c.headers(req, style)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, truncate(string(data), 400))
			continue
		}
		if resp.StatusCode >= 400 {
			return data, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, truncate(string(data), 800))
		}
		if err := json.Unmarshal(data, out); err != nil {
			return data, fmt.Errorf("unreadable LLM response: %w (%s)", err, truncate(string(data), 200))
		}
		return data, nil
	}
	return nil, lastErr
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= n
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// EstimateTokens is a cheap heuristic (≈ 3.5 chars per token) used for
// context budgeting when the server does not tell us better.
func EstimateTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) + 16
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) + len(tc.Function.Name) + 16
		}
	}
	return int(float64(total) / 3.5)
}

func F(v float64) *float64 { return &v }

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
