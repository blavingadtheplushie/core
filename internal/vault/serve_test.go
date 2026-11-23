package vault

import (
	"encoding/base64"
	"testing"
)

func TestServeRoute(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte(`{"name":"x"}`))
	cases := []struct {
		args   []string
		method string
		path   string
		ok     bool
	}{
		{[]string{"status"}, "GET", "/status", true},
		{[]string{"sync"}, "POST", "/sync", true},
		{[]string{"list", "items"}, "GET", "/list/object/items", true},
		{[]string{"get", "totp", "i1"}, "GET", "/object/totp/i1", true},
		{[]string{"delete", "item", "i1"}, "DELETE", "/object/item/i1", true},
		{[]string{"create", "item", b64}, "POST", "/object/item", true},
		{[]string{"create", "item", b64, "--organizationid", "o1"}, "POST", "/object/item?organizationId=o1", true},
		{[]string{"edit", "item", "i1", b64}, "PUT", "/object/item/i1", true},
		{[]string{"generate", "-ulns", "--length", "32"}, "GET", "/generate?uppercase=true&lowercase=true&number=true&special=true&length=32", true},
		{[]string{"unlock", "--raw"}, "", "", false},
		{[]string{"login", "--apikey"}, "", "", false},
		{[]string{"config", "server", "x"}, "", "", false},
		{[]string{"create", "item", "!!notb64"}, "", "", false},
	}
	for _, c := range cases {
		m, p, body, ok := serveRoute(c.args)
		if ok != c.ok || (ok && (m != c.method || p != c.path)) {
			t.Errorf("%v → %s %s %v, want %s %s %v", c.args, m, p, ok, c.method, c.path, c.ok)
		}
		if ok && (c.args[0] == "create" || c.args[0] == "edit") && string(body) != `{"name":"x"}` {
			t.Errorf("%v body %q", c.args, body)
		}
	}
}

func TestUnwrapServe(t *testing.T) {
	cases := map[string]string{
		`{"object":"list","data":[{"id":"a"}]}`:                  `[{"id":"a"}]`,
		`{"object":"list","data":null}`:                          `[]`,
		`{"object":"string","data":"123456"}`:                    `123456`,
		`{"object":"template","template":{"status":"unlocked"}}`: `{"status":"unlocked"}`,
		`{"object":"item","id":"i1","name":"n"}`:                 `{"object":"item","id":"i1","name":"n"}`,
	}
	for in, want := range cases {
		if got := unwrapServe([]byte(in)); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}
