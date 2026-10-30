package vault

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRealVault runs against a real Vaultwarden when VAULT_E2E_URL is set
// (plus VAULT_E2E_CLIENT_ID, VAULT_E2E_CLIENT_SECRET, VAULT_E2E_PASSWORD and
// optionally VAULT_E2E_CA / VAULT_E2E_BW). It writes to a throwaway folder.
func TestRealVault(t *testing.T) {
	url := os.Getenv("VAULT_E2E_URL")
	if url == "" {
		t.Skip("VAULT_E2E_URL not set")
	}
	bin := os.Getenv("VAULT_E2E_BW")
	if bin == "" {
		bin = "bw"
	}
	c := New(Config{URL: url, ClientID: os.Getenv("VAULT_E2E_CLIENT_ID"), ClientSecret: os.Getenv("VAULT_E2E_CLIENT_SECRET"),
		Password: os.Getenv("VAULT_E2E_PASSWORD"), Folder: "BLAVINGAD-E2E", CACert: os.Getenv("VAULT_E2E_CA"),
		DataDir: filepath.Join(t.TempDir(), "cli"), Binary: bin})
	ctx := context.Background()
	if err := c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	pw, err := c.Generate(ctx, 40)
	if err != nil || len(pw) != 40 {
		t.Fatalf("generate: %q %v", pw, err)
	}
	item, updated, err := c.Save(ctx, SaveInput{Name: "E2E_TOKEN", Username: "bot", Password: pw, URI: "https://example.com", Fields: map[string]string{"region": "eu-west"}})
	if err != nil || updated {
		t.Fatalf("create: %v updated=%v", err, updated)
	}
	if got, _ := c.Resolve(ctx, "vault://E2E_TOKEN"); got != pw {
		t.Errorf("created password not readable back")
	}
	if got, _ := c.Resolve(ctx, "vault://E2E_TOKEN/region"); got != "eu-west" {
		t.Errorf("custom field = %q", got)
	}
	_, updated, err = c.Save(ctx, SaveInput{Name: "E2E_TOKEN", Password: "rotated-e2e-value-123", Fields: map[string]string{"region": "us-east"}})
	if err != nil || !updated {
		t.Fatalf("update: %v updated=%v", err, updated)
	}
	if got, _ := c.Resolve(ctx, "vault://E2E_TOKEN"); got != "rotated-e2e-value-123" {
		t.Errorf("updated password = %q", got)
	}
	if got, _ := c.Resolve(ctx, "vault://E2E_TOKEN/username"); got != "bot" {
		t.Errorf("update must keep untouched fields, username = %q", got)
	}
	if got, _ := c.Resolve(ctx, "vault://E2E_TOKEN/region"); got != "us-east" {
		t.Errorf("updated field = %q", got)
	}
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	t.Logf("vault ok: %d items, item %s", c.Status().Items, item.ID)
}
