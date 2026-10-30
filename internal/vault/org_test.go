package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOrgBW: the personal vault is forbidden by policy (when POLICY=1), the
// bot shares one editable collection "Infra" and a read-only one "Finance".
const fakeOrgBW = `#!/bin/sh
log="$BITWARDENCLI_APPDATA_DIR/../calls.log"
case "$1" in
  status) echo '{"serverUrl":"https://vault.test","status":"locked"}' ;;
  unlock) echo S ;;
  sync) exit 0 ;;
  list)
    case "$2" in
      folders) echo '[{"id":"f1","name":"BLAVINGAD"}]' ;;
      organizations) echo '[{"id":"o1","name":"ACME"}]' ;;
      collections) if [ -n "$TWO" ]; then echo '[{"id":"c1","name":"Infra","organizationId":"o1"},{"id":"c2","name":"Finance","organizationId":"o1"}]'; else echo '[{"id":"c1","name":"Infra","organizationId":"o1"}]'; fi ;;
      items) cat "$BITWARDENCLI_APPDATA_DIR/../items.json" 2>/dev/null || echo '[]' ;;
    esac ;;
  create)
    if [ "$2" = folder ]; then echo '{"id":"f1"}'; exit 0; fi
    json=$(echo "$3" | base64 -d)
    echo "create $json $4 $5" >> "$log"
    case "$json" in
      *'"collectionIds":["c2"]'*) echo "You do not have permission to edit this item." >&2; exit 1 ;;
      *organizationId*) echo '{"id":"new1","name":"X","organizationId":"o1","collectionIds":["c1"]}' ;;
      *) if [ -n "$POLICY" ]; then echo "Due to an Enterprise Policy, you are restricted from saving items to your individual vault." >&2; exit 1; fi
         echo '{"id":"new2","name":"X"}' ;;
    esac ;;
esac
`

func orgClient(t *testing.T, cfg func(*Config)) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bw")
	os.WriteFile(bin, []byte(fakeOrgBW), 0o755)
	c := Config{URL: "https://vault.test", ClientID: "x", ClientSecret: "y", Password: "p", Folder: "BLAVINGAD", DataDir: filepath.Join(dir, "cli"), Binary: bin}
	if cfg != nil {
		cfg(&c)
	}
	cl := New(c)
	if err := cl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	return cl, filepath.Join(dir, "calls.log")
}

func calls(t *testing.T, path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestPolicyFallsBackToOnlyCollection(t *testing.T) {
	cl, log := orgClient(t, nil)
	cl.cfg.Binary = wrapEnv(t, cl.cfg.Binary, "POLICY=1")
	item, updated, err := cl.Save(context.Background(), SaveInput{Name: "DB_PASSWORD", Password: "s3cr3t-value-123"})
	if err != nil || updated || item.OrgID != "o1" {
		t.Fatalf("save: %+v %v", item, err)
	}
	got := calls(t, log)
	if !strings.Contains(got, `"organizationId":"o1"`) || !strings.Contains(got, `"collectionIds":["c1"]`) || !strings.Contains(got, "--organizationid o1") {
		t.Errorf("expected a retry into collection Infra, calls:\n%s", got)
	}
}

func TestPolicyWithSeveralCollectionsExplains(t *testing.T) {
	cl, _ := orgClient(t, nil)
	cl.cfg.Binary = wrapEnv(t, cl.cfg.Binary, "POLICY=1", "TWO=1")
	cl.Refresh(context.Background())
	_, _, err := cl.Save(context.Background(), SaveInput{Name: "X", Password: "y-12345678"})
	if err == nil || !strings.Contains(err.Error(), "VAULT_COLLECTION") || !strings.Contains(err.Error(), `"Infra"`) || !strings.Contains(err.Error(), `"Finance"`) {
		t.Fatalf("error must explain the policy and list collections: %v", err)
	}
}

func TestConfiguredAndReadOnlyCollection(t *testing.T) {
	cl, log := orgClient(t, func(c *Config) { c.Collection = "infra" })
	cl.cfg.Binary = wrapEnv(t, cl.cfg.Binary, "TWO=1")
	cl.Refresh(context.Background())
	if st := cl.Status(); !strings.Contains(st.Writes, `collection "Infra"`) {
		t.Errorf("status.writes = %q", st.Writes)
	}
	if _, _, err := cl.Save(context.Background(), SaveInput{Name: "A", Kind: "note", Password: "note-secret-123"}); err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log); !strings.Contains(got, `"type":2`) || !strings.Contains(got, `"secret"`) {
		t.Errorf("secure note not created as expected:\n%s", got)
	}
	_, _, err := cl.Save(context.Background(), SaveInput{Name: "B", Password: "zzzzzzzz1", Collection: "Finance"})
	if err == nil || !strings.Contains(err.Error(), "Can edit") {
		t.Fatalf("read-only collection must explain the missing permission: %v", err)
	}
	if _, _, err := cl.Save(context.Background(), SaveInput{Name: "C", Password: "zzzzzzzz2", Collection: "Nope"}); err == nil || !strings.Contains(err.Error(), "available") {
		t.Errorf("unknown collection must list the available ones: %v", err)
	}
}

// wrapEnv returns a script that runs bin with extra environment variables
// (bw() deliberately gives the CLI a minimal environment).
func wrapEnv(t *testing.T, bin string, vars ...string) string {
	t.Helper()
	w := filepath.Join(t.TempDir(), "bw-wrap")
	os.WriteFile(w, []byte("#!/bin/sh\nexec env "+strings.Join(vars, " ")+" "+bin+" \"$@\"\n"), 0o755)
	return w
}
