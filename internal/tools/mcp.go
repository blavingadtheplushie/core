package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"blavingad/internal/core"
	"blavingad/internal/mcp"
	"blavingad/internal/textutil"
	"blavingad/internal/vault"
)

// MCP servers are configured in the dashboard (#/mcp) or with mcp action=add;
// the list lives in the setting "mcp.servers" (JSON, no secrets: tokens are
// vault references, OAuth state lives in vault items MCP_OAUTH_<NAME>).

const mcpSettingKey = "mcp.servers"

var mcpMu sync.Mutex

func (d *Deps) MCPServers() []mcp.Server {
	var out []mcp.Server
	json.Unmarshal([]byte(d.St.GetSetting(mcpSettingKey, "[]")), &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (d *Deps) mcpServer(name string) (mcp.Server, bool) {
	for _, s := range d.MCPServers() {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return mcp.Server{}, false
}

// SaveMCPServer adds or replaces a server (by name).
func (d *Deps) SaveMCPServer(s mcp.Server) error {
	s.Name = slugify(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	if s.Name == "" || !strings.HasPrefix(s.URL, "http") {
		return errors.New("name and an http(s) url are required")
	}
	switch s.Auth {
	case "", "none":
		s.Auth = "none"
	case "bearer":
		if !strings.HasPrefix(s.TokenRef, "vault://") {
			return errors.New("bearer auth needs token_ref = vault://Item/field (never a raw token)")
		}
	case "oauth":
	default:
		return errors.New("auth must be none, bearer or oauth")
	}
	switch s.Policy {
	case "", "inherit":
		s.Policy = ""
	case "auto", "approve", "off":
	default:
		return errors.New("policy must be auto, approve, off or empty (same as the mcp tool)")
	}
	mcpMu.Lock()
	defer mcpMu.Unlock()
	list := d.MCPServers()
	kept := list[:0]
	for _, x := range list {
		if x.Name != s.Name {
			kept = append(kept, x)
		}
	}
	kept = append(kept, s)
	b, _ := json.Marshal(kept)
	if err := d.St.SetSetting(mcpSettingKey, string(b)); err != nil {
		return err
	}
	d.registerMCPTool()
	return nil
}

func (d *Deps) DeleteMCPServer(name string) error {
	mcpMu.Lock()
	defer mcpMu.Unlock()
	list := d.MCPServers()
	kept := list[:0]
	for _, x := range list {
		if x.Name != name {
			kept = append(kept, x)
		}
	}
	if len(kept) == len(list) {
		return errors.New("unknown server")
	}
	b, _ := json.Marshal(kept)
	d.St.SetSetting(mcpSettingKey, string(b))
	d.registerMCPTool()
	return nil
}

// vaultMCPStore persists OAuth state in the vault and masks tokens.
type vaultMCPStore struct{ v *vault.Client }

func (s vaultMCPStore) Resolve(ctx context.Context, ref string) (string, error) {
	val, err := s.v.Resolve(ctx, ref)
	if err == nil {
		core.Secrets.Add(val)
		var st mcp.OAuthState
		if json.Unmarshal([]byte(val), &st) == nil {
			core.Secrets.Add(st.AccessToken, st.RefreshToken, st.ClientSecret)
		}
	}
	return val, err
}

func (s vaultMCPStore) SaveOAuth(ctx context.Context, item string, st mcp.OAuthState) error {
	b, _ := json.Marshal(st)
	core.Secrets.Add(st.AccessToken, st.RefreshToken, st.ClientSecret, string(b))
	_, _, err := s.v.Save(ctx, vault.SaveInput{Name: item, Kind: "note", Fields: map[string]string{"state": string(b)},
		Notes: "OAuth state of an MCP server, managed by Blavingad (internal/mcp). Do not edit by hand."})
	return err
}

func (d *Deps) MCPClient() *mcp.Client {
	return &mcp.Client{Store: vaultMCPStore{d.Vault}, ImgDir: filepath.Join(d.Cfg.WorkspaceDir, "mcp")}
}

// mcpPolicy is the per-server policy override of one mcp call ("" = tool policy).
func (d *Deps) mcpPolicy(args json.RawMessage) core.Policy {
	name := field(args, "server")
	if name == "" || strings.HasPrefix(name, "http") {
		return ""
	}
	s, ok := d.mcpServer(name)
	if !ok {
		return ""
	}
	if !s.Enabled {
		return core.PolicyOff
	}
	return core.Policy(s.Policy)
}

func (d *Deps) mcpDescription() string { return d.mcpDescriptionFor(nil) }

// mcpDescriptionFor lists only the servers the profile may use.
func (d *Deps) mcpDescriptionFor(p *core.Profile) string {
	var b strings.Builder
	b.WriteString("Calls a remote MCP server (Streamable HTTP; auth and OAuth token refresh handled, tokens in the vault). Actions: servers (configured servers + status), list (server → its tools with schemas), call (server + tool + arguments), auth_start / auth_finish (OAuth: open the URL, paste back the 127.0.0.1 redirect URL), add (name, url, auth none|bearer|oauth, token_ref vault://…, description). Images returned by tools are saved under /workspace/mcp/<server>/. Servers:")
	n := 0
	for _, s := range d.MCPServers() {
		if !s.Enabled || s.Policy == "off" || !p.AllowsMCP(s.Name) {
			continue
		}
		n++
		fmt.Fprintf(&b, "\n• %s: %s", s.Name, s.Description)
	}
	if n == 0 {
		b.WriteString(" none available.")
	}
	return b.String()
}

func registerMCP(d *Deps) {
	d.registerMCPTool()
	startLegacyImport(d)
}

func (d *Deps) registerMCPTool() {
	d.Reg.Add(&core.Tool{
		Name: "mcp", Category: "mcp", Emoji: "🔌", Risk: core.RiskSensitive,
		Description: d.mcpDescription(),
		DescribeFor: d.mcpDescriptionFor,
		CallPolicy:  d.mcpPolicy,
		Params: core.Obj(map[string]any{
			"action":       core.Enum("Action.", "servers", "list", "call", "auth_start", "auth_finish", "add"),
			"server":       core.Str("Server name (see servers), or a raw https URL for a public server."),
			"tool":         core.Str("Remote tool name (call)."),
			"arguments":    map[string]any{"type": "object", "description": "Remote tool arguments (call)."},
			"max_chars":    core.Int("Truncate output (default 15000)."),
			"redirect_url": core.Str("auth_finish: the full URL you landed on."),
			"url":          core.Str("add: server URL."),
			"auth":         core.Enum("add: auth mode.", "none", "bearer", "oauth"),
			"token_ref":    core.Str("add (bearer): vault://Item/field."),
			"description":  core.Str("add: one line, what it is for and its privacy rules."),
			"policy":       core.Enum("add: per-server policy (empty = same as the mcp tool).", "auto", "approve", "off"),
			"client_id":    core.Str("auth_start: OAuth client id if the server has no dynamic registration (optional)."),
			"scope":        core.Str("auth_start: OAuth scope (optional)."),
		}, "action"),
		Summarize: func(args json.RawMessage) string {
			s := "MCP " + field(args, "action") + " " + field(args, "server")
			if t := field(args, "tool"); t != "" {
				s += " → " + t
			}
			return s
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Action, Server, Tool, URL, Auth, Description, Scope, Policy string
				RedirectURL                                         string         `json:"redirect_url"`
				TokenRef                                            string         `json:"token_ref"`
				ClientID                                            string         `json:"client_id"`
				Arguments                                           map[string]any `json:"arguments"`
				MaxChars                                            int            `json:"max_chars"`
			}](args)
			if err != nil {
				return "", err
			}
			if a.MaxChars <= 0 {
				a.MaxChars = 15000
			}
			c := d.MCPClient()
			if a.Action == "servers" {
				var b strings.Builder
				for _, s := range d.MCPServers() {
					if !rc.Profile.AllowsMCP(s.Name) {
						continue
					}
					pol := s.Policy
					if pol == "" {
						pol = "same as mcp tool"
					}
					fmt.Fprintf(&b, "- %s (%s, auth %s, %s, enabled=%v, policy %s): %s\n", s.Name, s.URL, s.Auth, c.Status(ctx, s), s.Enabled, pol, s.Description)
				}
				if b.Len() == 0 {
					return "no MCP server configured (dashboard → MCP, or action=add)", nil
				}
				return b.String(), nil
			}
			if a.Action == "add" {
				if !rc.Profile.AllowsMCP("") {
					return "", errors.New("adding MCP servers needs full MCP access in your access profile")
				}
				s := mcp.Server{Name: a.Server, URL: a.URL, Auth: a.Auth, TokenRef: a.TokenRef, Description: a.Description, Enabled: true, Policy: a.Policy}
				if err := d.SaveMCPServer(s); err != nil {
					return "", err
				}
				d.St.Audit(rc.Actor(), "mcp.server_save", s.Name, map[string]string{"url": s.URL, "auth": s.Auth})
				return "saved server " + slugify(a.Server) + (map[bool]string{true: " — now run auth_start", false: ""}[a.Auth == "oauth"]), nil
			}
			var s mcp.Server
			if strings.HasPrefix(a.Server, "https://") || strings.HasPrefix(a.Server, "http://") {
				if !rc.Profile.AllowsMCP("") {
					return "", errors.New("ad-hoc MCP URLs are not allowed for this access profile")
				}
				s = mcp.Server{Name: "adhoc", URL: a.Server, Auth: "none", Enabled: true}
			} else {
				var ok bool
				if s, ok = d.mcpServer(a.Server); !ok || !rc.Profile.AllowsMCP(s.Name) {
					return "", fmt.Errorf("unknown MCP server %q (action=servers lists them)", a.Server)
				}
				if s.Policy == "off" {
					return "", errors.New("this server is switched off by an admin")
				}
				if !s.Enabled {
					return "", errors.New("this server is disabled in the dashboard")
				}
			}
			if s.Auth != "none" && s.Auth != "" {
				if err := d.vaultAllowed(rc); err != nil {
					return "", err
				}
			}
			switch a.Action {
			case "list":
				tools, err := c.List(ctx, s)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, t := range tools {
					props, _ := json.Marshal(t.InputSchema["properties"])
					fmt.Fprintf(&b, "- %s: %s | args: %s | required: %v\n", t.Name, textutil.Short(t.Description, 300), props, t.InputSchema["required"])
				}
				return Clip(b.String(), a.MaxChars), nil
			case "call":
				if a.Tool == "" {
					return "", errors.New("tool is required (use action=list)")
				}
				out, isErr, err := c.Call(ctx, s, a.Tool, a.Arguments)
				if err != nil {
					return "", err
				}
				if isErr {
					out = "ERROR: " + out
				}
				return Clip(out, a.MaxChars), nil
			case "auth_start":
				if s.Auth != "oauth" {
					return "", errors.New("this server does not use oauth")
				}
				u, err := c.AuthStart(ctx, s, a.ClientID, "", a.Scope, "")
				if err != nil {
					return "", err
				}
				return "Open this URL, approve, then give me the full 127.0.0.1 URL you land on (the page won't load, that's normal) for auth_finish:\n" + u, nil
			case "auth_finish":
				if err := c.AuthFinish(ctx, s, a.RedirectURL); err != nil {
					return "", err
				}
				d.St.Audit(rc.Actor(), "mcp.oauth_connected", s.Name, nil)
				return "connected to " + s.Name, nil
			}
			return "", errors.New("unknown action")
		},
	})
}
