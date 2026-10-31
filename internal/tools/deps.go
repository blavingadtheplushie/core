// Package tools contains every built-in capability of BLAVINGAD.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/mail"
	"blavingad/internal/memory"
	"blavingad/internal/selfupdate"
	"blavingad/internal/store"
	"blavingad/internal/vault"
)

type Rescheduler interface{ Reload() }

type Deps struct {
	Cfg    *config.Config
	St     *store.Store
	Mem    *memory.Service
	LLM    *llm.Client
	Reg    *core.Registry
	Bus    *core.Bus
	Slack  *slack.Client
	Mail   *mail.Service
	Self   *selfupdate.Manager
	Sched  Rescheduler
	Access *core.Access
	Vault  *vault.Client
	// Reveal shows a secret to the human behind a run (Slack ephemeral).
	Reveal func(rc *core.RunContext, ref, value string) error
}

// RegisterAll wires every built-in tool plus the agent-authored ones.
func RegisterAll(d *Deps) {
	registerWeb(d)
	registerSlack(d)
	registerSlackBlocks(d)
	registerMemory(d)
	registerFiles(d)
	registerGitHub(d)
	registerEmail(d)
	registerDeploy(d)
	registerTunnel(d)
	registerSelf(d)
	registerSchedule(d)
	registerAccess(d)
	registerVault(d)
	registerElevenLabs(d)
	registerSlackUser(d)
	LoadCustomTools(d)
}

const maxOutput = 14000

// Clip keeps the head and the tail of long outputs — errors usually live at
// the end, context at the beginning.
func Clip(s string, max int) string {
	if max <= 0 {
		max = maxOutput
	}
	if len(s) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	return s[:head] + fmt.Sprintf("\n\n…[%d characters omitted]…\n\n", len(s)-max) + s[len(s)-tail:]
}

func JSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// resolvePath maps relative paths to the workspace and refuses paths outside
// the agent's playgrounds (workspace, its own source, data dir, /tmp).
func (d *Deps) resolvePath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(d.Cfg.WorkspaceDir, p)
	}
	p = filepath.Clean(p)
	home, _ := os.UserHomeDir()
	for _, root := range []string{d.Cfg.WorkspaceDir, d.Cfg.SourceDir, d.Cfg.DataDir, "/tmp", home} {
		if root == "" {
			continue
		}
		if p == root || strings.HasPrefix(p, filepath.Clean(root)+string(os.PathSeparator)) {
			return p, nil
		}
	}
	return "", fmt.Errorf("path outside the allowed areas (%s, %s, /tmp): %s", d.Cfg.WorkspaceDir, d.Cfg.SourceDir, p)
}

type cmdResult struct {
	Output   string
	ExitCode int
	Duration time.Duration
}

// sh runs a bash command with a timeout and returns combined output.
func (d *Deps) sh(ctx context.Context, dir, command string, timeout time.Duration, env ...string) cmdResult {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	if dir == "" {
		dir = d.Cfg.WorkspaceDir
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), d.toolEnv()...)
	cmd.Env = append(cmd.Env, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	err := cmd.Run()
	res := cmdResult{Output: out.String(), Duration: time.Since(start)}
	if err != nil {
		res.ExitCode = -1
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			res.Output += fmt.Sprintf("\n⏱️ timed out (%s)", timeout)
		}
	}
	return res
}

func (d *Deps) toolEnv() []string {
	env := []string{"HOME=" + homeDir(), "TERM=dumb", "NO_COLOR=1", "CI=1"}
	if d.Cfg.GitHubToken != "" {
		env = append(env, "GH_TOKEN="+d.Cfg.GitHubToken, "GITHUB_TOKEN="+d.Cfg.GitHubToken)
	}
	if d.Cfg.CloudflareToken != "" {
		env = append(env, "CLOUDFLARE_API_TOKEN="+d.Cfg.CloudflareToken, "CLOUDFLARE_ACCOUNT_ID="+d.Cfg.CloudflareAccount)
	}
	return env
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/root"
	}
	return h
}

func (r cmdResult) String() string {
	return Clip(fmt.Sprintf("exit=%d (%s)\n%s", r.ExitCode, r.Duration.Round(time.Millisecond), r.Output), 0)
}

func short(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func field(args json.RawMessage, key string) string {
	var m map[string]any
	json.Unmarshal(args, &m)
	if v, ok := m[key]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func summarizeField(prefix, key string, n int) func(json.RawMessage) string {
	return func(args json.RawMessage) string {
		if v := field(args, key); v != "" {
			return prefix + short(v, n)
		}
		return ""
	}
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
