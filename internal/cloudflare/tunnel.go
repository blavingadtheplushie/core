// Package cloudflare manages a remotely-managed Cloudflare Tunnel: its
// public hostnames (ingress rules) and the DNS records pointing at it.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"blavingad/internal/config"
	"blavingad/internal/store"
)

// SettingTunnelID stores the id of a tunnel created by the agent itself.
const SettingTunnelID = "cloudflare.tunnel_id"

type Client struct {
	Token   string
	Account string
	BaseURL string
	HTTP    *http.Client
}

// New builds a client from the config; the account may come from the
// tunnel token when CLOUDFLARE_ACCOUNT_ID is not set.
func New(cfg *config.Config) *Client {
	account := cfg.CloudflareAccount
	if a, _, ok := ParseTunnelToken(cfg.CloudflareTunnelToken); ok && account == "" {
		account = a
	}
	return &Client{Token: cfg.CloudflareToken, Account: account}
}

func (c *Client) Configured() bool { return c.Token != "" && c.Account != "" }

// TunnelID: explicit setting, then the connector token, then the tunnel the
// agent created.
func TunnelID(cfg *config.Config, st *store.Store) string {
	if cfg.CloudflareTunnelID != "" {
		return cfg.CloudflareTunnelID
	}
	if _, t, ok := ParseTunnelToken(cfg.CloudflareTunnelToken); ok {
		return t
	}
	if st != nil {
		return st.GetSetting(SettingTunnelID, "")
	}
	return ""
}

// ParseTunnelToken decodes a connector token (base64 JSON {"a","t","s"}).
func ParseTunnelToken(tok string) (account, tunnelID string, ok bool) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", "", false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		raw, err := enc.DecodeString(tok)
		if err != nil {
			continue
		}
		var v struct{ A, T string }
		if json.Unmarshal(raw, &v) == nil && v.T != "" {
			return v.A, v.T, true
		}
	}
	return "", "", false
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env struct {
		Success bool            `json:"success"`
		Errors  []apiError      `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("cloudflare %s %s: HTTP %d, unreadable answer", method, path, resp.StatusCode)
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%s (%d)", e.Message, e.Code))
		}
		msg := strings.Join(msgs, "; ")
		if resp.StatusCode == 403 || strings.Contains(strings.ToLower(msg), "authentication") {
			msg += " — the API token needs Account › Cloudflare Tunnel: Edit, Zone › DNS: Edit and Zone › Zone: Read"
		}
		return fmt.Errorf("cloudflare: %s", msg)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// ---------- tunnels ----------

type Tunnel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"` // inactive | degraded | healthy | down
	Connections []struct {
		ColoName string `json:"colo_name"`
	} `json:"connections"`
	ConfigSrc string `json:"config_src"`
}

func (c *Client) Tunnel(ctx context.Context, id string) (Tunnel, error) {
	var t Tunnel
	err := c.do(ctx, "GET", "/accounts/"+c.Account+"/cfd_tunnel/"+id, nil, &t)
	return t, err
}

// FindTunnel returns the live tunnel with that name, if any.
func (c *Client) FindTunnel(ctx context.Context, name string) (Tunnel, bool, error) {
	var list []Tunnel
	if err := c.do(ctx, "GET", "/accounts/"+c.Account+"/cfd_tunnel?is_deleted=false&name="+name, nil, &list); err != nil {
		return Tunnel{}, false, err
	}
	for _, t := range list {
		if t.Name == name {
			return t, true, nil
		}
	}
	return Tunnel{}, false, nil
}

// CreateTunnel creates a remotely-managed tunnel (routes live in Cloudflare).
func (c *Client) CreateTunnel(ctx context.Context, name string) (Tunnel, error) {
	var t Tunnel
	err := c.do(ctx, "POST", "/accounts/"+c.Account+"/cfd_tunnel", map[string]any{"name": name, "config_src": "cloudflare"}, &t)
	return t, err
}

// ConnectorToken is the secret cloudflared runs with.
func (c *Client) ConnectorToken(ctx context.Context, id string) (string, error) {
	var tok string
	err := c.do(ctx, "GET", "/accounts/"+c.Account+"/cfd_tunnel/"+id+"/token", nil, &tok)
	return tok, err
}

// ---------- routes ----------

type Route struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path,omitempty"`
	Service  string `json:"service"`
}

type tunnelConfig struct {
	Config map[string]any `json:"config"`
}

func (c *Client) config(ctx context.Context, id string) (map[string]any, []map[string]any, error) {
	var tc tunnelConfig
	if err := c.do(ctx, "GET", "/accounts/"+c.Account+"/cfd_tunnel/"+id+"/configurations", nil, &tc); err != nil {
		return nil, nil, err
	}
	if tc.Config == nil {
		tc.Config = map[string]any{}
	}
	var rules []map[string]any
	if list, ok := tc.Config["ingress"].([]any); ok {
		for _, r := range list {
			if m, ok := r.(map[string]any); ok {
				rules = append(rules, m)
			}
		}
	}
	return tc.Config, rules, nil
}

// putRules writes the ingress back, keeping every other config field and
// ending with a catch-all as cloudflared requires.
func (c *Client) putRules(ctx context.Context, id string, cfg map[string]any, rules []map[string]any) error {
	var out []map[string]any
	var catchAll map[string]any
	for _, r := range rules {
		if str(r["hostname"]) == "" && str(r["path"]) == "" {
			catchAll = r
			continue
		}
		out = append(out, r)
	}
	if catchAll == nil {
		catchAll = map[string]any{"service": "http_status:404"}
	}
	cfg["ingress"] = append(out, catchAll)
	return c.do(ctx, "PUT", "/accounts/"+c.Account+"/cfd_tunnel/"+id+"/configurations", map[string]any{"config": cfg}, nil)
}

func (c *Client) Routes(ctx context.Context, id string) ([]Route, error) {
	_, rules, err := c.config(ctx, id)
	if err != nil {
		return nil, err
	}
	var routes []Route
	for _, r := range rules {
		if h := str(r["hostname"]); h != "" {
			routes = append(routes, Route{Hostname: h, Path: str(r["path"]), Service: str(r["service"])})
		}
	}
	return routes, nil
}

var (
	reHost    = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
	reService = regexp.MustCompile(`^(https?|tcp|ssh|rdp|smb|unix|unix\+tls)://\S+$|^http_status:\d{3}$|^hello_world$`)
	rePort    = regexp.MustCompile(`^\d{2,5}$`)
)

// Normalize validates a hostname and a service, filling defaults.
func Normalize(hostname, service string) (string, string, error) {
	h := strings.ToLower(strings.TrimSpace(hostname))
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"), "/")
	if !reHost.MatchString(h) {
		return "", "", fmt.Errorf("invalid hostname %q (expected e.g. app.example.com)", hostname)
	}
	s := strings.TrimSpace(service)
	if rePort.MatchString(s) {
		s = "http://localhost:" + s
	}
	if !reService.MatchString(s) {
		return "", "", fmt.Errorf("invalid service %q (expected e.g. http://localhost:3000)", service)
	}
	return h, s, nil
}

// AddRoute publishes hostname → service and points the DNS at the tunnel.
// It replaces an existing rule for the same hostname and path.
func (c *Client) AddRoute(ctx context.Context, id string, r Route) (string, error) {
	h, s, err := Normalize(r.Hostname, r.Service)
	if err != nil {
		return "", err
	}
	cfg, rules, err := c.config(ctx, id)
	if err != nil {
		return "", err
	}
	replaced := false
	var kept []map[string]any
	for _, rule := range rules {
		if strings.EqualFold(str(rule["hostname"]), h) && str(rule["path"]) == r.Path {
			replaced = true
			continue
		}
		kept = append(kept, rule)
	}
	rule := map[string]any{"hostname": h, "service": s, "originRequest": map[string]any{}}
	if r.Path != "" {
		rule["path"] = r.Path
	}
	if err := c.putRules(ctx, id, cfg, append([]map[string]any{rule}, kept...)); err != nil {
		return "", err
	}
	dns, err := c.pointDNS(ctx, id, h)
	if err != nil {
		return "", fmt.Errorf("route saved, but DNS failed: %w", err)
	}
	verb := "added"
	if replaced {
		verb = "updated"
	}
	return fmt.Sprintf("route %s: https://%s%s → %s · %s", verb, h, r.Path, s, dns), nil
}

// RemoveRoute deletes every rule for hostname and its DNS record when it
// points at this tunnel.
func (c *Client) RemoveRoute(ctx context.Context, id, hostname string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(hostname))
	cfg, rules, err := c.config(ctx, id)
	if err != nil {
		return "", err
	}
	var kept []map[string]any
	removed := 0
	for _, rule := range rules {
		if strings.EqualFold(str(rule["hostname"]), h) {
			removed++
			continue
		}
		kept = append(kept, rule)
	}
	if removed == 0 {
		return "", fmt.Errorf("no route for %s on this tunnel", h)
	}
	if err := c.putRules(ctx, id, cfg, kept); err != nil {
		return "", err
	}
	note := "DNS record left alone"
	if zone, err := c.zoneFor(ctx, h); err == nil {
		if rec, ok, _ := c.record(ctx, zone, h); ok && rec.Content == id+".cfargotunnel.com" {
			if c.do(ctx, "DELETE", "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil) == nil {
				note = "DNS record deleted"
			}
		}
	}
	return fmt.Sprintf("route removed: %s (%s)", h, note), nil
}

// ---------- DNS ----------

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// zoneFor finds the account zone holding hostname (longest match).
func (c *Client) zoneFor(ctx context.Context, hostname string) (string, error) {
	labels := strings.Split(strings.TrimPrefix(hostname, "*."), ".")
	for i := 0; i < len(labels)-1; i++ {
		name := strings.Join(labels[i:], ".")
		var zones []struct{ ID, Name string }
		if err := c.do(ctx, "GET", "/zones?name="+name+"&account.id="+c.Account, nil, &zones); err != nil {
			return "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("no Cloudflare zone of this account holds %s", hostname)
}

func (c *Client) record(ctx context.Context, zone, name string) (dnsRecord, bool, error) {
	var recs []dnsRecord
	if err := c.do(ctx, "GET", "/zones/"+zone+"/dns_records?name="+name, nil, &recs); err != nil {
		return dnsRecord{}, false, err
	}
	for _, r := range recs {
		if strings.EqualFold(r.Name, name) {
			return r, true, nil
		}
	}
	return dnsRecord{}, false, nil
}

func (c *Client) pointDNS(ctx context.Context, id, hostname string) (string, error) {
	zone, err := c.zoneFor(ctx, hostname)
	if err != nil {
		return "", err
	}
	target := id + ".cfargotunnel.com"
	body := map[string]any{"type": "CNAME", "name": hostname, "content": target, "proxied": true, "comment": "BLAVINGAD tunnel route"}
	rec, ok, err := c.record(ctx, zone, hostname)
	if err != nil {
		return "", err
	}
	switch {
	case ok && rec.Content == target:
		return "DNS already points to the tunnel", nil
	case ok && rec.Type != "CNAME":
		return "", fmt.Errorf("%s already has a %s record (%s): remove it first", hostname, rec.Type, rec.Content)
	case ok:
		return "DNS CNAME updated (was " + rec.Content + ")", c.do(ctx, "PUT", "/zones/"+zone+"/dns_records/"+rec.ID, body, nil)
	}
	return "DNS CNAME created", c.do(ctx, "POST", "/zones/"+zone+"/dns_records", body, nil)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ErrNoTunnel is returned when no tunnel is known yet.
var ErrNoTunnel = errors.New("no Cloudflare Tunnel configured yet: run tunnel_setup (or set CLOUDFLARE_TUNNEL_TOKEN / CLOUDFLARE_TUNNEL_ID)")
