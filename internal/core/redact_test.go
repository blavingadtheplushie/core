package core

import "testing"

func TestRedactor(t *testing.T) {
	r := &Redactor{values: map[string]bool{}}
	r.Add("cf-token-SUPERSECRET-123456", "SUPERSECRET", "short", "")
	got := r.Redact("export T=cf-token-SUPERSECRET-123456; echo SUPERSECRET short")
	want := "export T=" + Mask + "; echo " + Mask + " short"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if r.Count() != 2 {
		t.Errorf("count = %d", r.Count())
	}
	if (&Redactor{values: map[string]bool{}}).Redact("rien") != "rien" {
		t.Error("empty redactor must be a no-op")
	}
}
