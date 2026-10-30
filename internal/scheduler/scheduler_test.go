package scheduler

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	from := time.Date(2026, 10, 29, 10, 0, 0, 0, loc) // a Thursday
	next, err := Next("0 9 * * 1-5", "", from, loc)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 30, 9, 0, 0, 0, loc); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
	at, err := Next("", "2026-11-02T14:30", from, loc)
	if err != nil || at.Hour() != 14 || at.Location() != loc {
		t.Errorf("one-shot parse: %v %v", at, err)
	}
	if _, err := Next("not a cron", "", from, loc); err == nil {
		t.Error("invalid cron accepted")
	}
}
