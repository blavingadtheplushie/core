package core

import (
	"testing"

	"blavingad/internal/store"
)

func tool(name, cat string) *Tool { return &Tool{Name: name, Category: cat} }

func TestProfileAllows(t *testing.T) {
	p := &Profile{Tools: []string{"*", "-cat:self", "-shell"}, Memory: "full"}
	cases := map[*Tool]bool{
		tool("web_search", "web"): true, tool("self_upgrade", "self"): false,
		tool("shell", "code"): false, tool("fs_read", "code"): true, tool("memory_save", "memory"): true,
	}
	for tl, want := range cases {
		if got := p.Allows(tl); got != want {
			t.Errorf("%s: got %v want %v", tl.Name, got, want)
		}
	}
	guest := &Profile{Tools: []string{"cat:web", "cat:memory", "slack_react"}, Memory: "read"}
	if guest.Allows(tool("memory_save", "memory")) || !guest.Allows(tool("memory_search", "memory")) {
		t.Error("read memory must allow search but not save")
	}
	guest.Memory = "none"
	if guest.Allows(tool("memory_search", "memory")) || guest.Allows(tool("slack_search", "slack")) {
		t.Error("no memory must hide memory and Slack history search")
	}
	if !guest.Allows(tool("slack_react", "slack")) || guest.Allows(tool("slack_post", "slack")) {
		t.Error("single tool tokens")
	}
	if (&Profile{Tools: []string{}}).Allows(tool("web_search", "web")) {
		t.Error("empty token list must allow nothing")
	}
}

func TestCanApprove(t *testing.T) {
	if !(&Profile{Approve: "any"}).CanApprove("U1", "U2") {
		t.Error("any")
	}
	own := &Profile{Approve: "own"}
	if !own.CanApprove("U1", "U1") || own.CanApprove("U1", "U2") || own.CanApprove("U1", "") {
		t.Error("own")
	}
	if (&Profile{Approve: "none"}).CanApprove("U1", "U1") {
		t.Error("none")
	}
}

func TestAccessResolve(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewAccess(st, []string{"UADMIN1"})
	if p, listed := a.Resolve("UADMIN1"); p.Name != "admin" || !listed {
		t.Errorf("env admin resolved to %s", p.Name)
	}
	if p, listed := a.Resolve("USTRANGER"); p.Name != "guest" || listed || p.Memory != "none" {
		t.Errorf("unlisted resolved to %+v", p)
	}
	if err := a.SetMember("<@UALICE1>", "member", "dev", "test"); err != nil {
		t.Fatal(err)
	}
	if p, listed := a.Resolve("UALICE1"); p.Name != "member" || !listed {
		t.Errorf("member resolved to %s", p.Name)
	}
	if a.SetMember("UADMIN1", "guest", "", "") == nil {
		t.Error("env admins must not be downgraded")
	}
	if a.SetMember("not-an-id", "member", "", "") == nil {
		t.Error("invalid id accepted")
	}

	// The admin profile can never be weakened.
	a.SaveProfile(Profile{Name: "admin", Label: "Chef", Tools: []string{}, Memory: "none", Approve: "none"})
	if p := a.Full(); p.Label != "Chef" || p.Approve != "any" || p.Memory != "full" || len(p.Tools) != 1 || p.Tools[0] != "*" {
		t.Errorf("admin weakened: %+v", p)
	}
	if a.DeleteProfile("admin") == nil || a.DeleteProfile("guest") == nil {
		t.Error("admin and the default profile must not be deletable")
	}

	// Blocked default: nobody outside the list gets an answer.
	if err := a.SetDefault("blocked"); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.Resolve("USTRANGER"); p.Reply {
		t.Error("blocked default must not reply")
	}
	// Deleting a member's profile sends them back to the default.
	a.SaveProfile(Profile{Name: "client", Tools: []string{"cat:web"}, Memory: "none", Approve: "none", Reply: true})
	a.SetMember("UBOB1", "client", "", "")
	if err := a.DeleteProfile("client"); err != nil {
		t.Fatal(err)
	}
	if p, listed := a.Resolve("UBOB1"); listed || p.Name != "blocked" {
		t.Errorf("orphan member resolved to %s (listed=%v)", p.Name, listed)
	}
	// A broken default fails closed.
	st.SetSetting("access.default_profile", "nexiste-pas")
	if p, _ := a.Resolve("USTRANGER"); p.IsAdmin() || p.Memory != "none" {
		t.Errorf("broken config must fail closed, got %+v", p)
	}
}
