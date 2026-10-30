package slackbot

import (
	"testing"

	"blavingad/internal/store"
)

func TestBotStreakResetsOnHuman(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{st: st, BotUserID: "USELF"}
	add := func(ts, user string, isBot bool) {
		st.DB.Exec(`INSERT INTO slack_messages(channel, ts, thread_ts, user_id, user_name, text, is_bot, created_at) VALUES('C1',?,?,?,'','x',?,?)`, ts, "100.0", user, isBot, store.Now())
	}
	add("100.0", "UHUMAN", false)
	add("101.0", "UBOT", true)
	add("102.0", "USELF", true)
	add("103.0", "UBOT", true)
	if n := b.botStreak("C1", "100.0", "103.0"); n != 2 {
		t.Fatalf("streak = %d, want 2 (own replies excluded)", n)
	}
	add("104.0", "UHUMAN", false)
	add("105.0", "UBOT", true)
	if n := b.botStreak("C1", "100.0", "105.0"); n != 1 {
		t.Fatalf("streak after human = %d, want 1", n)
	}
	if n := b.botStreak("C1", "100.0", "101.0"); n != 1 {
		t.Fatalf("streak at 101 = %d, want 1", n)
	}
}
