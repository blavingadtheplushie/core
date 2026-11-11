package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"blavingad/internal/store"
)

// Upstream is one provider endpoint. KeyRef is a vault://Item/field
// reference, "config" (the agent's own LLM_API_KEY) or empty (no auth).
type Upstream struct {
	Name     string            `json:"name"`
	Style    string            `json:"style"` // openai | anthropic
	BaseURL  string            `json:"base_url"`
	KeyRef   string            `json:"key_ref"`
	Headers  map[string]string `json:"headers"`
	Models   []string          `json:"models"` // empty = accepts any model
	Priority int               `json:"priority"`
	Enabled  bool              `json:"enabled"`
}

type Mapping struct {
	Alias    string `json:"alias"`
	Upstream string `json:"upstream"`
	Model    string `json:"model"`
}

type Key struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Prefix       string   `json:"prefix"`
	Models       []string `json:"models"` // empty = every model
	Enabled      bool     `json:"enabled"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   string   `json:"last_used_at,omitempty"`
	Requests     int64    `json:"requests"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
}

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,40}$`)

func (p *Proxy) Upstreams() []Upstream {
	rows, err := p.st.DB.Query(`SELECT name, style, base_url, key_ref, headers, models, priority, enabled FROM llm_upstreams ORDER BY priority, name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Upstream
	for rows.Next() {
		var u Upstream
		var h, m string
		var en int
		if rows.Scan(&u.Name, &u.Style, &u.BaseURL, &u.KeyRef, &h, &m, &u.Priority, &en) == nil {
			json.Unmarshal([]byte(h), &u.Headers)
			json.Unmarshal([]byte(m), &u.Models)
			u.Enabled = en == 1
			out = append(out, u)
		}
	}
	return out
}

func (p *Proxy) SaveUpstream(u Upstream) error {
	u.Name = strings.ToLower(strings.TrimSpace(u.Name))
	if !reName.MatchString(u.Name) {
		return errors.New("name: lowercase letters, digits, . _ - (max 40)")
	}
	if u.Style != "openai" && u.Style != "anthropic" {
		return errors.New("style must be openai or anthropic")
	}
	u.BaseURL = strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	if !strings.HasPrefix(u.BaseURL, "http://") && !strings.HasPrefix(u.BaseURL, "https://") {
		return errors.New("base_url must start with http(s)://")
	}
	u.KeyRef = strings.TrimSpace(u.KeyRef)
	if u.KeyRef != "" && u.KeyRef != "config" && !strings.HasPrefix(u.KeyRef, "vault://") {
		return errors.New("key_ref must be a vault://Item/field reference (or \"config\"), never a raw key")
	}
	if u.Headers == nil {
		u.Headers = map[string]string{}
	}
	if u.Models == nil {
		u.Models = []string{}
	}
	en := 0
	if u.Enabled {
		en = 1
	}
	_, err := p.st.DB.Exec(`INSERT INTO llm_upstreams(name, style, base_url, key_ref, headers, models, priority, enabled, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET style=excluded.style, base_url=excluded.base_url, key_ref=excluded.key_ref,
		headers=excluded.headers, models=excluded.models, priority=excluded.priority, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		u.Name, u.Style, u.BaseURL, u.KeyRef, store.ToJSON(u.Headers), store.ToJSON(u.Models), u.Priority, en, store.Now(), store.Now())
	return err
}

func (p *Proxy) DeleteUpstream(name string) error {
	res, err := p.st.DB.Exec(`DELETE FROM llm_upstreams WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no upstream %q", name)
	}
	p.st.DB.Exec(`DELETE FROM llm_model_map WHERE upstream = ?`, name)
	return nil
}

func (p *Proxy) Mappings() []Mapping {
	rows, err := p.st.DB.Query(`SELECT alias, upstream, model FROM llm_model_map ORDER BY alias, upstream`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Mapping
	for rows.Next() {
		var m Mapping
		if rows.Scan(&m.Alias, &m.Upstream, &m.Model) == nil {
			out = append(out, m)
		}
	}
	return out
}

// SetMapping maps a public alias to a real model on one upstream ("*" = on
// every upstream).
func (p *Proxy) SetMapping(m Mapping) error {
	m.Alias, m.Upstream, m.Model = strings.TrimSpace(m.Alias), strings.TrimSpace(m.Upstream), strings.TrimSpace(m.Model)
	if m.Alias == "" || m.Upstream == "" || m.Model == "" {
		return errors.New("alias, upstream and model are required")
	}
	if m.Upstream != "*" && p.st.Count(`SELECT COUNT(*) FROM llm_upstreams WHERE name = ?`, m.Upstream) == 0 {
		return fmt.Errorf("no upstream %q", m.Upstream)
	}
	_, err := p.st.DB.Exec(`INSERT INTO llm_model_map(alias, upstream, model) VALUES(?,?,?) ON CONFLICT(alias, upstream) DO UPDATE SET model=excluded.model`, m.Alias, m.Upstream, m.Model)
	return err
}

func (p *Proxy) DeleteMapping(alias, upstream string) error {
	q, args := `DELETE FROM llm_model_map WHERE alias = ?`, []any{alias}
	if upstream != "" {
		q += ` AND upstream = ?`
		args = append(args, upstream)
	}
	_, err := p.st.DB.Exec(q, args...)
	return err
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// CreateKey returns the plaintext key once; only its hash is stored.
func (p *Proxy) CreateKey(name, by string, models []string) (Key, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Key{}, "", errors.New("a name is required")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Key{}, "", err
	}
	plain := "sk-blv-" + hex.EncodeToString(b)
	if models == nil {
		models = []string{}
	}
	k := Key{ID: store.NewID("key"), Name: name, Prefix: plain[:14], Models: models, Enabled: true, CreatedBy: by, CreatedAt: store.Now()}
	_, err := p.st.DB.Exec(`INSERT INTO llm_keys(id, name, prefix, hash, models, enabled, created_by, created_at) VALUES(?,?,?,?,?,1,?,?)`,
		k.ID, k.Name, k.Prefix, hashKey(plain), store.ToJSON(models), by, k.CreatedAt)
	return k, plain, err
}

func (p *Proxy) Keys() []Key {
	rows, err := p.st.DB.Query(`SELECT id, name, prefix, models, enabled, created_by, created_at, COALESCE(last_used_at,''), requests, input_tokens, output_tokens FROM llm_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var m string
		var en int
		if rows.Scan(&k.ID, &k.Name, &k.Prefix, &m, &en, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.Requests, &k.InputTokens, &k.OutputTokens) == nil {
			json.Unmarshal([]byte(m), &k.Models)
			k.Enabled = en == 1
			out = append(out, k)
		}
	}
	return out
}

// RevokeKey disables (by id, name or prefix) a key; delete=true removes it.
func (p *Proxy) RevokeKey(ref string, del bool) error {
	q := `UPDATE llm_keys SET enabled = 0 WHERE id = ? OR name = ? OR prefix = ?`
	if del {
		q = `DELETE FROM llm_keys WHERE id = ? OR name = ? OR prefix = ?`
	}
	res, err := p.st.DB.Exec(q, ref, ref, ref)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no key %q", ref)
	}
	return nil
}

func (p *Proxy) lookupKey(plain string) (Key, bool) {
	var k Key
	var m string
	var en int
	err := p.st.DB.QueryRow(`SELECT id, name, models, enabled FROM llm_keys WHERE hash = ?`, hashKey(plain)).Scan(&k.ID, &k.Name, &m, &en)
	if err != nil || en != 1 {
		return Key{}, false
	}
	json.Unmarshal([]byte(m), &k.Models)
	return k, true
}

// Aliases lists the public model names (for GET /v1/models).
func (p *Proxy) Aliases() []string {
	seen := map[string]bool{}
	for _, m := range p.Mappings() {
		seen[m.Alias] = true
	}
	for _, u := range p.Upstreams() {
		if u.Enabled {
			for _, m := range u.Models {
				seen[m] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
