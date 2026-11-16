package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHackatimeHelpers(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755)
	f := filepath.Join(dir, "repo", "a", "b.go")
	if got := gitRoot(f); got != filepath.Join(dir, "repo") {
		t.Fatalf("gitRoot = %q", got)
	}
	if gitRoot(filepath.Join(dir, "x.go")) != "" && gitRoot(filepath.Join(dir, "x.go")) == dir {
		t.Fatal("no repo expected")
	}
	if hackatimeLanguage(f) != "Go" || hackatimeLanguage("/x/Dockerfile") != "Docker" {
		t.Fatal("language")
	}
	now := time.Now()
	if !hackatimeDue("/t/f", now) || hackatimeDue("/t/f", now.Add(time.Minute)) || !hackatimeDue("/t/f", now.Add(3*time.Minute)) {
		t.Fatal("debounce")
	}
}
