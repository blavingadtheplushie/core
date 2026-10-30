package mail

import "testing"

func TestIsAutomated(t *testing.T) {
	cases := []struct {
		as, prec, list, xar, from string
		want                      bool
	}{
		{"", "", "", "", "Bee <bee@skogsduva.club>", false},
		{"no", "", "", "", "bee@skogsduva.club", false},
		{"auto-replied", "", "", "", "bee@skogsduva.club", true},
		{"", "bulk", "", "", "news@x.com", true},
		{"", "", "<list.x.com>", "", "a@x.com", true},
		{"", "", "", "", "MAILER-DAEMON@x.com", true},
		{"", "", "", "", "noreply@github.com", true},
	}
	for _, c := range cases {
		if got := isAutomated(c.as, c.prec, c.list, c.xar, c.from); got != c.want {
			t.Errorf("isAutomated(%q,%q,%q,%q,%q) = %v, want %v", c.as, c.prec, c.list, c.xar, c.from, got, c.want)
		}
	}
}
