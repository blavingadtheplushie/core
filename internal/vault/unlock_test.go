package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUnlockBW: the local CLI data is "stale" (a cached login whose keys the
// password no longer decrypts) until a fresh login; GOOD is the password
// that works once logged in again.
const fakeUnlockBW = `#!/bin/sh
d="$BITWARDENCLI_APPDATA_DIR"
case "$1" in
  status)
    if [ -f "$d/in" ] || [ -f "$d/stale" ]; then echo '{"serverUrl":"https://vault.test","status":"locked","userEmail":"bot@acme.test"}'
    else echo '{"serverUrl":null,"status":"unauthenticated"}'; fi ;;
  config) exit 0 ;;
  logout) rm -f "$d/in" "$d/stale" ;;
  login) touch "$d/in" ;;
  unlock)
    if [ -f "$d/stale" ] || [ "$BW_PASSWORD" != "$GOOD" ]; then
      echo "ERROR bitwarden_crypto::keys::master_key: error=The decryption operation failed" >&2; exit 1
    fi
    echo "SESSION" ;;
  list)
    [ "$BW_SESSION" = "SESSION" ] || { echo "Vault is locked." >&2; exit 1; }
    case "$2" in folders) echo '[{"id":"f1","name":"BLAVINGAD"}]' ;; items) echo '[]' ;; *) echo '[]' ;; esac ;;
  create)
    [ "$BW_SESSION" = "SESSION" ] || { echo "Vault is locked." >&2; exit 1; }
    echo '{"id":"n1","name":"X"}' ;;
esac
`

func unlockClient(t *testing.T, password, good string, stale bool) *Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bw")
	os.WriteFile(bin, []byte("#!/bin/sh\nGOOD='"+good+"'\n"+strings.TrimPrefix(fakeUnlockBW, "#!/bin/sh\n")), 0o755)
	data := filepath.Join(dir, "cli")
	os.MkdirAll(data, 0o700)
	if stale {
		os.WriteFile(filepath.Join(data, "stale"), nil, 0o600)
	}
	return New(Config{URL: "https://vault.test", ClientID: "user.x", ClientSecret: "s", Password: password, Folder: "BLAVINGAD", DataDir: data, Binary: bin})
}

func TestUnlockRecoversFromStaleLogin(t *testing.T) {
	c := unlockClient(t, "master", "master", true)
	if err := c.Open(context.Background()); err != nil {
		t.Fatalf("a stale cached login must be replaced by a fresh one: %v", err)
	}
}

func TestUnlockStripsEnvFileQuotes(t *testing.T) {
	c := unlockClient(t, `"master"`, "master", false)
	if err := c.Open(context.Background()); err != nil {
		t.Fatalf("quotes kept by --env-file must be tolerated: %v", err)
	}
}

func TestWrongPasswordIsExplainedAndWritesFailCleanly(t *testing.T) {
	c := unlockClient(t, "pa$$word", "other", false)
	err := c.Open(context.Background())
	if err == nil {
		t.Fatal("expected an unlock error")
	}
	msg := err.Error()
	for _, want := range []string{"does not decrypt the vault of bot@acme.test", "8 characters", `write "$$"`, "same account"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error misses %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "pa$$word") {
		t.Fatal("the error must never contain the password")
	}
	// A write after a failed boot reports the real cause, not "Vault is locked".
	_, _, err = c.Save(context.Background(), SaveInput{Name: "X", Password: "v"})
	if err == nil || strings.Contains(err.Error(), "Vault is locked") || !strings.Contains(err.Error(), "does not decrypt") {
		t.Errorf("save error = %v", err)
	}
}

func TestWriteReopensAfterBootFailure(t *testing.T) {
	c := unlockClient(t, "master", "master", false)
	c.cfg.Password = "wrong"
	if c.Open(context.Background()) == nil {
		t.Fatal("expected failure")
	}
	c.cfg.Password = "master" // e.g. the server came back / was fixed
	if _, _, err := c.Save(context.Background(), SaveInput{Name: "X", Password: "v"}); err != nil {
		t.Fatalf("save must reopen the vault: %v", err)
	}
}
