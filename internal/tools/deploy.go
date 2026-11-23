package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/store"
	"blavingad/internal/textutil"
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

	d.Reg.Add(&core.Tool{
		Name: "deploy_cloudflare_pages", Category: "deploy", Emoji: "🚀", Risk: core.RiskSensitive, Available: cfOK,
		Description: "Deploys a static folder (built site: HTML/CSS/JS) to Cloudflare Pages. Creates the project if it doesn't exist. Returns the public *.pages.dev URL.",
		Params: core.Obj(map[string]any{
			"path":    core.Str("Folder to publish (already built)."),
			"project": core.Str("Pages project name (lowercase, dashes)."),
		}, "path", "project"),
		Summarize: func(args json.RawMessage) string {
			return "Cloudflare Pages: " + field(args, "project") + " ← " + textutil.Short(field(args, "path"), 50)
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
			name := textutil.FirstNonEmpty(slugify(a.Name), dir)
			if res.ExitCode != 0 {
				d.recordDeploy(rc, "cloudflare_worker", name, url, "failed", map[string]string{"path": dir})
				return "", fmt.Errorf("deployment failed:\n%s", Clip(res.Output, 4000))
			}
			d.recordDeploy(rc, "cloudflare_worker", name, url, "live", map[string]string{"path": dir})
			return "✅ Worker deployed " + url + "\n\n" + Clip(res.Output, 3000), nil
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
					return "", fmt.Errorf("could not create the repo: %s", textutil.Short(string(data), 300))
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
