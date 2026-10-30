package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"blavingad/internal/cloudflare"
	"blavingad/internal/core"
	"blavingad/internal/vault"
)

func (d *Deps) tunnel() (*cloudflare.Client, string, error) {
	cf := cloudflare.New(d.Cfg)
	if !cf.Configured() {
		return nil, "", errors.New("Cloudflare is not configured: set CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID")
	}
	id := cloudflare.TunnelID(d.Cfg, d.St)
	if id == "" {
		return cf, "", cloudflare.ErrNoTunnel
	}
	return cf, id, nil
}

// tunnelService is where cloudflared reaches an app deployed with
// deploy_docker: localhost when it runs next to Docker (LXC/VM install),
// the container name when it is a sidecar on the deploy network.
func (d *Deps) tunnelService(container string, hostPort, containerPort int) string {
	if _, err := exec.LookPath("cloudflared"); err == nil {
		return fmt.Sprintf("http://localhost:%d", hostPort)
	}
	return fmt.Sprintf("http://%s:%d", container, containerPort)
}

func registerTunnel(d *Deps) {
	cfOK := func() bool { return cloudflare.New(d.Cfg).Configured() }

	d.Reg.Add(&core.Tool{
		Name: "tunnel_status", Category: "deploy", Emoji: "🚇", Available: cfOK,
		Description: "Shows the Cloudflare Tunnel (name, health, connectors) and its public routes (hostname → local service). Use it before adding or removing routes.",
		Params:      core.Obj(map[string]any{}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			cf, id, err := d.tunnel()
			if err != nil {
				return "", err
			}
			t, err := cf.Tunnel(ctx, id)
			if err != nil {
				return "", err
			}
			routes, err := cf.Routes(ctx, id)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Tunnel %s (%s) — %s, %d connection(s)\n", t.Name, t.ID, t.Status, len(t.Connections))
			if len(routes) == 0 {
				b.WriteString("No public route yet.")
			}
			for _, r := range routes {
				fmt.Fprintf(&b, "• https://%s%s → %s\n", r.Hostname, r.Path, r.Service)
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "tunnel_route_add", Category: "deploy", Emoji: "🚇", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Publishes a local service on the Internet through the Cloudflare Tunnel: hostname (in a zone of the Cloudflare account) → service (e.g. http://localhost:3000, or just the port). Creates/updates the proxied DNS CNAME. Replaces an existing route for the same hostname. HTTPS is handled by Cloudflare.",
		Params: core.Obj(map[string]any{
			"hostname": core.Str("Public hostname, e.g. app.example.com."),
			"service":  core.Str("Origin reached by cloudflared: http://localhost:PORT (or a port number), https://…, tcp://…, ssh://…"),
			"path":     core.Str("Optional path regex to restrict the route (e.g. ^/api)."),
		}, "hostname", "service"),
		Summarize: func(args json.RawMessage) string {
			return "Tunnel: " + field(args, "hostname") + " → " + short(field(args, "service"), 40)
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct{ Hostname, Service, Path string }](args)
			if err != nil {
				return "", err
			}
			cf, id, err := d.tunnel()
			if err != nil {
				return "", err
			}
			msg, err := cf.AddRoute(ctx, id, cloudflare.Route{Hostname: a.Hostname, Service: a.Service, Path: a.Path})
			if err != nil {
				return "", err
			}
			h, s, _ := cloudflare.Normalize(a.Hostname, a.Service)
			d.recordDeploy(rc, "tunnel", h, "https://"+h+a.Path, "live", map[string]string{"service": s, "tunnel": id})
			return "✅ " + msg + "\nDNS can take a minute to propagate; test with http_request.", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "tunnel_route_remove", Category: "deploy", Emoji: "🚇", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Unpublishes a hostname from the Cloudflare Tunnel and deletes its DNS record if it points to the tunnel.",
		Params:      core.Obj(map[string]any{"hostname": core.Str("Hostname to remove.")}, "hostname"),
		Summarize:   summarizeField("Tunnel: remove ", "hostname", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Hostname string }](args)
			cf, id, err := d.tunnel()
			if err != nil {
				return "", err
			}
			msg, err := cf.RemoveRoute(ctx, id, a.Hostname)
			if err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "tunnel.route_remove", a.Hostname, nil)
			return "✅ " + msg, nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "tunnel_setup", Category: "deploy", Emoji: "🚇", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Sets up the Cloudflare Tunnel once: finds or creates a remotely-managed tunnel, then starts its connector (cloudflared system service when running as root with systemd, otherwise stores the connector token in the vault). Safe to re-run: it reports what is already in place.",
		Params:      core.Obj(map[string]any{"name": core.Str("Tunnel name (default: the agent's name).")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Name string }](args)
			return d.setupTunnel(ctx, rc, a.Name)
		},
	})
}

func (d *Deps) setupTunnel(ctx context.Context, rc *core.RunContext, name string) (string, error) {
	cf, id, err := d.tunnel()
	if err != nil && !errors.Is(err, cloudflare.ErrNoTunnel) {
		return "", err
	}
	var notes []string
	if id == "" {
		name = firstNonEmpty(slugify(name), slugify(d.Cfg.AgentName), "blavingad")
		t, found, err := cf.FindTunnel(ctx, name)
		if err != nil {
			return "", err
		}
		if !found {
			if t, err = cf.CreateTunnel(ctx, name); err != nil {
				return "", err
			}
			notes = append(notes, "created tunnel "+name)
		} else {
			notes = append(notes, "reusing the existing tunnel "+name)
		}
		id = t.ID
		d.St.SetSetting(cloudflare.SettingTunnelID, id)
		d.St.Audit(rc.Actor(), "tunnel.setup", name, map[string]string{"id": id})
	}
	t, err := cf.Tunnel(ctx, id)
	if err != nil {
		return "", err
	}
	if t.Status == "healthy" || len(t.Connections) > 0 {
		notes = append(notes, fmt.Sprintf("connector already running (%s, %d connection(s))", t.Status, len(t.Connections)))
		return fmt.Sprintf("✅ Tunnel %s (%s): %s.", t.Name, t.ID, strings.Join(notes, "; ")), nil
	}

	token := d.Cfg.CloudflareTunnelToken
	if token == "" {
		if token, err = cf.ConnectorToken(ctx, id); err != nil {
			return "", fmt.Errorf("tunnel %s is ready but its connector token could not be read: %w", id, err)
		}
	}
	core.Secrets.Add(token)

	_, errCF := exec.LookPath("cloudflared")
	_, errSD := exec.LookPath("systemctl")
	if os.Geteuid() == 0 && errCF == nil && errSD == nil {
		if out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "cloudflared").Output(); strings.TrimSpace(string(out)) == "active" {
			notes = append(notes, "a cloudflared service is active but this tunnel has no connection: it probably runs another tunnel's token — check `systemctl cat cloudflared`")
		} else {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			out, err := exec.CommandContext(cctx, "cloudflared", "service", "install", token).CombinedOutput()
			cancel()
			if err != nil {
				return "", fmt.Errorf("cloudflared service install failed: %s", Clip(core.Secrets.Redact(string(out)), 1500))
			}
			notes = append(notes, "installed and started the cloudflared service")
		}
	} else if d.Vault != nil && d.Vault.Enabled() && d.Cfg.CloudflareTunnelToken == "" {
		if _, _, err := d.Vault.Save(ctx, vault.SaveInput{Name: "CLOUDFLARE_TUNNEL_TOKEN", Password: token,
			Notes: "Connector token of Cloudflare Tunnel " + t.Name + " (" + id + ")"}); err != nil {
			notes = append(notes, "could not store the connector token in the vault: "+err.Error())
		} else {
			notes = append(notes, "stored the connector token in the vault as CLOUDFLARE_TUNNEL_TOKEN — start the connector with `docker compose --profile tunnel up -d` (or `cloudflared service install` on the host)")
		}
	} else {
		notes = append(notes, "no way to start cloudflared from here: install it and run it with the tunnel's token (Cloudflare dashboard › Zero Trust › Networks › Tunnels › "+t.Name+")")
	}
	return fmt.Sprintf("✅ Tunnel %s (%s): %s. Add public routes with tunnel_route_add.", t.Name, t.ID, strings.Join(notes, "; ")), nil
}
