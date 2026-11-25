package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/llm"
)

// Model router: cheap "fast" endpoint for chat and small tasks, the main
// model (opus) for heavy work. A run starts on the fast model only when the
// request looks light; it escalates to the main model mid-run when the fast
// model reaches for a heavy tool, fails, or loops too long.

const routerSettingKey = "router.config"

type RouterConfig struct {
	Enabled bool `json:"enabled"`
	// Fast endpoint (any OpenAI/Anthropic-compatible API).
	Style   string `json:"style"`    // openai | anthropic
	BaseURL string `json:"base_url"` // e.g. https://api.mistral.ai/v1
	KeyRef  string `json:"key_ref"`  // vault://Item/field ("" = no auth)
	Model   string `json:"model"`
	// Which run sources may start on the fast model.
	Sources []string `json:"sources"`
	// Requests containing one of these words go straight to the main model.
	HeavyKeywords []string `json:"heavy_keywords"`
	// Requests longer than this (characters) go to the main model (0 = no limit).
	MaxChars int `json:"max_chars"`
	// Escalate when the fast model calls one of these tools.
	HeavyTools []string `json:"heavy_tools"`
	// Escalate after this many iterations on the fast model (0 = never).
	EscalateAfter int `json:"escalate_after"`
	// Escalate after this many failed tool calls (0 = never).
	EscalateOnErrors int `json:"escalate_on_errors"`
	// Background calls (reflection, channel digests, compaction) use the fast model too.
	Background bool `json:"background"`
}

func DefaultRouterConfig() RouterConfig {
	return RouterConfig{
		Style:   llm.StyleOpenAI,
		BaseURL: "https://api.mistral.ai/v1",
		KeyRef:  "vault://MISTRAL_API_KEY/password",
		Model:   "deepseek-v4-1-flash-1017",
		Sources: []string{"slack", "discord", "signal", "web", "schedule", "email"},
		HeavyKeywords: []string{"code", "debug", "bug", "refactor", "self_upgrade", "upgrade", "deploy", "déploie", "implement", "implémente",
			"script", "fix", "build", "setup", "migration", "stack trace", "traceback", "crash", "go test", "pull request", "commit"},
		MaxChars:         700,
		HeavyTools:       []string{"self_upgrade", "self_build", "self_rollback", "fs_edit", "fs_write", "delegate", "tool_create", "deploy_cloudflare_pages", "deploy_cloudflare_worker", "deploy_github_pages", "orchard"},
		EscalateAfter:    8,
		EscalateOnErrors: 2,
		Background:       true,
	}
}

var RouterSources = []string{"slack", "discord", "signal", "web", "schedule", "email"}

// RouterConfig reads the saved config over the defaults.
func (r *Runner) RouterConfig() RouterConfig {
	c := DefaultRouterConfig()
	if raw := r.st.GetSetting(routerSettingKey, ""); raw != "" {
		json.Unmarshal([]byte(raw), &c)
	}
	return c
}

func (r *Runner) SaveRouterConfig(c RouterConfig) error {
	c.Style = strings.TrimSpace(c.Style)
	if c.Style == "" {
		c.Style = llm.StyleOpenAI
	}
	if c.Style != llm.StyleOpenAI && c.Style != llm.StyleAnthropic {
		return errors.New("style must be openai or anthropic")
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL != "" {
		if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("base URL must be an http(s) URL")
		}
	}
	c.KeyRef = strings.TrimSpace(c.KeyRef)
	if c.KeyRef != "" && !strings.HasPrefix(c.KeyRef, "vault://") {
		return errors.New("key must be a vault://Item/field reference, never a raw key")
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Enabled && (c.Model == "" || c.BaseURL == "") {
		return errors.New("a base URL and a model are required to enable the router")
	}
	if c.MaxChars < 0 || c.EscalateAfter < 0 || c.EscalateOnErrors < 0 {
		return errors.New("limits must be >= 0")
	}
	c.Sources = cleanList(c.Sources, false)
	c.HeavyKeywords = cleanList(c.HeavyKeywords, true)
	c.HeavyTools = cleanList(c.HeavyTools, false)
	b, _ := json.Marshal(c)
	r.fastMu.Lock()
	r.fastKey, r.fastKeyAt = "", time.Time{} // re-resolve the key
	r.fastMu.Unlock()
	return r.st.SetSetting(routerSettingKey, string(b))
}

func cleanList(in []string, lower bool) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if lower {
			s = strings.ToLower(s)
		}
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// SetKeyResolver lets the router resolve its vault:// key reference.
func (r *Runner) SetKeyResolver(f func(ctx context.Context, ref string) (string, error)) {
	r.resolveKey = f
}

// fastAPIKey resolves (and caches for 10 min) the fast endpoint's key.
func (r *Runner) fastAPIKey(c RouterConfig) (string, error) {
	if c.KeyRef == "" {
		return "", nil
	}
	r.fastMu.Lock()
	defer r.fastMu.Unlock()
	if r.fastKey != "" && r.fastKeyRef == c.KeyRef && time.Since(r.fastKeyAt) < 10*time.Minute {
		return r.fastKey, nil
	}
	if r.resolveKey == nil {
		return "", errors.New("vault unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	k, err := r.resolveKey(ctx, c.KeyRef)
	if err != nil {
		return "", err
	}
	if k == "" {
		return "", fmt.Errorf("%s is empty", c.KeyRef)
	}
	core.Secrets.Add(k)
	r.fastKey, r.fastKeyRef, r.fastKeyAt = k, c.KeyRef, time.Now()
	return k, nil
}

// FastClient is the client for the fast endpoint; it reads the router
// config on every call, so dashboard edits apply immediately.
func (r *Runner) FastClient() *llm.Client {
	r.fastOnce.Do(func() {
		c := llm.New("", "")
		c.Endpoint = func() (string, string) {
			rc := r.RouterConfig()
			return rc.Style, rc.BaseURL
		}
		c.KeyFunc = func() string {
			k, _ := r.fastAPIKey(r.RouterConfig())
			return k
		}
		r.fast = c
	})
	return r.fast
}

// RouterReady tells whether the fast endpoint can be used, and why not.
func (r *Runner) RouterReady(c RouterConfig) (bool, string) {
	if !c.Enabled {
		return false, "router off"
	}
	if c.Model == "" || c.BaseURL == "" {
		return false, "fast endpoint not configured"
	}
	if _, err := r.fastAPIKey(c); err != nil {
		return false, "fast model key unavailable: " + err.Error()
	}
	return true, ""
}

// Route picks the model a run starts on. fast=false means the main model.
func (r *Runner) Route(req Request) (fast bool, reason string) {
	c := r.RouterConfig()
	if ok, why := r.RouterReady(c); !ok {
		return false, why
	}
	return routeDecision(c, req)
}

// routeDecision is the pure heuristic (tested without a vault).
func routeDecision(c RouterConfig, req Request) (bool, string) {
	text := strings.ToLower(req.Text)
	switch {
	case strings.Contains(text, "!opus"):
		return false, "forced with !opus"
	case strings.Contains(text, "!fast"):
		return true, "forced with !fast"
	case req.Depth > 0:
		return false, "sub-agent (delegate) work"
	case req.Source == "self":
		return false, "self-review"
	case !slices.Contains(c.Sources, req.Source):
		return false, "source " + req.Source + " not routed"
	case len(req.Images) > 0:
		return false, "images attached (vision)"
	case c.MaxChars > 0 && len([]rune(req.Text)) > c.MaxChars:
		return false, fmt.Sprintf("long request (%d chars > %d)", len([]rune(req.Text)), c.MaxChars)
	}
	for _, kw := range c.HeavyKeywords {
		if kw != "" && containsWord(text, kw) {
			return false, fmt.Sprintf("heavy keyword %q", kw)
		}
	}
	return true, "light request"
}

// containsWord matches kw at word boundaries (so "fix" doesn't hit "prefix").
func containsWord(text, kw string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], kw)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isWordByte(text[j-1])
		end := j + len(kw)
		after := end >= len(text) || !isWordByte(text[end])
		if before && after {
			return true
		}
		i = j + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= 0x80 || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// heavyCall returns the first heavy tool among the calls.
func heavyCall(c RouterConfig, calls []llm.ToolCall) string {
	for _, tc := range calls {
		if slices.Contains(c.HeavyTools, tc.Function.Name) {
			return tc.Function.Name
		}
	}
	return ""
}

// bgChat runs a background call (reflection, digest, compaction) on the
// fast endpoint when the router allows it, else on the fast_model setting.
func (r *Runner) bgChat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	c := r.RouterConfig()
	if c.Background {
		if ok, _ := r.RouterReady(c); ok {
			fr := req
			fr.Model = c.Model
			if resp, err := r.FastClient().Chat(ctx, fr); err == nil {
				return resp, nil
			}
		}
	}
	return r.llm.Chat(ctx, req)
}

// chatClient returns the client a run currently talks to.
func (r *Runner) chatClient(s Settings) *llm.Client {
	if s.onFast {
		return r.FastClient()
	}
	return r.llm
}

