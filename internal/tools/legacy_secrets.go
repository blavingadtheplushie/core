package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/mcp"
	"blavingad/internal/vault"
)

// importLegacySecrets moves the old plaintext files of /data/secrets into
// the vault (once), seeds the MCP servers they belonged to, then renames
// each imported file to *.imported so nothing reads it anymore. It never
// overwrites an existing vault item and never logs values.
func (d *Deps) importLegacySecrets(ctx context.Context) {
	if !d.vaultReady() {
		return
	}
	dir := filepath.Join(d.Cfg.DataDir, "secrets")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if err := d.Vault.Open(ctx); err != nil {
		slog.Warn("legacy secrets: vault not open", "err", err)
		return
	}
	read := func(name string, v any) bool {
		b, err := os.ReadFile(filepath.Join(dir, name))
		return err == nil && json.Unmarshal(b, v) == nil
	}
	done := func(name string) { os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".imported")) }
	exists := func(item string) bool { _, err := d.Vault.Find("vault://" + item); return err == nil }
	store := vaultMCPStore{d.Vault}
	oauth := func(file string, s mcp.Server, conv func(m map[string]any) mcp.OAuthState) {
		var m map[string]any
		if !read(file, &m) {
			return
		}
		if !exists(s.OAuthItem()) {
			if err := store.SaveOAuth(ctx, s.OAuthItem(), conv(m)); err != nil {
				slog.Warn("legacy secrets: oauth import failed", "server", s.Name, "err", err)
				return
			}
		}
		if _, ok := d.mcpServer(s.Name); !ok {
			d.SaveMCPServer(s)
		}
		done(file)
		os.Remove(filepath.Join(dir, file+".lock"))
		slog.Info("legacy secrets: imported", "server", s.Name)
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	num := func(m map[string]any, k string) int64 { v, _ := m[k].(float64); return int64(v) }
	sub := func(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
	exp := func(m map[string]any) int64 {
		if num(m, "expires_in") == 0 {
			return 0
		}
		return num(m, "obtained_at") + num(m, "expires_in")
	}

	attend := mcp.Server{Name: "attend", URL: "https://attend.hackclub.com/mcp", Auth: "oauth", Enabled: true,
		Description: "Hack Club Attend as Paul (read-only: events, participants, groups, tickets, messages). Haven Paris event_id deec1354-3b54-4739-8080-94992f9f9da5. Minors' personal data: only Paul / Haven organisers, never in public channels."}
	oauth("attend_oauth.json", attend, func(m map[string]any) mcp.OAuthState {
		return mcp.OAuthState{ClientID: str(m, "client_id"), TokenEndpoint: attend.URL + "/oauth/token", AccessToken: str(m, "access_token"), RefreshToken: str(m, "refresh_token"), ExpiresAt: exp(m)}
	})
	hcb := mcp.Server{Name: "hcb", URL: "https://hcb-mcp.k.hackclub.dev/mcp", Auth: "oauth", Enabled: true,
		Description: "HCB (Hack Club Bank) read-only API as Paul. Financial data: only Paul / org members, never in public channels."}
	oauth("hcb_tokens.json", hcb, func(m map[string]any) mcp.OAuthState {
		te := str(m, "token_endpoint")
		if te == "" {
			te = "https://hcb-mcp.k.hackclub.dev/oauth/token"
		}
		return mcp.OAuthState{ClientID: str(m, "client_id"), TokenEndpoint: te, AccessToken: str(m, "access_token"), RefreshToken: str(m, "refresh_token"), ExpiresAt: exp(m)}
	})
	figma := mcp.Server{Name: "figma", URL: "https://mcp.figma.com/mcp", Auth: "oauth", Enabled: true,
		Description: "Figma's official MCP as Paul (48 tools: get_metadata, get_screenshot, get_design_context, use_figma and write tools…). Haven Paris fileKey Y5oURz8xoMOnMINJrqgz3Z. Paul's designs: only for Paul."}
	oauth("figma_mcp.json", figma, func(m map[string]any) mcp.OAuthState {
		cl, t := sub(m, "client"), sub(m, "tokens")
		return mcp.OAuthState{ClientID: str(cl, "client_id"), ClientSecret: str(cl, "client_secret"), TokenEndpoint: str(sub(m, "meta"), "token_endpoint"),
			AuthEndpoint: str(sub(m, "meta"), "authorization_endpoint"), RedirectURI: mcp.DefaultRedirect, Scope: "mcp:connect",
			AccessToken: str(t, "access_token"), RefreshToken: str(t, "refresh_token"), ExpiresAt: exp(t)}
	})
	if _, ok := d.mcpServer("lastscanned"); !ok {
		d.SaveMCPServer(mcp.Server{Name: "lastscanned", URL: "https://mcp.lastscanned.com", Auth: "bearer", TokenRef: "vault://LASTSCANNED_API_KEY", Enabled: true,
			Description: "Parcel tracking, 543 carriers (track_package, track_packages, list_carriers). Free plan: 5 numbers/day, don't re-poll."})
	}
	if _, ok := d.mcpServer("tricoteuses"); !ok {
		d.SaveMCPServer(mcp.Server{Name: "tricoteuses", URL: "https://parlement.tricoteuses.fr/mcp", Auth: "none", Enabled: true,
			Description: "French Parliament data (AN + Sénat: députés, dossiers, amendements, scrutins, questions, débats, PLF). See skill mcp-tricoteuses-parlement."})
	}

	// Plain files whose value already lives in the vault (checked identical by hand on 2026-11-23).
	for _, f := range []string{"idfm.json", "lastscanned.json", "media.json", "google_sa.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			done(f)
		}
	}
	// Files not yet in the vault: stored as secure notes, whole content in field "content".
	for file, item := range map[string]string{"orchard.json": "ORCHARD_BLAVINGAD_TOKEN", "orchard-anubis-graft.pem": "ORCHARD_ANUBIS_GRAFT_PEM", "orchard-r2.env": "ORCHARD_R2_ENV"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			continue
		}
		if !exists(item) {
			in := vault.SaveInput{Name: item, Kind: "note", Fields: map[string]string{"content": string(b)}, Notes: "Imported from /data/secrets/" + file + " on " + time.Now().Format("2006-01-02") + "."}
			if file == "orchard.json" {
				var o map[string]any
				json.Unmarshal(b, &o)
				in = vault.SaveInput{Name: item, Password: str(o, "token"), URI: str(o, "url"), Username: "me@blavingad.ink", Notes: "Blavingad's own Orchard token (org " + str(o, "org") + "), imported from /data/secrets/orchard.json."}
			}
			core.Secrets.Add(string(b))
			if _, _, err := d.Vault.Save(ctx, in); err != nil {
				slog.Warn("legacy secrets: import failed", "file", file, "err", err)
				continue
			}
		}
		done(file)
	}
}

func startLegacyImport(d *Deps) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		d.importLegacySecrets(ctx)
	}()
}
