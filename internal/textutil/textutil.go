// Package textutil holds the tiny string helpers every package used to
// copy-paste (first non-empty value, rune-safe truncation).
package textutil

import "strings"

// FirstNonEmpty returns the first value that is not blank.
func FirstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Trunc cuts s to n runes, adding "…" when something was cut. It never
// splits a UTF-8 character.
func Trunc(s string, n int) string {
	if len(s) <= n { // fast path: bytes ≥ runes
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Short is Trunc on a single trimmed line (newlines become spaces), for
// labels and one-line previews.
func Short(s string, n int) string {
	return Trunc(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")), n)
}

// Clip is Trunc on trimmed text (newlines kept).
func Clip(s string, n int) string {
	return Trunc(strings.TrimSpace(s), n)
}
