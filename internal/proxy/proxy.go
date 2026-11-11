package proxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"blavingad/internal/store"
)

// Resolver turns a vault://Item/field reference into its value.
type Resolver func(ctx context.Context, ref string) (string, error)

type Proxy struct {
	st        *store.Store
	resolve   Resolver
	configKey string // value used for key_ref = "config"
	client    *http.Client
	Cooldown  time.Duration

	mu     sync.Mutex
	downTo map[string]time.Time // upstream → cooling down until
	lastEr map[string]string
}

func New(st *store.Store, resolve Resolver, configKey string) *Proxy {
	return &Proxy{
		st: st, resolve: resolve, configKey: configKey,
		client:   &http.Client{Timeout: 15 * time.Minute},
		Cooldown: 2 * time.Minute,
		downTo:   map[string]time.Time{}, lastEr: map[string]string{},
	}
}

func (p *Proxy) markDown(name, why string) {
	p.mu.Lock()
	p.downTo[name] = time.Now().Add(p.Cooldown)
	p.lastEr[name] = why
	p.mu.Unlock()
}

func (p *Proxy) markUp(name string) {
	p.mu.Lock()
	delete(p.downTo, name)
	p.mu.Unlock()
}

func (p *Proxy) isDown(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Before(p.downTo[name])
}

// Health reports per-upstream cooldown state.
type Health struct {
	Name      string `json:"name"`
	Down      bool   `json:"down"`
	Until     string `json:"until,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func (p *Proxy) Health() []Health {
	var out []Health
	for _, u := range p.Upstreams() {
		p.mu.Lock()
		h := Health{Name: u.Name, LastError: p.lastEr[u.Name]}
		if t := p.downTo[u.Name]; time.Now().Before(t) {
			h.Down, h.Until = true, t.UTC().Format(time.RFC3339)
		}
		p.mu.Unlock()
		out = append(out, h)
	}
	return out
}

// Seed creates the default upstream from the agent's own LLM config, once.
func (p *Proxy) Seed(name, style, baseURL string) {
	if baseURL == "" || p.st.Count(`SELECT COUNT(*) FROM llm_upstreams`) > 0 {
		return
	}
	p.SaveUpstream(Upstream{Name: name, Style: style, BaseURL: baseURL, KeyRef: "config", Priority: 10, Enabled: true})
}
