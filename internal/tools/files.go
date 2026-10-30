package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"blavingad/internal/core"
)

var safeCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true, "rg": true, "find": true, "pwd": true,
	"echo": true, "wc": true, "tree": true, "which": true, "date": true, "du": true, "df": true, "file": true,
	"stat": true, "jq": true, "sort": true, "uniq": true, "diff": true, "whoami": true, "uname": true,
	"uptime": true,
}

var safeSub = map[string]map[string]bool{
	"git":     {"status": true, "log": true, "diff": true, "show": true, "branch": true, "remote": true, "rev-parse": true, "ls-files": true, "blame": true},
	"go":      {"version": true, "env": true, "list": true, "doc": true, "vet": true},
	"node":    {"--version": true, "-v": true},
	"python3": {"--version": true},
	"bun":     {"--version": true},
	"npm":     {"--version": true, "ls": true, "view": true},
	"docker":  {"ps": true, "images": true, "logs": true, "inspect": true},
}

var shellMeta = regexp.MustCompile("[;&|><`$(){}]|\\bsudo\\b")

// readOnlyCommand recognises a conservative set of commands that cannot
// change anything, so they skip the approval prompt.
func readOnlyCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || shellMeta.MatchString(cmd) || strings.Contains(cmd, "\n") {
		return false
	}
	f := strings.Fields(cmd)
	name := f[0]
	if sub, ok := safeSub[name]; ok {
		if len(f) < 2 {
			return false
		}
		return sub[f[1]]
	}
	if name == "find" && (strings.Contains(cmd, "-delete") || strings.Contains(cmd, "-exec")) {
		return false
	}
	return safeCommands[name]
}

func registerFiles(d *Deps) {
	d.Reg.Add(&core.Tool{
		Name: "shell", Category: "code", Emoji: "🐚", Risk: core.RiskSensitive,
		Description: "Runs a bash command in your container (workspace by default). Available tools: git, gh, go, node/npm/npx, bun, python3/pip, wrangler, docker, curl, jq, rg. Read-only commands (ls, cat, git status…) run without approval. To use a password/token, NEVER write it in the command: pass secrets={\"PGPASSWORD\":\"vault://Postgres prod/password\"} and use $PGPASSWORD (the output is masked automatically).",
		Params: core.Obj(map[string]any{
			"command":     core.Str("Bash command."),
			"cwd":         core.Str("Working directory (default: workspace)."),
			"timeout_sec": core.Int("Timeout in seconds (default 120, max 1800)."),
			"secrets":     map[string]any{"type": "object", "description": "Environment variables to fill from the vault: NAME → vault://Item/field.", "additionalProperties": map[string]any{"type": "string"}},
		}, "command"),
		NeedsApproval: func(args json.RawMessage) bool {
			a, _ := core.Parse[struct {
				Command string            `json:"command"`
				Secrets map[string]string `json:"secrets"`
			}](args)
			return len(a.Secrets) > 0 || !readOnlyCommand(a.Command)
		},
		Summarize: func(args json.RawMessage) string {
			a, _ := core.Parse[struct {
				Command string            `json:"command"`
				Secrets map[string]string `json:"secrets"`
			}](args)
			label := "$ " + short(a.Command, 90)
			if len(a.Secrets) > 0 {
				var refs []string
				for k, v := range a.Secrets {
					refs = append(refs, k+"←"+v)
				}
				sort.Strings(refs)
				label += "  🔐 " + strings.Join(refs, ", ")
			}
			return label
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Command    string            `json:"command"`
				Cwd        string            `json:"cwd"`
				TimeoutSec int               `json:"timeout_sec"`
				Secrets    map[string]string `json:"secrets"`
			}](args)
			if err != nil {
				return "", err
			}
			secretEnv, err := d.secretEnv(ctx, rc, a.Secrets)
			if err != nil {
				return "", err
			}
			dir := d.Cfg.WorkspaceDir
			if a.Cwd != "" {
				if dir, err = d.resolvePath(a.Cwd); err != nil {
					return "", err
				}
			}
			if a.TimeoutSec <= 0 {
				a.TimeoutSec = 120
			}
			if a.TimeoutSec > 1800 {
				a.TimeoutSec = 1800
			}
			return d.sh(ctx, dir, a.Command, time.Duration(a.TimeoutSec)*time.Second, secretEnv...).String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "fs_read", Category: "code", Emoji: "📖",
		Description: "Reads a text file (with line numbers). Relative paths = workspace. Your own source code is in " + d.Cfg.SourceDir + ".",
		Params: core.Obj(map[string]any{
			"path":   core.Str("File path."),
			"offset": core.Int("First line (1-based, default 1)."),
			"limit":  core.Int("Number of lines (default 400)."),
		}, "path"),
		Summarize: summarizeField("Reading ", "path", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Path          string
				Offset, Limit int
			}](args)
			p, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			f, err := os.Open(p)
			if err != nil {
				return "", err
			}
			defer f.Close()
			if a.Offset < 1 {
				a.Offset = 1
			}
			if a.Limit <= 0 {
				a.Limit = 400
			}
			var b strings.Builder
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			line, shown, total := 0, 0, 0
			for sc.Scan() {
				line++
				total = line
				if line >= a.Offset && shown < a.Limit {
					fmt.Fprintf(&b, "%5d│ %s\n", line, sc.Text())
					shown++
				}
			}
			if shown == 0 {
				return fmt.Sprintf("(file has %d lines, nothing to show from line %d)", total, a.Offset), nil
			}
			if a.Offset+shown-1 < total {
				fmt.Fprintf(&b, "… (%d lines total, continue with offset=%d)\n", total, a.Offset+shown)
			}
			return Clip(b.String(), 40000), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "fs_write", Category: "code", Emoji: "📝",
		Description: "Creates or fully replaces a file (parent folders are created).",
		Params: core.Obj(map[string]any{
			"path":    core.Str("File path."),
			"content": core.Str("Full content."),
		}, "path", "content"),
		Summarize: summarizeField("Writing ", "path", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Path, Content string }](args)
			p, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Wrote %s (%d bytes).", p, len(a.Content)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "fs_edit", Category: "code", Emoji: "✂️",
		Description: "Replaces an exact passage in a file. old_text must be unique unless replace_all=true. Prefer it over fs_write for small changes.",
		Params: core.Obj(map[string]any{
			"path":        core.Str("File path."),
			"old_text":    core.Str("Exact text to replace (including indentation)."),
			"new_text":    core.Str("New text."),
			"replace_all": core.Bool("Replace all occurrences."),
		}, "path", "old_text", "new_text"),
		Summarize: summarizeField("Editing ", "path", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Path       string `json:"path"`
				OldText    string `json:"old_text"`
				NewText    string `json:"new_text"`
				ReplaceAll bool   `json:"replace_all"`
			}](args)
			p, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			s := string(data)
			n := strings.Count(s, a.OldText)
			if a.OldText == "" || n == 0 {
				return "", errors.New("old_text not found — re-read the file with fs_read and copy the exact passage")
			}
			if n > 1 && !a.ReplaceAll {
				return "", fmt.Errorf("old_text appears %d times — add context or set replace_all=true", n)
			}
			if a.ReplaceAll {
				s = strings.ReplaceAll(s, a.OldText, a.NewText)
			} else {
				s = strings.Replace(s, a.OldText, a.NewText, 1)
			}
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s edited (%d replacement(s)).", p, map[bool]int{true: n, false: 1}[a.ReplaceAll]), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "fs_list", Category: "code", Emoji: "🗃️",
		Description: "Lists a folder's tree (skips .git, node_modules…).",
		Params: core.Obj(map[string]any{
			"path":  core.Str("Folder (default: workspace)."),
			"depth": core.Int("Max depth (default 3)."),
		}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Path  string
				Depth int
			}](args)
			root, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			if a.Depth <= 0 {
				a.Depth = 3
			}
			var lines []string
			filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
				if err != nil || len(lines) > 600 {
					return nil
				}
				rel, _ := filepath.Rel(root, p)
				if rel == "." {
					return nil
				}
				if e.IsDir() && (e.Name() == ".git" || e.Name() == "node_modules" || e.Name() == ".wrangler" || e.Name() == "vendor" || e.Name() == "__pycache__") {
					return filepath.SkipDir
				}
				depth := strings.Count(rel, string(os.PathSeparator)) + 1
				if depth > a.Depth {
					if e.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				suffix := ""
				if e.IsDir() {
					suffix = "/"
				} else if info, err := e.Info(); err == nil {
					suffix = fmt.Sprintf("  (%d B)", info.Size())
				}
				lines = append(lines, strings.Repeat("  ", depth-1)+e.Name()+suffix)
				return nil
			})
			if len(lines) == 0 {
				return "(empty)", nil
			}
			return root + "\n" + strings.Join(lines, "\n"), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "fs_search", Category: "code", Emoji: "🔦",
		Description: "Searches a folder's files for a regex (ripgrep). Returns file:line:content.",
		Params: core.Obj(map[string]any{
			"pattern": core.Str("Regular expression."),
			"path":    core.Str("Folder (default: workspace)."),
			"glob":    core.Str("File filter (e.g. *.go)."),
		}, "pattern"),
		Summarize: summarizeField("Search: ", "pattern", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Pattern, Path, Glob string }](args)
			root, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			cmdArgs := []string{"-n", "--no-heading", "--max-columns", "300", "-m", "50"}
			if a.Glob != "" {
				cmdArgs = append(cmdArgs, "-g", a.Glob)
			}
			cmdArgs = append(cmdArgs, "-e", a.Pattern, root)
			out, err := exec.CommandContext(ctx, "rg", cmdArgs...).CombinedOutput()
			if err != nil && len(out) == 0 {
				if _, lookErr := exec.LookPath("rg"); lookErr != nil {
					return goGrep(root, a.Pattern)
				}
				return "No matches.", nil
			}
			return Clip(string(out), 0), nil
		},
	})
}

func goGrep(root, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	var hits []string
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || len(hits) > 200 {
			return nil
		}
		if e.IsDir() && (e.Name() == ".git" || e.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if e.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 2<<20 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d:%s", p, i+1, short(line, 300)))
			}
		}
		return nil
	})
	sort.Strings(hits)
	if len(hits) == 0 {
		return "No matches.", nil
	}
	return strings.Join(hits, "\n"), nil
}
