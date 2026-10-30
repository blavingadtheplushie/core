package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"blavingad/internal/core"
)

func (d *Deps) gh(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			if b != "" {
				r = strings.NewReader(b)
			}
		default:
			payload, _ := json.Marshal(b)
			r = bytes.NewReader(payload)
		}
	}
	if !strings.HasPrefix(path, "http") {
		path = "https://api.github.com/" + strings.TrimPrefix(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Cfg.GitHubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "BLAVINGAD")
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, data, nil
}

func (d *Deps) ghUser(ctx context.Context) string {
	if d.Cfg.GitHubUser != "" {
		return d.Cfg.GitHubUser
	}
	_, data, err := d.gh(ctx, "GET", "/user", nil)
	if err != nil {
		return ""
	}
	var u struct{ Login string }
	json.Unmarshal(data, &u)
	d.Cfg.GitHubUser = u.Login
	return u.Login
}

// compactJSON trims GitHub's very verbose payloads to what matters.
func compactJSON(data []byte) string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return Clip(string(data), 0)
	}
	strip(v)
	b, _ := json.MarshalIndent(v, "", " ")
	return Clip(string(b), 0)
}

func strip(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if val == nil || strings.HasSuffix(k, "_url") && k != "html_url" && k != "clone_url" || k == "node_id" || k == "_links" || k == "gravatar_id" || k == "reactions" || k == "permissions" {
				delete(t, k)
				continue
			}
			strip(val)
		}
	case []any:
		for _, item := range t {
			strip(item)
		}
	}
}

func registerGitHub(d *Deps) {
	ok := d.Cfg.GitHubConfigured

	d.Reg.Add(&core.Tool{
		Name: "github_api", Category: "github", Emoji: "🐙", Risk: core.RiskSensitive, Available: ok,
		Description: "Calls the GitHub REST API with YOUR account (issues, PRs, repos, actions, releases, gists…). GETs run without approval. For git (clone/commit/push), use the shell tool: credentials are already configured.",
		Params: core.Obj(map[string]any{
			"method": core.Enum("Method.", "GET", "POST", "PATCH", "PUT", "DELETE"),
			"path":   core.Str("API path, e.g. /repos/owner/repo/issues?state=open"),
			"body":   core.Str("JSON body (serialized) for POST/PATCH/PUT."),
		}, "method", "path"),
		NeedsApproval: func(args json.RawMessage) bool { return strings.ToUpper(field(args, "method")) != "GET" },
		Summarize: func(args json.RawMessage) string {
			return "GitHub " + strings.ToUpper(field(args, "method")) + " " + short(field(args, "path"), 70)
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Method, Path, Body string }](args)
			status, data, err := d.gh(ctx, strings.ToUpper(a.Method), a.Path, a.Body)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("HTTP %d\n%s", status, compactJSON(data)), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "github_search", Category: "github", Emoji: "🔭", Available: ok,
		Description: "Searches GitHub: repos, code, issues/PRs or users.",
		Params: core.Obj(map[string]any{
			"type":  core.Enum("Search type.", "repositories", "code", "issues", "users"),
			"query": core.Str("GitHub query (GitHub search syntax, e.g. 'language:go stars:>500 slack bot')."),
		}, "type", "query"),
		Summarize: summarizeField("GitHub: ", "query", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Type, Query string }](args)
			status, data, err := d.gh(ctx, "GET", "/search/"+a.Type+"?per_page=10&q="+url.QueryEscape(a.Query), nil)
			if err != nil {
				return "", err
			}
			if status >= 400 {
				return "", fmt.Errorf("GitHub HTTP %d: %s", status, short(string(data), 300))
			}
			var res struct {
				TotalCount int              `json:"total_count"`
				Items      []map[string]any `json:"items"`
			}
			json.Unmarshal(data, &res)
			var b strings.Builder
			fmt.Fprintf(&b, "%d results\n", res.TotalCount)
			for _, it := range res.Items {
				switch a.Type {
				case "repositories":
					fmt.Fprintf(&b, "- %v ⭐%v — %v\n  %v\n", it["full_name"], it["stargazers_count"], short(fmt.Sprint(it["description"]), 140), it["html_url"])
				case "code":
					repo, _ := it["repository"].(map[string]any)
					fmt.Fprintf(&b, "- %v: %v\n  %v\n", repo["full_name"], it["path"], it["html_url"])
				case "issues":
					fmt.Fprintf(&b, "- #%v [%v] %v\n  %v\n", it["number"], it["state"], it["title"], it["html_url"])
				default:
					fmt.Fprintf(&b, "- %v %v\n", it["login"], it["html_url"])
				}
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "github_create_repo", Category: "github", Emoji: "📦", Risk: core.RiskSensitive, Available: ok,
		Description: "Creates a GitHub repo on your account.",
		Params: core.Obj(map[string]any{
			"name":        core.Str("Repo name."),
			"description": core.Str("Description."),
			"private":     core.Bool("Private repo (default true)."),
		}, "name"),
		Summarize: summarizeField("New repo ", "name", 50),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				Name, Description string
				Private           *bool
			}](args)
			private := true
			if a.Private != nil {
				private = *a.Private
			}
			status, data, err := d.gh(ctx, "POST", "/user/repos", map[string]any{"name": a.Name, "description": a.Description, "private": private, "auto_init": false})
			if err != nil {
				return "", err
			}
			if status >= 400 {
				return "", fmt.Errorf("GitHub HTTP %d: %s", status, short(string(data), 400))
			}
			var repo map[string]any
			json.Unmarshal(data, &repo)
			d.St.Audit(rc.Actor(), "github.repo.create", fmt.Sprint(repo["full_name"]), nil)
			return fmt.Sprintf("Repo created: %v\nclone: %v", repo["html_url"], repo["clone_url"]), nil
		},
	})
}
