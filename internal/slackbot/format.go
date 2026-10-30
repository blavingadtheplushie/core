package slackbot

import (
	"regexp"
	"strings"
)

var (
	reCodeBlock = regexp.MustCompile("(?s)```.*?```")
	reInline    = regexp.MustCompile("`[^`\n]+`")
	reBold      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBold2     = regexp.MustCompile(`__(.+?)__`)
	reStrike    = regexp.MustCompile(`~~(.+?)~~`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	reHeading   = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*$`)
	reBullet    = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	reHr        = regexp.MustCompile(`(?m)^\s*(-{3,}|\*{3,})\s*$`)
)

// ToMrkdwn converts the GitHub-flavoured markdown models love into Slack
// mrkdwn, leaving code untouched.
func ToMrkdwn(s string) string {
	var stash []string
	protect := func(m string) string {
		stash = append(stash, m)
		return "\x00" + string(rune('A'+len(stash)-1)) + "\x00"
	}
	s = reCodeBlock.ReplaceAllStringFunc(s, func(m string) string {
		// Slack ignores the language hint and prints it; drop it.
		if nl := strings.Index(m, "\n"); nl > 3 && !strings.Contains(m[3:nl], " ") {
			m = "```" + m[nl:]
		}
		return protect(m)
	})
	s = reInline.ReplaceAllStringFunc(s, protect)
	s = reHeading.ReplaceAllString(s, "*$1*")
	s = reBold.ReplaceAllString(s, "*$1*")
	s = reBold2.ReplaceAllString(s, "*$1*")
	s = reStrike.ReplaceAllString(s, "~$1~")
	s = reLink.ReplaceAllString(s, "<$2|$1>")
	s = reHr.ReplaceAllString(s, "──────────")
	s = reBullet.ReplaceAllString(s, "$1• ")
	for i, v := range stash {
		s = strings.Replace(s, "\x00"+string(rune('A'+i))+"\x00", v, 1)
	}
	return s
}

// Chunk splits text on paragraph/line boundaries so no piece exceeds max
// (Slack section blocks cap at 3000 chars).
func Chunk(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "\n\n")
		if cut < max/2 {
			cut = strings.LastIndex(s[:max], "\n")
		}
		if cut < max/3 {
			cut = strings.LastIndex(s[:max], " ")
		}
		if cut <= 0 {
			cut = max
		}
		// Never split inside a code block: close and reopen it.
		part := s[:cut]
		rest := strings.TrimLeft(s[cut:], "\n")
		if strings.Count(part, "```")%2 == 1 {
			part += "\n```"
			rest = "```\n" + rest
		}
		out = append(out, part)
		s = rest
	}
	if strings.TrimSpace(s) != "" {
		out = append(out, s)
	}
	return out
}
