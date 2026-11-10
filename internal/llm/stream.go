package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// streamAnthropic sends a Messages request with "stream": true and rebuilds
// the usual non-streamed response from the SSE events, calling onText for
// every text delta. Connection errors, 429 and 5xx are retried as long as
// nothing was streamed yet.
func (c *Client) streamAnthropic(ctx context.Context, url string, body any, out *anthResponse, onText func(string)) ([]byte, error) {
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
		c.headers(req, StyleAnthropic)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if resp.StatusCode >= 400 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			lastErr = fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, truncate(string(data), 800))
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				continue
			}
			return data, lastErr
		}
		// Some gateways ignore "stream" and answer plain JSON.
		if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if err := json.Unmarshal(data, out); err != nil {
				return data, fmt.Errorf("unreadable LLM response: %w (%s)", err, truncate(string(data), 200))
			}
			for _, b := range out.Content {
				if b.Type == "text" && b.Text != "" {
					onText(b.Text)
				}
			}
			return data, nil
		}
		data, err := parseAnthropicSSE(resp.Body, out, onText)
		resp.Body.Close()
		return data, err
	}
	return nil, lastErr
}

// parseAnthropicSSE reads Anthropic stream events into out.
func parseAnthropicSSE(r io.Reader, out *anthResponse, onText func(string)) ([]byte, error) {
	type ev struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		Message      json.RawMessage `json:"message"`
		ContentBlock *anthBlock      `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	blocks := map[int]*anthBlock{}
	inputs := map[int]*strings.Builder{}
	var order []int
	var trace strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	done := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		if trace.Len() < 3000 {
			trace.WriteString(raw + "\n")
		}
		var e ev
		if json.Unmarshal([]byte(raw), &e) != nil {
			continue
		}
		switch e.Type {
		case "message_start":
			var m struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(e.Message, &m) == nil {
				out.Usage.InputTokens = m.Usage.InputTokens
				out.Usage.OutputTokens = m.Usage.OutputTokens
			}
		case "content_block_start":
			b := anthBlock{}
			if e.ContentBlock != nil {
				b = *e.ContentBlock
				b.Input = nil // the start block carries an empty {} placeholder
			}
			blocks[e.Index] = &b
			order = append(order, e.Index)
			if b.Type == "text" && b.Text != "" {
				onText(b.Text)
			}
		case "content_block_delta":
			b := blocks[e.Index]
			if b == nil {
				b = &anthBlock{}
				blocks[e.Index] = b
				order = append(order, e.Index)
			}
			switch e.Delta.Type {
			case "text_delta":
				if b.Type == "" {
					b.Type = "text"
				}
				b.Text += e.Delta.Text
				if e.Delta.Text != "" {
					onText(e.Delta.Text)
				}
			case "thinking_delta":
				b.Thinking += e.Delta.Thinking
			case "input_json_delta":
				if inputs[e.Index] == nil {
					inputs[e.Index] = &strings.Builder{}
				}
				inputs[e.Index].WriteString(e.Delta.PartialJSON)
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				out.StopReason = e.Delta.StopReason
			}
			if e.Usage != nil {
				if e.Usage.InputTokens > 0 {
					out.Usage.InputTokens = e.Usage.InputTokens
				}
				out.Usage.OutputTokens = e.Usage.OutputTokens
			}
		case "message_stop":
			done = true
		case "error":
			if e.Error != nil {
				out.Error = &struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{e.Error.Type, e.Error.Message}
			}
			done = true
		}
		if done {
			break
		}
	}
	if err := sc.Err(); err != nil && !done {
		return []byte(trace.String()), fmt.Errorf("LLM stream interrupted: %w", err)
	}
	for _, i := range order {
		b := blocks[i]
		if in := inputs[i]; in != nil {
			b.Input = json.RawMessage(in.String())
		}
		out.Content = append(out.Content, *b)
	}
	if !done && out.StopReason == "" && out.Error == nil {
		return []byte(trace.String()), fmt.Errorf("LLM stream ended early")
	}
	return []byte(trace.String()), nil
}
