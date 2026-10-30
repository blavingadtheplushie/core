package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/store"
)

type Manager struct {
	cfg     *config.Config
	st      *store.Store
	bus     *core.Bus
	p       paths
	Version string
	mu      sync.Mutex
	restart chan string
}

type pending struct {
	EvolutionID string `json:"evolution_id"`
	Summary     string `json:"summary"`
	Commit      string `json:"commit"`
	Channel     string `json:"channel"`
	ThreadTS    string `json:"thread_ts"`
}

func NewManager(cfg *config.Config, st *store.Store, bus *core.Bus, version string) *Manager {
	return &Manager{cfg: cfg, st: st, bus: bus, p: binPaths(cfg.DataDir), Version: version, restart: make(chan string, 1)}
}

// RestartRequests fires when the agent wants the supervisor to swap binaries.
func (m *Manager) RestartRequests() <-chan string { return m.restart }

func (m *Manager) RequestRestart(reason string) {
	select {
	case m.restart <- reason:
	default:
	}
}

func run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (m *Manager) git(ctx context.Context, args ...string) (string, error) {
	return run(ctx, m.cfg.SourceDir, nil, "git", args...)
}

// ConfigureGit sets identity and GitHub credentials for every git call the
// agent makes (its own repo and anything it clones in the workspace).
func (m *Manager) ConfigureGit() {
	ctx := context.Background()
	home, _ := os.UserHomeDir()
	run(ctx, "", nil, "git", "config", "--global", "user.name", m.cfg.GitAuthorName)
	if m.cfg.GitAuthorEmail != "" {
		run(ctx, "", nil, "git", "config", "--global", "user.email", m.cfg.GitAuthorEmail)
	} else {
		run(ctx, "", nil, "git", "config", "--global", "user.email", strings.ToLower(m.cfg.AgentName)+"@users.noreply.github.com")
	}
	run(ctx, "", nil, "git", "config", "--global", "init.defaultBranch", "main")
	run(ctx, "", nil, "git", "config", "--global", "safe.directory", "*")
	if m.cfg.GitHubToken != "" {
		user := m.cfg.GitHubUser
		if user == "" {
			user = "x-access-token"
		}
		cred := fmt.Sprintf("https://%s:%s@github.com\n", user, m.cfg.GitHubToken)
		os.WriteFile(filepath.Join(home, ".git-credentials"), []byte(cred), 0o600)
		run(ctx, "", nil, "git", "config", "--global", "credential.helper", "store")
	}
}

// EnsureSource makes sure the agent has a git checkout of its own code.
func (m *Manager) EnsureSource(seedDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	src := m.cfg.SourceDir
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		os.MkdirAll(filepath.Dir(src), 0o755)
		cloned := false
		if m.cfg.SelfRepo != "" && m.cfg.GitHubToken != "" {
			url := "https://github.com/" + m.cfg.SelfRepo + ".git"
			if out, err := run(ctx, "", nil, "git", "clone", "--branch", m.cfg.SelfRepoBranch, url, src); err == nil {
				cloned = true
				slog.Info("self source cloned", "repo", m.cfg.SelfRepo)
			} else {
				slog.Warn("self repo clone failed, seeding from image", "out", out)
				os.RemoveAll(src)
			}
		}
		if !cloned {
			if _, err := os.Stat(seedDir); err != nil {
				return fmt.Errorf("no source to copy (%s)", seedDir)
			}
			if out, err := run(ctx, "", nil, "cp", "-a", seedDir+"/.", src); err != nil {
				return fmt.Errorf("copying the source: %s", out)
			}
		}
	}
	seeded := false
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		m.git(ctx, "init")
		m.git(ctx, "add", "-A")
		m.git(ctx, "commit", "-m", "🌱 initial: source of "+m.cfg.AgentName)
		seeded = true
	}
	if m.cfg.SelfRepo != "" && m.cfg.GitHubToken != "" {
		url := "https://github.com/" + m.cfg.SelfRepo + ".git"
		if _, err := m.git(ctx, "remote", "get-url", "origin"); err != nil {
			m.git(ctx, "remote", "add", "origin", url)
		} else {
			m.git(ctx, "remote", "set-url", "origin", url)
		}
		if seeded {
			// Publish the starting point so every later self-upgrade is a
			// readable diff on GitHub. Fails harmlessly if the repo has history.
			if out, err := m.git(ctx, "push", "origin", "HEAD:"+m.cfg.SelfRepoBranch); err != nil {
				slog.Warn("initial push to self repo failed", "out", out)
			}
		}
	}
	return nil
}

type Info struct {
	Version     string   `json:"version"`
	SourceDir   string   `json:"source_dir"`
	SelfRepo    string   `json:"self_repo"`
	Branch      string   `json:"branch"`
	Head        string   `json:"head"`
	Dirty       []string `json:"dirty"`
	Log         []Commit `json:"log"`
	SelfBuilt   bool     `json:"self_built"`
	HasPrevious bool     `json:"has_previous"`
	// Write access to the source tree (the agent edits it in place).
	Writable     bool   `json:"source_writable"`
	WriteProblem string `json:"source_write_problem,omitempty"`
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

func (m *Manager) Info(ctx context.Context) Info {
	info := Info{Version: m.Version, SourceDir: m.cfg.SourceDir, SelfRepo: m.cfg.SelfRepo,
		SelfBuilt: exists(m.p.current), HasPrevious: exists(m.p.prev)}
	wc := m.WriteStatus()
	info.Writable = wc.Writable
	if !wc.Writable {
		info.WriteProblem = fmt.Sprintf("%d file(s) not writable, e.g. %s: %s", wc.Failures, wc.Example, wc.Cause)
	}
	info.Branch, _ = m.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	info.Head, _ = m.git(ctx, "rev-parse", "--short", "HEAD")
	if out, err := m.git(ctx, "status", "--porcelain"); err == nil && out != "" {
		info.Dirty = strings.Split(out, "\n")
	}
	if out, err := m.git(ctx, "log", "-n", "30", "--pretty=format:%h\x1f%s\x1f%an\x1f%aI"); err == nil && out != "" {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, "\x1f")
			if len(f) == 4 {
				info.Log = append(info.Log, Commit{SHA: f[0], Subject: f[1], Author: f[2], Date: f[3]})
			}
		}
	}
	return info
}

func (m *Manager) Diff(ctx context.Context) string {
	out, _ := m.git(ctx, "diff", "HEAD", "--stat")
	full, _ := m.git(ctx, "diff", "HEAD")
	if len(full) > 30000 {
		full = full[:30000] + "\n…[diff truncated]"
	}
	return out + "\n\n" + full
}

// Build vets, compiles and tests the source tree. The binary lands in a
// temporary path; nothing is promoted here.
func (m *Manager) Build(ctx context.Context, runTests bool) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var log strings.Builder
	step := func(title string, args ...string) error {
		log.WriteString("$ " + strings.Join(args, " ") + "\n")
		out, err := run(ctx, m.cfg.SourceDir, []string{"CGO_ENABLED=0"}, args[0], args[1:]...)
		if out != "" {
			log.WriteString(out + "\n")
		}
		if err != nil {
			return fmt.Errorf("%s failed: %w", title, err)
		}
		return nil
	}
	if err := step("go mod tidy", "go", "mod", "tidy"); err != nil {
		return "", log.String(), err
	}
	if err := step("go vet", "go", "vet", "./..."); err != nil {
		return "", log.String(), err
	}
	tmp := filepath.Join(m.p.dir, "blavingad.build")
	os.MkdirAll(m.p.dir, 0o755)
	ver := fmt.Sprintf("-X main.version=self-%s", time.Now().UTC().Format("20060102-150405"))
	if err := step("go build", "go", "build", "-trimpath", "-ldflags", "-s -w "+ver, "-o", tmp, "./cmd/blavingad"); err != nil {
		return "", log.String(), err
	}
	if runTests {
		if err := step("go test", "go", "test", "./..."); err != nil {
			return "", log.String(), err
		}
	}
	// Smoke-test: the new binary must at least start and print its version.
	if err := step("smoke test", tmp, "version"); err != nil {
		return "", log.String(), err
	}
	return tmp, log.String(), nil
}

type UpgradeResult struct {
	EvolutionID string `json:"evolution_id"`
	Commit      string `json:"commit"`
	Pushed      bool   `json:"pushed"`
	BuildLog    string `json:"build_log"`
}

// Upgrade builds, commits, pushes and stages the new binary, then asks for a
// restart. The supervisor rolls back automatically if it crashes.
func (m *Manager) Upgrade(ctx context.Context, rc *core.RunContext, summary string) (UpgradeResult, error) {
	var res UpgradeResult
	bin, log, err := m.Build(ctx, true)
	res.BuildLog = log
	if err != nil {
		return res, err
	}
	m.git(ctx, "add", "-A")
	if out, err := m.git(ctx, "commit", "-m", "🧬 "+summary, "-m", "Self-improvement of "+m.cfg.AgentName+" (run "+rc.RunID+")"); err != nil && !strings.Contains(out, "nothing to commit") {
		return res, fmt.Errorf("git commit: %s", out)
	}
	res.Commit, _ = m.git(ctx, "rev-parse", "--short", "HEAD")
	if m.cfg.SelfRepo != "" && m.cfg.GitHubToken != "" {
		if out, err := m.git(ctx, "push", "origin", "HEAD:"+m.cfg.SelfRepoBranch); err == nil {
			res.Pushed = true
		} else {
			res.BuildLog += "\n⚠️ push failed: " + out
		}
	}
	if err := os.Rename(bin, m.p.next); err != nil {
		return res, err
	}
	if head, err := m.git(ctx, "rev-parse", "HEAD"); err == nil {
		os.WriteFile(m.builtSHAPath(), []byte(head), 0o644)
	}
	res.EvolutionID = store.NewID("evo")
	m.st.DB.Exec(`INSERT INTO evolutions(id, kind, summary, details, commit_sha, status, run_id, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		res.EvolutionID, "code", summary, truncate(log, 20000), res.Commit, "staged", rc.RunID, store.Now())
	pend := pending{EvolutionID: res.EvolutionID, Summary: summary, Commit: res.Commit, Channel: rc.Channel, ThreadTS: firstNonEmpty(rc.ThreadTS, rc.MessageTS)}
	b, _ := json.Marshal(pend)
	os.WriteFile(filepath.Join(m.p.dir, "pending.json"), b, 0o644)
	m.st.Audit(rc.Actor(), "self.upgrade", res.Commit, map[string]string{"summary": summary})
	m.bus.Publish("evolution", map[string]string{"id": res.EvolutionID, "status": "staged", "summary": summary})
	m.RequestRestart("upgrade: " + summary)
	return res, nil
}

// UpdateOptions drive a human-triggered update (CLI or dashboard).
type UpdateOptions struct {
	Pull  bool   // git pull from SELF_REPO first
	Force bool   // rebuild even if the source did not change
	Actor string // who asked
	Log   func(string)
	// Restart is called once the new binary is staged.
	Restart func(reason string)
}

var ErrUpToDate = errors.New("already up to date")

func (m *Manager) builtSHAPath() string { return filepath.Join(m.p.dir, "built.sha") }

// Update rebuilds BLAVINGAD from its source tree (optionally pulling it
// first) and restarts on the new binary — no image rebuild needed. The
// supervisor's probation/rollback applies exactly as for self-upgrades.
func (m *Manager) Update(ctx context.Context, opt UpdateOptions) (UpgradeResult, error) {
	var res UpgradeResult
	logf := func(format string, a ...any) {
		if opt.Log != nil {
			opt.Log(fmt.Sprintf(format, a...))
		}
	}
	dirty, _ := m.git(ctx, "status", "--porcelain")
	before, _ := m.git(ctx, "rev-parse", "HEAD")
	if opt.Pull {
		if m.cfg.SelfRepo == "" {
			return res, errors.New("no SELF_REPO configured: nothing to pull from (copy files into " + m.cfg.SourceDir + " and update without --pull)")
		}
		if strings.TrimSpace(dirty) != "" {
			return res, fmt.Errorf("uncommitted changes in %s:\n%s\ncommit or discard them first, or update without pull", m.cfg.SourceDir, dirty)
		}
		logf("⬇️  Fetching %s (%s)…", m.cfg.SelfRepo, m.cfg.SelfRepoBranch)
		if out, err := m.git(ctx, "fetch", "origin", m.cfg.SelfRepoBranch); err != nil {
			return res, fmt.Errorf("git fetch: %s", out)
		}
		target := "origin/" + m.cfg.SelfRepoBranch
		if _, err := m.git(ctx, "merge", "--ff-only", target); err != nil {
			// The agent may have local commits (pushed self-upgrades are
			// already upstream); replay them on top of what was pulled.
			logf("↪️  Not a fast-forward, rebasing local commits onto %s…", target)
			if out, err := m.git(ctx, "rebase", target); err != nil {
				m.git(ctx, "rebase", "--abort")
				return res, fmt.Errorf("could not reconcile local commits with %s (conflict):\n%s", target, truncate(out, 2000))
			}
		}
	}
	after, _ := m.git(ctx, "rev-parse", "HEAD")
	built, _ := os.ReadFile(m.builtSHAPath())
	if !opt.Force && strings.TrimSpace(dirty) == "" && strings.TrimSpace(string(built)) == after {
		return res, ErrUpToDate
	}
	if before != after {
		if log, err := m.git(ctx, "log", "--oneline", before+".."+after); err == nil && log != "" {
			logf("📦 New commits:\n%s", log)
		}
	}
	logf("🏗️  Building and testing (go vet, go build, go test, smoke test)…")
	bin, log, err := m.Build(ctx, true)
	res.BuildLog = log
	if err != nil {
		return res, err
	}
	summary := "Update"
	if opt.Pull {
		summary = "Update from " + m.cfg.SelfRepo
	}
	if strings.TrimSpace(dirty) != "" {
		m.git(ctx, "add", "-A")
		if out, err := m.git(ctx, "commit", "-m", "⬆️ Manual update", "-m", "Requested by "+opt.Actor); err != nil && !strings.Contains(out, "nothing to commit") {
			os.Remove(bin)
			return res, fmt.Errorf("git commit: %s", out)
		}
		summary = "Manual update"
	}
	res.Commit, _ = m.git(ctx, "rev-parse", "--short", "HEAD")
	if m.cfg.SelfRepo != "" {
		// Push local commits (manual edits, rebased self-upgrades) so the
		// repository stays the source of truth. Credentials come from the
		// git credential store written at boot.
		if ahead, _ := m.git(ctx, "rev-list", "--count", "origin/"+m.cfg.SelfRepoBranch+"..HEAD"); strings.TrimSpace(ahead) != "0" && strings.TrimSpace(ahead) != "" {
			if out, err := m.git(ctx, "push", "origin", "HEAD:"+m.cfg.SelfRepoBranch); err == nil {
				res.Pushed = true
			} else {
				logf("⚠️  Push failed (the update still applies): %s", truncate(out, 300))
			}
		}
	}
	if err := os.Rename(bin, m.p.next); err != nil {
		return res, err
	}
	head, _ := m.git(ctx, "rev-parse", "HEAD")
	os.WriteFile(m.builtSHAPath(), []byte(head), 0o644)
	res.EvolutionID = store.NewID("evo")
	m.st.DB.Exec(`INSERT INTO evolutions(id, kind, summary, details, commit_sha, status, created_at) VALUES(?,?,?,?,?,?,?)`,
		res.EvolutionID, "update", summary+" by "+opt.Actor, truncate(log, 20000), res.Commit, "staged", store.Now())
	b, _ := json.Marshal(pending{EvolutionID: res.EvolutionID, Summary: summary + " by " + opt.Actor, Commit: res.Commit})
	os.WriteFile(filepath.Join(m.p.dir, "pending.json"), b, 0o644)
	m.st.Audit(opt.Actor, "self.update", res.Commit, map[string]any{"pull": opt.Pull})
	m.bus.Publish("evolution", map[string]string{"id": res.EvolutionID, "status": "staged", "summary": summary})
	logf("✅ Built %s, restarting on the new binary…", res.Commit)
	if opt.Restart != nil {
		opt.Restart("update " + res.Commit)
	} else {
		m.RequestRestart("update " + res.Commit)
	}
	return res, nil
}

// restartFile lets a separate process (the `update` CLI, run with docker
// exec) ask the running agent to restart.
func (m *Manager) restartFile() string { return filepath.Join(m.p.dir, "restart.request") }

func (m *Manager) RequestRestartFromOutside(reason string) error {
	return os.WriteFile(m.restartFile(), []byte(reason), 0o644)
}

// WatchRestartRequests turns restart.request files into restarts.
func (m *Manager) WatchRestartRequests(ctx context.Context) {
	os.Remove(m.restartFile()) // stale request from a previous life
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if b, err := os.ReadFile(m.restartFile()); err == nil {
				os.Remove(m.restartFile())
				m.RequestRestart(strings.TrimSpace(string(b)))
			}
		}
	}
}

// Rollback restores the previous binary and reverts the last commit.
func (m *Manager) Rollback(ctx context.Context, rc *core.RunContext, reason string) (string, error) {
	if !exists(m.p.prev) {
		return "", errors.New("no previous binary to restore")
	}
	in, err := os.Open(m.p.prev)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(m.p.next, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	out.Close()
	msg, _ := m.git(ctx, "revert", "--no-edit", "HEAD")
	if m.cfg.SelfRepo != "" && m.cfg.GitHubToken != "" {
		m.git(ctx, "push", "origin", "HEAD:"+m.cfg.SelfRepoBranch)
	}
	id := store.NewID("evo")
	m.st.DB.Exec(`INSERT INTO evolutions(id, kind, summary, details, status, run_id, created_at) VALUES(?,?,?,?,?,?,?)`,
		id, "rollback", "Rollback: "+reason, msg, "staged", rc.RunID, store.Now())
	m.st.Audit(rc.Actor(), "self.rollback", "", map[string]string{"reason": reason})
	m.RequestRestart("rollback")
	return msg, nil
}

// AfterBoot reconciles staged upgrades with what actually happened and
// returns announcements for the home channel.
func (m *Manager) AfterBoot(announce func(channel, thread, text string)) {
	if b, err := os.ReadFile(m.p.rollback); err == nil {
		var info RollbackInfo
		json.Unmarshal(b, &info)
		os.Remove(m.p.rollback)
		var evo, channel, thread string
		if pb, err := os.ReadFile(filepath.Join(m.p.dir, "pending.json")); err == nil {
			var p pending
			json.Unmarshal(pb, &p)
			evo, channel, thread = p.EvolutionID, p.Channel, p.ThreadTS
			os.Remove(filepath.Join(m.p.dir, "pending.json"))
		}
		if evo != "" {
			m.st.DB.Exec(`UPDATE evolutions SET status = 'rolled_back', details = COALESCE(details,'') || ? WHERE id = ?`, "\n\n--- crash ---\n"+info.Log, evo)
		}
		lesson := fmt.Sprintf("A self-upgrade was rolled back automatically (%s). Log excerpt:\n%s", info.Reason, truncate(lastLines(info.Log, 25), 2500))
		m.st.DB.Exec(`INSERT INTO memories(kind, subject, content, importance, source, created_at, updated_at) VALUES('lesson','self-upgrade rolled back',?,4,'supervisor',?,?)`, lesson, store.Now(), store.Now())
		announce(channel, thread, "🩹 My latest self-improvement crashed on startup, so I rolled back to the previous version.\n> "+info.Reason+"\nI kept the log in memory so I can fix it. 🧠")
		return
	}
	pb, err := os.ReadFile(filepath.Join(m.p.dir, "pending.json"))
	if err != nil {
		return
	}
	var p pending
	json.Unmarshal(pb, &p)
	go func() {
		// Survive the probation window before declaring victory.
		time.Sleep(probation + 5*time.Second)
		os.Remove(filepath.Join(m.p.dir, "pending.json"))
		m.st.DB.Exec(`UPDATE evolutions SET status = 'live' WHERE id = ?`, p.EvolutionID)
		m.bus.Publish("evolution", map[string]string{"id": p.EvolutionID, "status": "live"})
		announce(p.Channel, p.ThreadTS, fmt.Sprintf("🧬 Update applied and stable (`%s`): %s ✨", p.Commit, p.Summary))
	}()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
