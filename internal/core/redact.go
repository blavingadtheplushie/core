package core

import (
	"sort"
	"strings"
	"sync"
)

// Redactor masks known secret values (tokens from the config, passwords from
// the vault, secrets injected into tools) in anything that reaches the model,
// the database, Slack or the web UI.
type Redactor struct {
	mu       sync.RWMutex
	values   map[string]bool
	sorted   []string
	replacer *strings.Replacer
}

const Mask = "••••••"

// Secrets is the process-wide redactor.
var Secrets = &Redactor{values: map[string]bool{}}

// Add registers values to mask. Very short values are ignored: masking "1234"
// or "admin" everywhere would mangle normal text more than it protects.
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < 8 || r.values[v] {
			continue
		}
		r.values[v] = true
		changed = true
	}
	if !changed {
		return
	}
	r.sorted = r.sorted[:0]
	for v := range r.values {
		r.sorted = append(r.sorted, v)
	}
	// Longest first so a secret containing another is masked whole.
	sort.Slice(r.sorted, func(i, j int) bool { return len(r.sorted[i]) > len(r.sorted[j]) })
	pairs := make([]string, 0, 2*len(r.sorted))
	for _, v := range r.sorted {
		pairs = append(pairs, v, Mask)
	}
	r.replacer = strings.NewReplacer(pairs...)
}

func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	rep := r.replacer
	r.mu.RUnlock()
	if rep == nil || s == "" {
		return s
	}
	return rep.Replace(s)
}

func (r *Redactor) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.values)
}
