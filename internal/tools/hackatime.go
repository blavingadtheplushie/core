package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Hackatime heartbeats: every fs_write / fs_edit inside a git repo sends a
// lightweight "ai coding" heartbeat to Blavingad's own Hackatime account.
// Fire-and-forget, debounced per file, silently disabled without a key.

const (
	hackatimeRef      = "vault://HACKATIME_API_KEY/password"
	hackatimeURL      = "https://hackatime.hackclub.com/api/hackatime/v1/users/current/heartbeats"
	hackatimeDebounce = 2 * time.Minute
)

var (
	hackatimeMu   sync.Mutex
	hackatimeLast = map[string]time.Time{}
)

var hackatimeLangs = map[string]string{
	".go": "Go", ".kt": "Kotlin", ".kts": "Kotlin", ".java": "Java", ".js": "JavaScript",
	".mjs": "JavaScript", ".jsx": "JavaScript", ".ts": "TypeScript", ".tsx": "TypeScript",
	".py": "Python", ".rs": "Rust", ".c": "C", ".h": "C", ".cpp": "C++", ".hpp": "C++",
	".cs": "C#", ".rb": "Ruby", ".php": "PHP", ".swift": "Swift", ".sh": "Bash",
	".html": "HTML", ".css": "CSS", ".scss": "SCSS", ".json": "JSON", ".yaml": "YAML",
	".yml": "YAML", ".toml": "TOML", ".md": "Markdown", ".sql": "SQL", ".xml": "XML",
	".lua": "Lua", ".dart": "Dart", ".vue": "Vue", ".svelte": "Svelte", ".gradle": "Groovy",
	".dockerfile": "Docker",
}

// gitRoot returns the closest parent folder containing .git, or "".
func gitRoot(p string) string {
	dir := filepath.Dir(p)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func hackatimeLanguage(p string) string {
	base := strings.ToLower(filepath.Base(p))
	if base == "dockerfile" {
		return "Docker"
	}
	if base == "makefile" {
		return "Makefile"
	}
	return hackatimeLangs[strings.ToLower(filepath.Ext(p))]
}

// hackatimeDue reports whether a heartbeat for this file is due (debounce).
func hackatimeDue(p string, now time.Time) bool {
	hackatimeMu.Lock()
	defer hackatimeMu.Unlock()
	if t, ok := hackatimeLast[p]; ok && now.Sub(t) < hackatimeDebounce {
		return false
	}
	hackatimeLast[p] = now
	return true
}

// hackatimeBeat sends a heartbeat in the background. Never blocks, never fails the tool.
func (d *Deps) hackatimeBeat(p string, lines int) {
	if !d.vaultReady() {
		return
	}
	root := gitRoot(p)
	if root == "" || !hackatimeDue(p, time.Now()) {
		return
	}
	go func() {
		defer func() { recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		key, err := d.resolveRef(ctx, hackatimeRef)
		key = strings.TrimSpace(key)
		if err != nil || key == "" {
			return
		}
		hb := map[string]any{
			"entity":   p,
			"type":     "file",
			"category": "ai coding",
			"time":     float64(time.Now().UnixNano()) / 1e9,
			"project":  filepath.Base(root),
			"is_write": true,
			"editor":   "blavingad",
			"plugin":   "blavingad/1.0",
			"machine":  "blavingad-lxc",
			"operating_system": "Linux",
		}
		if lang := hackatimeLanguage(p); lang != "" {
			hb["language"] = lang
		}
		if lines > 0 {
			hb["lines"] = lines
		}
		body, _ := json.Marshal([]any{hb})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, hackatimeURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "wakatime/blavingad (Linux) blavingad/1.0")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}
