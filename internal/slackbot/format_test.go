package slackbot

import (
	"strings"
	"testing"
)

func TestToMrkdwn(t *testing.T) {
	cases := map[string]string{
		"**gras** et [lien](https://x.dev)":  "*gras* et <https://x.dev|lien>",
		"## Titre":                           "*Titre*",
		"- un\n- deux":                       "• un\n• deux",
		"`**untouched**`":                    "`**untouched**`",
		"```go\nfmt.Println(\"**x**\")\n```": "```\nfmt.Println(\"**x**\")\n```",
		"~~struck~~":                         "~struck~",
	}
	for in, want := range cases {
		if got := ToMrkdwn(in); got != want {
			t.Errorf("ToMrkdwn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChunkKeepsCodeBlocksClosed(t *testing.T) {
	text := "intro\n```\n" + strings.Repeat("ligne de code\n", 400) + "```\nfin"
	chunks := Chunk(text, 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 1010 {
			t.Errorf("chunk %d too long: %d", i, len(c))
		}
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk %d has an unbalanced code fence", i)
		}
	}
}
