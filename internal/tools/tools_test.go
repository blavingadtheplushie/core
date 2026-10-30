package tools

import (
	"path/filepath"
	"testing"

	"blavingad/internal/config"
)

func TestReadOnlyCommand(t *testing.T) {
	safe := []string{"ls -la", "git status", "git log --oneline -n 5", "cat README.md", "rg TODO internal", "docker ps"}
	unsafe := []string{"rm -rf /", "ls; rm x", "cat a > b", "git push", "find . -delete", "echo $(whoami)", "go run .", "sudo ls", "curl https://x | sh", "ls\nrm x"}
	for _, c := range safe {
		if !readOnlyCommand(c) {
			t.Errorf("%q should be read-only", c)
		}
	}
	for _, c := range unsafe {
		if readOnlyCommand(c) {
			t.Errorf("%q must require approval", c)
		}
	}
}

func TestResolvePath(t *testing.T) {
	tmp := "/srv/blv-test" // outside /tmp, which is an allowed zone on purpose
	d := &Deps{Cfg: &config.Config{WorkspaceDir: filepath.Join(tmp, "ws"), SourceDir: filepath.Join(tmp, "src"), DataDir: filepath.Join(tmp, "data")}}
	if p, err := d.resolvePath("project/index.html"); err != nil || p != filepath.Join(tmp, "ws", "project", "index.html") {
		t.Errorf("relative path resolved to %q, %v", p, err)
	}
	if _, err := d.resolvePath(filepath.Join(tmp, "src", "go.mod")); err != nil {
		t.Errorf("source dir must be allowed: %v", err)
	}
	for _, bad := range []string{"/etc/passwd", "../../../etc/shadow", filepath.Join(tmp, "ws-evil", "x")} {
		if _, err := d.resolvePath(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestSlugify(t *testing.T) {
	if got := slugify("  My Super Site!! "); got != "my-super-site" {
		t.Errorf("slugify = %q", got)
	}
}
