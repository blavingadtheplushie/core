package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/store"
)

var (
	reSlug       = regexp.MustCompile(`[^a-z0-9-]+`)
	rePagesURL   = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.pages\.dev`)
	reWorkersURL = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.workers\.dev`)
)

func slugify(s string) string {
	s = reSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (d *Deps) recordDeploy(rc *core.RunContext, kind, name, url, status string, details any) string {
	id := store.NewID("dep")
	d.St.DB.Exec(`INSERT INTO deployments(id, kind, name, url, status, details, run_id, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, kind, name, url, status, store.ToJSON(details), rc.RunID, store.Now())
	d.St.Audit(rc.Actor(), "deploy."+kind, name, map[string]string{"url": url, "status": status})
	d.Bus.Publish("deployment", map[string]string{"id": id, "kind": kind, "name": name, "url": url, "status": status})
	return id
}

func registerDeploy(d *Deps) {
	cfOK := d.Cfg.CloudflareConfigured
	dockerOK := func() bool { return d.Cfg.DockerEnabled }

	d.Reg.Add(&core.Tool{
		Name: "deploy_cloudflare_pages", Category: "deploy", Emoji: "🚀", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Deploys a static folder (built site: HTML/CSS/JS) to Cloudflare Pages. Creates the project if it doesn't exist. Returns the public *.pages.dev URL.",
		Params: core.Obj(map[string]any{
			"path":    core.Str("Folder to publish (already built)."),
			"project": core.Str("Pages project name (lowercase, dashes)."),
		}, "path", "project"),
		Summarize: func(args json.RawMessage) string {
			return "Cloudflare Pages: " + field(args, "project") + " ← " + short(field(args, "path"), 50)
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Path, Project string }](args)
			dir, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			project := slugify(a.Project)
			if project == "" {
				return "", errors.New("invalid project name")
			}
			d.sh(ctx, dir, "wrangler pages project create "+q(project)+" --production-branch main", 2*time.Minute)
			res := d.sh(ctx, dir, "wrangler pages deploy "+q(dir)+" --project-name "+q(project)+" --branch main --commit-dirty=true", 10*time.Minute)
			url := "https://" + project + ".pages.dev"
			status := "live"
			if res.ExitCode != 0 {
				status = "failed"
			}
			preview := rePagesURL.FindString(res.Output)
			d.recordDeploy(rc, "cloudflare_pages", project, url, status, map[string]string{"preview": preview, "path": dir})
			if status == "failed" {
				return "", fmt.Errorf("deployment failed:\n%s", Clip(res.Output, 4000))
			}
			return fmt.Sprintf("✅ Deployed to %s (preview of this deployment: %s)\n\n%s", url, preview, Clip(res.Output, 2500)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "deploy_cloudflare_worker", Category: "deploy", Emoji: "⚡", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Deploys a Cloudflare Worker from a folder containing wrangler.toml / wrangler.jsonc (runs `wrangler deploy`). Installs npm dependencies if needed.",
		Params: core.Obj(map[string]any{
			"path": core.Str("Worker project folder."),
			"name": core.Str("Worker name (optional, defaults to the one in wrangler.toml)."),
		}, "path"),
		Summarize: summarizeField("Cloudflare Worker ← ", "path", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Path, Name string }](args)
			dir, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			cmd := "if [ -f package.json ] && [ ! -d node_modules ]; then npm install --no-audit --no-fund; fi; wrangler deploy"
			if a.Name != "" {
				cmd += " --name " + q(slugify(a.Name))
			}
			res := d.sh(ctx, dir, cmd, 10*time.Minute)
			url := reWorkersURL.FindString(res.Output)
			name := firstNonEmpty(slugify(a.Name), dir)
			if res.ExitCode != 0 {
				d.recordDeploy(rc, "cloudflare_worker", name, url, "failed", map[string]string{"path": dir})
				return "", fmt.Errorf("deployment failed:\n%s", Clip(res.Output, 4000))
			}
			d.recordDeploy(rc, "cloudflare_worker", name, url, "live", map[string]string{"path": dir})
			return "✅ Worker deployed " + url + "\n\n" + Clip(res.Output, 3000), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "deploy_docker", Category: "deploy", Emoji: "🐳", Risk: core.RiskSensitive, Available: dockerOK,
		Description: "Deploys an app as a Docker container on the host: builds from a folder with a Dockerfile (or uses an existing image), then (re)starts the container persistently and exposes it. Returns the URL.",
		Params: core.Obj(map[string]any{
			"name":  core.Str("App name (lowercase, dashes)."),
			"path":  core.Str("Folder containing the Dockerfile (or empty if using an image)."),
			"image": core.Str("Image to run when there is no build (e.g. nginx:alpine)."),
			"port":  core.Int("Port listened on INSIDE the container (e.g. 3000)."),
			"env":   map[string]any{"type": "object", "description": "Environment variables.", "additionalProperties": map[string]any{"type": "string"}},
		}, "name", "port"),
		Summarize: summarizeField("Docker: ", "name", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Name, Path, Image string
				Port              int
				Env               map[string]string
			}](args)
			name := slugify(a.Name)
			if name == "" || a.Port <= 0 {
				return "", errors.New("name and port are required")
			}
			container := "blv-" + name
			image := a.Image
			var log strings.Builder
			if a.Path != "" {
				dir, err := d.resolvePath(a.Path)
				if err != nil {
					return "", err
				}
				image = container + ":latest"
				if rc.Progress != nil {
					rc.Progress("🏗️ Building image " + image)
				}
				res := d.sh(ctx, dir, "docker build -t "+q(image)+" .", 20*time.Minute)
				log.WriteString(Clip(res.Output, 3000))
				if res.ExitCode != 0 {
					d.recordDeploy(rc, "docker", name, "", "failed", map[string]string{"stage": "build"})
					return "", fmt.Errorf("build failed:\n%s", Clip(res.Output, 5000))
				}
			}
			if image == "" {
				return "", errors.New("path or image is required")
			}
			hostPort := d.dockerPort(name)
			run := []string{"docker run -d --name", q(container), "--restart unless-stopped", "--label blavingad.app=" + q(name)}
			url := fmt.Sprintf("http://%s:%d", d.Cfg.DeployHost, hostPort)
			if d.Cfg.DeployNetwork != "" {
				run = append(run, "--network", q(d.Cfg.DeployNetwork))
			}
			if d.Cfg.DeployTraefik && d.Cfg.DeployDomain != "" {
				host := name + "." + d.Cfg.DeployDomain
				url = "https://" + host
				run = append(run,
					"--label traefik.enable=true",
					"--label "+q("traefik.http.routers."+container+".rule=Host(`"+host+"`)"),
					"--label "+q("traefik.http.routers."+container+".tls.certresolver=letsencrypt"),
					"--label "+q(fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%d", container, a.Port)))
			} else {
				run = append(run, "-p", fmt.Sprintf("%d:%d", hostPort, a.Port))
			}
			keys := make([]string, 0, len(a.Env))
			for k := range a.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				run = append(run, "-e", q(k+"="+a.Env[k]))
			}
			run = append(run, q(image))
			d.sh(ctx, "", "docker rm -f "+q(container), time.Minute)
			res := d.sh(ctx, "", strings.Join(run, " "), 5*time.Minute)
			if res.ExitCode != 0 {
				d.recordDeploy(rc, "docker", name, url, "failed", map[string]any{"stage": "run", "port": hostPort})
				return "", fmt.Errorf("start failed:\n%s", Clip(res.Output, 4000))
			}
			time.Sleep(3 * time.Second)
			state := d.sh(ctx, "", "docker inspect -f '{{.State.Status}}' "+q(container), 30*time.Second)
			logs := d.sh(ctx, "", "docker logs --tail 20 "+q(container), 30*time.Second)
			d.recordDeploy(rc, "docker", name, url, "live", map[string]any{"image": image, "port": hostPort, "container_port": a.Port, "container": container})
			return fmt.Sprintf("✅ %s started (%s) → %s\n\nLatest logs:\n%s", container, strings.TrimSpace(state.Output), url, Clip(logs.Output, 2000)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "docker_manage", Category: "deploy", Emoji: "🛳️", Risk: core.RiskSensitive, Available: dockerOK,
		Description: "Manages the Docker apps you deployed: list, logs, restart, stop, remove. list and logs need no approval.",
		Params: core.Obj(map[string]any{
			"action": core.Enum("Action.", "list", "logs", "restart", "stop", "remove"),
			"name":   core.Str("App name (except for list)."),
		}, "action"),
		NeedsApproval: func(args json.RawMessage) bool {
			a := field(args, "action")
			return a != "list" && a != "logs"
		},
		Summarize: func(args json.RawMessage) string {
			return "Docker " + field(args, "action") + " " + field(args, "name")
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Action, Name string }](args)
			c := q("blv-" + slugify(a.Name))
			var cmd string
			switch a.Action {
			case "list":
				cmd = `docker ps -a --filter label=blavingad.app --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}\t{{.Image}}'`
			case "logs":
				cmd = "docker logs --tail 120 " + c
			case "restart":
				cmd = "docker restart " + c
			case "stop":
				cmd = "docker stop " + c
			case "remove":
				cmd = "docker rm -f " + c
				d.St.DB.Exec(`UPDATE deployments SET status = 'removed' WHERE kind = 'docker' AND name = ?`, slugify(a.Name))
			default:
				return "", errors.New("unknown action")
			}
			return d.sh(ctx, "", cmd, 2*time.Minute).String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "deploy_github_pages", Category: "deploy", Emoji: "📄", Risk: core.RiskSensitive, Available: d.Cfg.GitHubConfigured,
		Description: "Publishes a static folder to GitHub Pages (gh-pages branch of a public repo on your account, created if needed). Returns the github.io URL.",
		Params: core.Obj(map[string]any{
			"path": core.Str("Folder to publish."),
			"repo": core.Str("Repo name (on your account)."),
		}, "path", "repo"),
		Summarize: summarizeField("GitHub Pages: ", "repo", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Path, Repo string }](args)
			dir, err := d.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			user := d.ghUser(ctx)
			if user == "" {
				return "", errors.New("could not determine the GitHub account")
			}
			repo := slugify(a.Repo)
			if strings.EqualFold(a.Repo, user+".github.io") {
				repo = strings.ToLower(user) + ".github.io"
			}
			if status, _, _ := d.gh(ctx, "GET", "/repos/"+user+"/"+repo, nil); status == 404 {
				st, data, err := d.gh(ctx, "POST", "/user/repos", map[string]any{"name": repo, "private": false, "description": "Published by " + d.Cfg.AgentName})
				if err != nil || st >= 400 {
					return "", fmt.Errorf("could not create the repo: %s", short(string(data), 300))
				}
			}
			tmp, _ := os.MkdirTemp("", "ghpages-")
			defer os.RemoveAll(tmp)
			script := strings.Join([]string{
				"cp -a " + q(dir) + "/. " + q(tmp),
				"cd " + q(tmp), "rm -rf .git", "touch .nojekyll",
				"git init -q -b gh-pages", "git add -A",
				"git commit -q -m " + q("🚀 Published by "+d.Cfg.AgentName),
				"git push -f -q https://github.com/" + user + "/" + repo + ".git gh-pages",
			}, " && ")
			res := d.sh(ctx, tmp, script, 5*time.Minute)
			if res.ExitCode != 0 {
				d.recordDeploy(rc, "github_pages", repo, "", "failed", nil)
				return "", fmt.Errorf("push failed:\n%s", Clip(res.Output, 3000))
			}
			d.gh(ctx, "POST", "/repos/"+user+"/"+repo+"/pages", map[string]any{"source": map[string]string{"branch": "gh-pages", "path": "/"}})
			url := fmt.Sprintf("https://%s.github.io/%s/", strings.ToLower(user), repo)
			if strings.HasSuffix(repo, ".github.io") {
				url = "https://" + repo + "/"
			}
			d.recordDeploy(rc, "github_pages", repo, url, "live", map[string]string{"repo": user + "/" + repo})
			return "✅ Published to " + url + " (the first publish can take 1 to 2 minutes).", nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "deployments_list", Category: "deploy", Emoji: "🗺️",
		Description: "Lists your recent deployments (type, name, URL, status).",
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			rows, err := d.St.Rows(`SELECT kind, name, url, status, created_at FROM deployments ORDER BY created_at DESC LIMIT 30`)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "No deployments.", nil
			}
			var b strings.Builder
			for _, r := range rows {
				fmt.Fprintf(&b, "- [%v] %v %v → %v (%v)\n", r["status"], r["kind"], r["name"], r["url"], r["created_at"])
			}
			return b.String(), nil
		},
	})
}

// dockerPort keeps an app on the same host port across redeploys.
func (d *Deps) dockerPort(name string) int {
	rows, _ := d.St.Rows(`SELECT name, details FROM deployments WHERE kind = 'docker' AND details LIKE '%"port"%'`)
	used := map[int]bool{}
	for _, r := range rows {
		var det struct{ Port int }
		json.Unmarshal([]byte(fmt.Sprint(r["details"])), &det)
		if det.Port == 0 {
			continue
		}
		if r["name"] == name {
			return det.Port
		}
		used[det.Port] = true
	}
	p := d.Cfg.DeployPortBase
	for used[p] {
		p++
	}
	return p
}
