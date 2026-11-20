package agent

import (
	"encoding/json"
	"sort"

	"blavingad/internal/llm"
)

// Token accounting for the run trace: where do the input tokens go?

// estTokens is a rough tokenizer-free estimate (~3.5 chars per token).
func estTokens(s string) int { return int(float64(len(s))/3.5 + 0.5) }

func addUsage(dst *llm.Usage, u llm.Usage) {
	dst.PromptTokens += u.PromptTokens
	dst.CompletionTokens += u.CompletionTokens
	dst.CacheReadTokens += u.CacheReadTokens
	dst.CacheWriteTokens += u.CacheWriteTokens
}

// usageDetail is what one model call cost, as stored in run_steps.usage.
func usageDetail(u llm.Usage) map[string]int {
	return map[string]int{
		"input":       u.PromptTokens - u.CacheReadTokens - u.CacheWriteTokens, // uncached
		"cache_read":  u.CacheReadTokens,
		"cache_write": u.CacheWriteTokens,
		"output":      u.CompletionTokens,
	}
}

type ctxSection struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	Chars  int    `json:"chars"`
}

type ctxTool struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// recordContext logs a "context" step: estimated size of each system prompt
// section, of the tool schemas (with the heaviest tools) and of the history,
// i.e. the fixed cost resent on every model call of the run.
func (r *Runner) recordContext(runID string, parts []promptPart, defs []llm.ToolDef, req Request) {
	var secs []ctxSection
	total := 0
	for _, p := range parts {
		t := estTokens(p.Text)
		secs = append(secs, ctxSection{p.Name, t, len(p.Text)})
		total += t
	}
	var tools []ctxTool
	toolTotal := 0
	for _, d := range defs {
		b, _ := json.Marshal(d)
		t := estTokens(string(b))
		tools = append(tools, ctxTool{d.Function.Name, t})
		toolTotal += t
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Tokens > tools[j].Tokens })
	hist := 0
	for _, m := range req.History {
		hist += estTokens(m.Content)
	}
	user := estTokens(req.Text)
	secs = append(secs,
		ctxSection{Name: "tool schemas (" + itoa(len(defs)) + ")", Tokens: toolTotal},
		ctxSection{Name: "thread history (" + itoa(len(req.History)) + " msgs)", Tokens: hist},
		ctxSection{Name: "user message", Tokens: user})
	total += toolTotal + hist + user
	r.stepUsage(runID, "context", "prompt", "", "", "ok", 0, total, map[string]any{"sections": secs, "tools": tools, "total": total})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
