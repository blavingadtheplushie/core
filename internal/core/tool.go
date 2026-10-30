// Package core holds the types shared by the harness, the tools and the
// surfaces (Slack, web, email): tool registry, policies, run context, the
// event bus and the approval manager.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"blavingad/internal/llm"
	"blavingad/internal/store"
)

type Risk int

const (
	// RiskSafe tools run without asking (reads, searches, memory).
	RiskSafe Risk = iota
	// RiskSensitive tools have side effects outside the agent and ask for
	// approval by default (shell, deploys, emails, pushes).
	RiskSensitive
)

type Policy string

const (
	PolicyAuto    Policy = "auto"
	PolicyApprove Policy = "approve"
	PolicyOff     Policy = "off"
)

type Tool struct {
	Name        string
	Description string
	Category    string
	Emoji       string
	Params      map[string]any
	Risk        Risk
	// Custom marks agent-authored tools (hot-loaded from the database).
	Custom bool
	// Available hides tools whose integration is not configured.
	Available func() bool
	// NeedsApproval refines the policy per call (e.g. read-only shell
	// commands or GET requests skip the prompt). Only consulted when the
	// effective policy is "approve".
	NeedsApproval func(args json.RawMessage) bool
	// Summarize renders a short human label for progress/approval UIs.
	Summarize func(args json.RawMessage) string
	Handler   func(ctx context.Context, rc *RunContext, args json.RawMessage) (string, error)
}

func (t *Tool) IsAvailable() bool { return t.Available == nil || t.Available() }

func (t *Tool) Label(args json.RawMessage) string {
	if t.Summarize != nil {
		if s := t.Summarize(args); s != "" {
			return s
		}
	}
	return t.Name
}

// Obj builds a JSON-schema object; props are name → schema.
func Obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func Str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func Int(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func Bool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func Enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}
func StrList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// Parse decodes tool args leniently (models occasionally send "" or
// stringified JSON).
func Parse[T any](args json.RawMessage) (T, error) {
	var v T
	raw := strings.TrimSpace(string(args))
	if raw == "" || raw == "null" {
		raw = "{}"
	}
	if strings.HasPrefix(raw, `"`) {
		var inner string
		if json.Unmarshal([]byte(raw), &inner) == nil {
			raw = inner
		}
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, fmt.Errorf("arguments invalides: %w", err)
	}
	return v, nil
}

// ---------- registry ----------

type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
	st    *store.Store
}

func NewRegistry(st *store.Store) *Registry {
	return &Registry{tools: map[string]*Tool{}, st: st}
}

func (r *Registry) Add(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.Params == nil {
		t.Params = Obj(map[string]any{})
	}
	if t.Emoji == "" {
		t.Emoji = "🔧"
	}
	r.tools[t.Name] = t
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) All() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (r *Registry) Policy(t *Tool) Policy {
	switch Policy(r.st.GetSetting("policy."+t.Name, "")) {
	case PolicyAuto:
		return PolicyAuto
	case PolicyApprove:
		return PolicyApprove
	case PolicyOff:
		return PolicyOff
	}
	return DefaultPolicy(t)
}

func DefaultPolicy(t *Tool) Policy {
	if t.Risk == RiskSensitive {
		return PolicyApprove
	}
	return PolicyAuto
}

func (r *Registry) SetPolicy(name string, p Policy) error {
	t, ok := r.Get(name)
	if !ok {
		return fmt.Errorf("unknown tool: %s", name)
	}
	if p == DefaultPolicy(t) {
		return r.st.DeleteSetting("policy." + name)
	}
	return r.st.SetSetting("policy."+name, string(p))
}

// Definitions returns the OpenAI tool schema for every enabled, available
// tool, optionally restricted to an allowlist.
func (r *Registry) Definitions(allow map[string]bool, deny map[string]bool) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, t := range r.All() {
		if !t.IsAvailable() || r.Policy(t) == PolicyOff {
			continue
		}
		if allow != nil && !allow[t.Name] {
			continue
		}
		if deny != nil && deny[t.Name] {
			continue
		}
		desc := t.Description
		if r.Policy(t) == PolicyApprove {
			desc += " (⚠️ requires human approval)"
		}
		defs = append(defs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: t.Name, Description: desc, Parameters: t.Params}})
	}
	return defs
}
