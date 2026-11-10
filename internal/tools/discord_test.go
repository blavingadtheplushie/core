package tools

import (
	"strings"
	"testing"
)

func TestSplitDiscord(t *testing.T) {
	if p := SplitDiscord("hello", 0); len(p) != 1 || p[0] != "hello" {
		t.Fatalf("short: %v", p)
	}
	long := strings.Repeat("ligne de texte\n", 300)
	parts := SplitDiscord(long, 0)
	if len(parts) < 2 {
		t.Fatalf("expected split, got %d", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 1990 {
			t.Fatalf("part too long: %d", len([]rune(p)))
		}
	}
	if cleanSnowflake("<@!123456789012345678>") != "123456789012345678" {
		t.Fatal("cleanSnowflake")
	}
}
