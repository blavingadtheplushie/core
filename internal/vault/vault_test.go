package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBW emulates the few `bw` commands the client uses.
const fakeBW = `#!/bin/sh
case "$1" in
  status) if [ -f "$BITWARDENCLI_APPDATA_DIR/in" ]; then echo '{"serverUrl":"https://vault.test","status":"locked"}'; else echo '{"serverUrl":null,"status":"unauthenticated"}'; fi ;;
  config) exit 0 ;;
  login) [ "$BW_CLIENTSECRET" = "good" ] || { echo "Invalid client" >&2; exit 1; }; touch "$BITWARDENCLI_APPDATA_DIR/in" ;;
  unlock) [ "$BW_PASSWORD" = "master" ] || { echo "Invalid master password." >&2; exit 1; }; echo "SESSIONKEY" ;;
  sync) exit 0 ;;
  list)
    [ "$BW_SESSION" = "SESSIONKEY" ] || { echo "Vault is locked." >&2; exit 1; }
    if [ "$2" = folders ]; then echo '[{"id":"","name":"No Folder"},{"id":"f1","name":"BLAVINGAD"}]'; exit 0; fi
    cat <<'JSON'
[{"id":"i1","type":1,"name":"CLOUDFLARE_API_TOKEN","folderId":"f1","login":{"password":"cf-token-0123456789"},"fields":[]},
 {"id":"i2","type":1,"name":"Cloudflare","folderId":"f1","login":{"username":"bot@x.y","password":"cf-pass-abcdefgh","totp":"JBSWY3DPEHPK3PXP","uris":[{"uri":"https://dash.cloudflare.com"}]},"fields":[{"name":"account_id","value":"acc-42","type":0},{"name":"api_token","value":"cf-field-token-999999","type":1}]},
 {"id":"i3","type":2,"name":"OVH prod","folderId":null,"notes":"ovh-secret-note-123","fields":[]},
 {"id":"i4","type":1,"name":"Dup","folderId":null,"login":{"password":"dup-one-xxxxxxxx"},"fields":[]},
 {"id":"i5","type":1,"name":"Dup","folderId":null,"login":{"password":"dup-two-xxxxxxxx"},"fields":[]},
 {"id":"i6","type":1,"name":"GITHUB_TOKEN","folderId":null,"login":{"password":"not-in-folder-zzzz"},"fields":[]}]
JSON
    ;;
  get) [ "$2" = totp ] && echo 123456 ;;
  *) echo "unknown $1" >&2; exit 1 ;;
esac
`

func client(t *testing.T, secret string) *Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bw")
	if err := os.WriteFile(bin, []byte(fakeBW), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(Config{URL: "https://vault.test", ClientID: "user.x", ClientSecret: secret, Password: "master", Folder: "BLAVINGAD", DataDir: filepath.Join(dir, "data"), Binary: bin})
}

func TestOpenAndResolve(t *testing.T) {
	c := client(t, "good")
	ctx := context.Background()
	if err := c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"vault://CLOUDFLARE_API_TOKEN":  "cf-token-0123456789",
		"vault://Cloudflare/api_token":  "cf-field-token-999999",
		"vault://Cloudflare/account_id": "acc-42",
		"vault://cloudflare/username":   "bot@x.y",
		"vault://Cloudflare/uri":        "https://dash.cloudflare.com",
		"vault://Cloudflare/totp":       "123456",
		"vault://OVH prod":              "ovh-secret-note-123",
		"vault://i4":                    "dup-one-xxxxxxxx",
	}
	for ref, want := range cases {
		got, err := c.Resolve(ctx, ref)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v (want %q)", ref, got, err, want)
		}
	}
	for _, bad := range []string{"vault://Dup", "vault://Nope", "vault://Cloudflare/nope", "Cloudflare"} {
		if _, err := c.Resolve(ctx, bad); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
}

func TestResolveEnvConvention(t *testing.T) {
	c := client(t, "good")
	ctx := context.Background()
	if err := c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	raw := map[string]string{
		"CLOUDFLARE_API_TOKEN":  "",                              // convention: item in the folder
		"CLOUDFLARE_ACCOUNT_ID": "vault://Cloudflare/account_id", // explicit reference
		"GITHUB_TOKEN":          "",                              // homonym outside the folder: ignored
		"EXA_API_KEY":           "literal-value",                 // literal: untouched
		"LLM_API_KEY":           "vault://Missing",               // broken reference
	}
	vals, srcs, errs := c.ResolveEnv(ctx, raw)
	if vals["CLOUDFLARE_API_TOKEN"] != "cf-token-0123456789" || srcs["CLOUDFLARE_API_TOKEN"] != "vault://CLOUDFLARE_API_TOKEN" {
		t.Errorf("convention: %v %v", vals, srcs)
	}
	if vals["CLOUDFLARE_ACCOUNT_ID"] != "acc-42" {
		t.Errorf("explicit ref: %v", vals)
	}
	if _, ok := vals["GITHUB_TOKEN"]; ok {
		t.Error("an item outside the agent's folder must not be picked up by convention")
	}
	if _, ok := vals["EXA_API_KEY"]; ok {
		t.Error("literal values must be left alone")
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "LLM_API_KEY") {
		t.Errorf("errs = %v", errs)
	}
}

func TestSearchNeverLeaksValues(t *testing.T) {
	c := client(t, "good")
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := c.Search("", 0)
	if len(all) != 6 {
		t.Fatalf("got %d items", len(all))
	}
	dump := strings.ToLower(strings.Join(func() []string {
		var s []string
		for _, m := range all {
			s = append(s, m.Name, m.Username, m.URI, m.Reference, strings.Join(m.Fields, ","))
		}
		return s
	}(), " "))
	for _, secret := range []string{"cf-token-0123456789", "cf-pass-abcdefgh", "cf-field-token-999999", "ovh-secret-note-123", "jbswy3dpehpk3pxp"} {
		if strings.Contains(dump, strings.ToLower(secret)) {
			t.Errorf("search leaked %s", secret)
		}
	}
	if got := c.Search("cloudflare", 0); len(got) != 2 {
		t.Errorf("search cloudflare = %d", len(got))
	}
	secrets := strings.Join(c.Secrets(), " ")
	for _, want := range []string{"cf-token-0123456789", "cf-field-token-999999", "ovh-secret-note-123"} {
		if !strings.Contains(secrets, want) {
			t.Errorf("Secrets() misses %s (needed for masking)", want)
		}
	}
	if strings.Contains(secrets, "acc-42") {
		t.Error("non-hidden text fields are not secrets")
	}
}

func TestBadCredentials(t *testing.T) {
	c := client(t, "wrong")
	if err := c.Open(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid client") {
		t.Fatalf("expected a clean login error, got %v", err)
	}
	if c.Status().Error == "" || c.Status().Unlocked {
		t.Error("status must report the failure")
	}
	if _, err := c.Resolve(context.Background(), "vault://Cloudflare"); err == nil {
		t.Error("a locked vault must not resolve")
	}
}

func TestContainsFold(t *testing.T) {
	if !containsFold([]string{" API_Key "}, "api_key") {
		t.Fatal("expected case/space-insensitive match")
	}
	if containsFold([]string{"token"}, "api_key") {
		t.Fatal("unexpected match")
	}
}
