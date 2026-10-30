package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/store"
)

func TestEnsureWritableRelocates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root, permission bits don't block writes; this test covers the non-root path")
	}
	data := t.TempDir()
	src := filepath.Join(data, "src")
	os.MkdirAll(filepath.Join(src, "internal", "locked"), 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module x\n"), 0o444) // read-only file
	os.WriteFile(filepath.Join(src, "internal", "locked", "a.go"), []byte("package locked\n"), 0o644)
	os.Chmod(filepath.Join(src, "internal", "locked"), 0o555) // read-only dir
	t.Cleanup(func() { os.Chmod(filepath.Join(src, "internal", "locked"), 0o755) })

	c := CheckWritable(src)
	if c.Writable || c.Failures < 2 || !strings.Contains(c.Cause, "mode") {
		t.Fatalf("check = %+v", c)
	}

	t.Setenv("DATA_DIR", data)
	t.Setenv("SOURCE_DIR", "")
	os.Unsetenv("SOURCE_DIR")
	cfg := config.Load()
	if cfg.SourceDir != src {
		t.Fatalf("source dir = %s", cfg.SourceDir)
	}
	st, _ := store.Open(t.TempDir())
	m := NewManager(cfg, st, core.NewBus(), "test")
	notice := m.EnsureWritable(context.Background())
	if !strings.Contains(notice, "now work from there") {
		t.Fatalf("notice = %q", notice)
	}
	if cfg.SourceDir == src || !CheckWritable(cfg.SourceDir).Writable {
		t.Fatalf("not relocated to a writable dir: %s", cfg.SourceDir)
	}
	if b, _ := os.ReadFile(filepath.Join(cfg.SourceDir, "internal", "locked", "a.go")); string(b) != "package locked\n" {
		t.Error("files were not copied")
	}
	// The next boot follows the redirect.
	if again := config.Load(); again.SourceDir != cfg.SourceDir {
		t.Errorf("config.Load ignores source.path: %s", again.SourceDir)
	}
	// And a healthy tree is left alone.
	if n := m.EnsureWritable(context.Background()); n != "" {
		t.Errorf("healthy tree produced a notice: %q", n)
	}
}

// A tree the process can't even read (e.g. labeled for another container
// by SELinux) is rebuilt from the image seed.
func TestEnsureWritableReseedsUnreadableTree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	data := t.TempDir()
	src := filepath.Join(data, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module old\n"), 0o644)
	seed := t.TempDir()
	os.WriteFile(filepath.Join(seed, "go.mod"), []byte("module seed\n"), 0o444)
	os.Chmod(src, 0o000)
	t.Cleanup(func() { os.Chmod(src, 0o755) })

	t.Setenv("DATA_DIR", data)
	t.Setenv("SOURCE_DIR", "")
	os.Unsetenv("SOURCE_DIR")
	t.Setenv("HOME", t.TempDir())
	cfg := config.Load()
	st, _ := store.Open(t.TempDir())
	m := NewManager(cfg, st, core.NewBus(), "test")
	if err := m.EnsureSource(seed); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("an unreadable tree must be reported, not overwritten: %v", err)
	}
	notice := m.EnsureWritable(context.Background())
	if !strings.Contains(notice, "rebuilt a fresh source tree") || cfg.SourceDir == src {
		t.Fatalf("notice = %q, dir = %s", notice, cfg.SourceDir)
	}
	if b, _ := os.ReadFile(filepath.Join(cfg.SourceDir, "go.mod")); string(b) != "module seed\n" {
		t.Errorf("not seeded: %q", b)
	}
	if !CheckWritable(cfg.SourceDir).Writable {
		t.Error("the seeded copy must be writable (attributes must not be preserved)")
	}
	if _, err := os.Stat(filepath.Join(cfg.SourceDir, ".git")); err != nil {
		t.Error("the fresh tree must be a git repo")
	}
}
