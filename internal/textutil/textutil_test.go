package textutil

import "testing"

func TestHelpers(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "a", "b"); got != "a" {
		t.Errorf("FirstNonEmpty = %q", got)
	}
	if got := Trunc("héhéhé", 3); got != "héh…" {
		t.Errorf("Trunc = %q", got)
	}
	if got := Trunc("héhé", 4); got != "héhé" {
		t.Errorf("Trunc no cut = %q", got)
	}
	if got := Short(" a\nb ", 10); got != "a b" {
		t.Errorf("Short = %q", got)
	}
	if got := Clip(" a\nb ", 10); got != "a\nb" {
		t.Errorf("Clip = %q", got)
	}
}
