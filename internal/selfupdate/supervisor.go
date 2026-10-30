// Package selfupdate lets BLAVINGAD rewrite and redeploy itself.
//
// The container entrypoint is `blavingad supervise`. The supervisor runs the
// active binary as a child (`serve`). When the agent stages a new binary
// (blavingad.next) and exits with code 42, the supervisor promotes it. If a
// freshly promoted binary dies within the probation window, the previous
// binary is restored automatically and the crash log is handed to the next
// boot so the agent can learn from it.
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	ExitRestart = 42
	probation   = 90 * time.Second
)

type paths struct {
	dir, current, next, prev, rollback, stamp string
}

func binPaths(dataDir string) paths {
	dir := filepath.Join(dataDir, "bin")
	return paths{
		dir:      dir,
		current:  filepath.Join(dir, "blavingad"),
		next:     filepath.Join(dir, "blavingad.next"),
		prev:     filepath.Join(dir, "blavingad.prev"),
		rollback: filepath.Join(dir, "rollback.json"),
		stamp:    filepath.Join(dir, "image.stamp"),
	}
}

type RollbackInfo struct {
	At     string `json:"at"`
	Reason string `json:"reason"`
	Log    string `json:"log"`
}

// tail keeps the last N bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func imageStamp(base string) string {
	fi, err := os.Stat(base)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().Unix())
}

// Supervise is the PID-1-ish loop. It returns the exit code for the
// container.
func Supervise(dataDir string) int {
	p := binPaths(dataDir)
	os.MkdirAll(p.dir, 0o755)
	base, _ := os.Executable()
	base, _ = filepath.EvalSymlinks(base)

	// A rebuilt image is authoritative: drop the agent-built binary so the
	// human's new version runs. The agent's history stays in git.
	stamp := imageStamp(base)
	if old, _ := os.ReadFile(p.stamp); string(old) != stamp {
		if exists(p.current) {
			slog.Info("supervisor: new image detected, discarding self-built binary")
			os.Remove(p.current)
			os.Remove(p.prev)
		}
		os.WriteFile(p.stamp, []byte(stamp), 0o644)
	}

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	var stopping bool
	var mu sync.Mutex
	crashes := 0

	for {
		upgraded := false
		if exists(p.next) {
			if exists(p.current) {
				os.Rename(p.current, p.prev)
			}
			if err := os.Rename(p.next, p.current); err != nil {
				slog.Error("supervisor: promote failed", "err", err)
			} else {
				upgraded = true
				slog.Info("supervisor: promoted new self-built binary")
			}
		}
		bin := base
		if exists(p.current) {
			bin = p.current
		}

		logTail := &tail{max: 12 << 10}
		cmd := exec.Command(bin, "serve")
		cmd.Stdout = os.Stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, logTail)
		cmd.Env = os.Environ()
		start := time.Now()
		if err := cmd.Start(); err != nil {
			slog.Error("supervisor: start failed", "bin", bin, "err", err)
			if bin != base {
				rollback(p, "the binary failed to start: "+err.Error(), "")
				continue
			}
			return 1
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		var err error
	wait:
		for {
			select {
			case sig := <-sigs:
				mu.Lock()
				stopping = true
				mu.Unlock()
				cmd.Process.Signal(sig)
			case err = <-done:
				break wait
			}
		}
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			code = 1
		}
		mu.Lock()
		stop := stopping
		mu.Unlock()
		if stop {
			return 0
		}
		switch {
		case code == ExitRestart:
			slog.Info("supervisor: restart requested")
			crashes = 0
			continue
		case code == 0:
			return 0
		case upgraded && time.Since(start) < probation:
			slog.Error("supervisor: new binary crashed during probation, rolling back", "code", code)
			rollback(p, fmt.Sprintf("the new binary crashed after %s (code %d)", time.Since(start).Round(time.Second), code), logTail.String())
			continue
		default:
			// A child that ran well past probation was healthy; this crash
			// starts a new streak.
			if time.Since(start) > probation {
				crashes = 0
			}
			crashes++
			slog.Error("supervisor: child crashed", "code", code, "crashes", crashes)
			if crashes >= 3 && bin != base {
				rollback(p, "three crashes in a row", logTail.String())
				crashes = 0
			}
			time.Sleep(time.Duration(min(crashes*2, 30)) * time.Second)
		}
	}
}

func rollback(p paths, reason, log string) {
	os.Remove(p.current)
	if exists(p.prev) {
		os.Rename(p.prev, p.current)
	}
	info := RollbackInfo{At: time.Now().UTC().Format(time.RFC3339), Reason: reason, Log: log}
	b, _ := json.MarshalIndent(info, "", "  ")
	os.WriteFile(p.rollback, b, 0o644)
}
