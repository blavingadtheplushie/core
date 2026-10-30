package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/mail"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

func (d *Deps) exa(ctx context.Context, path string, body any) (map[string]any, error) {
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai"+path, bytes.NewReader(payload))
	req.Header.Set("x-api-key", d.Cfg.ExaAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Exa HTTP %d: %s", resp.StatusCode, short(string(data), 300))
	}
	var out map[string]any
	return out, json.Unmarshal(data, &out)
}

func formatExaResults(res map[string]any, maxText int) string {
	results, _ := res["results"].([]any)
	if len(results) == 0 {
		return "No results."
	}
	var b strings.Builder
	for i, r := range results {
		m, _ := r.(map[string]any)
		fmt.Fprintf(&b, "[%d] %v\n    %v\n", i+1, m["title"], m["url"])
		if d, ok := m["publishedDate"].(string); ok && d != "" {
			fmt.Fprintf(&b, "    published: %s\n", strings.Split(d, "T")[0])
		}
		if hl, ok := m["highlights"].([]any); ok && len(hl) > 0 {
			for _, h := range hl {
				fmt.Fprintf(&b, "    » %s\n", short(fmt.Sprint(h), 400))
			}
		}
		if t, ok := m["text"].(string); ok && t != "" {
			fmt.Fprintf(&b, "    %s\n", strings.ReplaceAll(short(t, maxText), "\n", " "))
		}
		if s, ok := m["summary"].(string); ok && s != "" {
			fmt.Fprintf(&b, "    summary: %s\n", s)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func registerWeb(d *Deps) {
	exaOK := func() bool { return d.Cfg.ExaConfigured() }

	d.Reg.Add(&core.Tool{
		Name: "web_search", Category: "web", Emoji: "🔎", Available: exaOK,
		Description: "Searches the web (Exa). Returns titles, URLs, dates and relevant excerpts. Use it whenever information may be recent or you're not sure.",
		Params: core.Obj(map[string]any{
			"query":           core.Str("Descriptive natural-language query (Exa is a semantic search)."),
			"num_results":     core.Int("Number of results (1-15, default 6)."),
			"category":        core.Enum("Restrict to a category.", "company", "research paper", "news", "pdf", "github", "tweet", "personal site", "linkedin profile", "financial report"),
			"include_domains": core.StrList("Domains to include (e.g. [\"github.com\"])."),
			"start_date":      core.Str("Minimum ISO publication date (e.g. 2026-01-01)."),
		}, "query"),
		Summarize: summarizeField("Web search: ", "query", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Query          string   `json:"query"`
				NumResults     int      `json:"num_results"`
				Category       string   `json:"category"`
				IncludeDomains []string `json:"include_domains"`
				StartDate      string   `json:"start_date"`
			}](args)
			if err != nil {
				return "", err
			}
			if a.NumResults <= 0 || a.NumResults > 15 {
				a.NumResults = 6
			}
			body := map[string]any{"query": a.Query, "numResults": a.NumResults, "type": "auto",
				"contents": map[string]any{"highlights": map[string]any{"numSentences": 3, "highlightsPerUrl": 2}, "text": map[string]any{"maxCharacters": 1200}}}
			if a.Category != "" {
				body["category"] = a.Category
			}
			if len(a.IncludeDomains) > 0 {
				body["includeDomains"] = a.IncludeDomains
			}
			if a.StartDate != "" {
				body["startPublishedDate"] = a.StartDate
			}
			res, err := d.exa(ctx, "/search", body)
			if err != nil {
				return "", err
			}
			return formatExaResults(res, 600), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "web_answer", Category: "web", Emoji: "💡", Available: exaOK,
		Description: "Asks the web a factual question (Exa Answer) and gets a synthesized answer with its sources.",
		Params:      core.Obj(map[string]any{"question": core.Str("The question.")}, "question"),
		Summarize:   summarizeField("Asking the web: ", "question", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			q := field(args, "question")
			res, err := d.exa(ctx, "/answer", map[string]any{"query": q, "text": false})
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%v\n\nSources:\n", res["answer"])
			if cits, ok := res["citations"].([]any); ok {
				for _, c := range cits {
					m, _ := c.(map[string]any)
					fmt.Fprintf(&b, "- %v — %v\n", m["title"], m["url"])
				}
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "web_fetch", Category: "web", Emoji: "📄",
		Description: "Reads the text content of one or more web pages (via Exa if available, otherwise direct download).",
		Params: core.Obj(map[string]any{
			"urls":      core.StrList("URLs to read (max 5)."),
			"max_chars": core.Int("Max characters per page (default 12000)."),
		}, "urls"),
		Summarize: func(args json.RawMessage) string {
			a, _ := core.Parse[struct{ Urls []string }](args)
			if len(a.Urls) > 0 {
				return "Reading " + short(a.Urls[0], 70)
			}
			return ""
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Urls     []string `json:"urls"`
				MaxChars int      `json:"max_chars"`
			}](args)
			if err != nil {
				return "", err
			}
			if len(a.Urls) == 0 {
				return "", errors.New("no URL")
			}
			if len(a.Urls) > 5 {
				a.Urls = a.Urls[:5]
			}
			if a.MaxChars <= 0 {
				a.MaxChars = 12000
			}
			if d.Cfg.ExaConfigured() {
				res, err := d.exa(ctx, "/contents", map[string]any{"urls": a.Urls, "text": map[string]any{"maxCharacters": a.MaxChars}, "livecrawl": "fallback"})
				if err == nil {
					if results, _ := res["results"].([]any); len(results) > 0 {
						var b strings.Builder
						for _, r := range results {
							m, _ := r.(map[string]any)
							fmt.Fprintf(&b, "# %v\n%v\n\n%v\n\n", m["title"], m["url"], m["text"])
						}
						return Clip(b.String(), a.MaxChars*len(a.Urls)+2000), nil
					}
				}
			}
			var b strings.Builder
			for _, u := range a.Urls {
				text, err := directFetch(ctx, u, a.MaxChars)
				if err != nil {
					fmt.Fprintf(&b, "# %s\n❌ %v\n\n", u, err)
					continue
				}
				fmt.Fprintf(&b, "# %s\n%s\n\n", u, text)
			}
			return b.String(), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "http_request", Category: "web", Emoji: "🌐", Risk: core.RiskSensitive,
		Description: "Raw HTTP request to an API (GET, POST, PUT, PATCH, DELETE). GETs need no approval. To authenticate, write {{vault://Item/field}} in the URL, a header or the body (e.g. \"Authorization\": \"Bearer {{vault://CLOUDFLARE_API_TOKEN}}\"): the value is injected without you seeing it — which then requires approval.",
		Params: core.Obj(map[string]any{
			"method":  core.Enum("HTTP method.", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"),
			"url":     core.Str("Full URL."),
			"headers": map[string]any{"type": "object", "description": "HTTP headers.", "additionalProperties": map[string]any{"type": "string"}},
			"body":    core.Str("Request body (text or serialized JSON)."),
		}, "method", "url"),
		NeedsApproval: func(args json.RawMessage) bool {
			// Any secret leaving the container must be seen by a human
			// first, even in a GET: that is the classic exfiltration path.
			if hasPlaceholder(string(args)) {
				return true
			}
			m := strings.ToUpper(field(args, "method"))
			return m != "GET" && m != "HEAD" && m != ""
		},
		Summarize: func(args json.RawMessage) string {
			return strings.ToUpper(field(args, "method")) + " " + short(field(args, "url"), 80)
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			}](args)
			if err != nil {
				return "", err
			}
			if a.Method == "" {
				a.Method = "GET"
			}
			if a.URL, err = d.expand(ctx, rc, a.URL); err != nil {
				return "", err
			}
			if a.Body, err = d.expand(ctx, rc, a.Body); err != nil {
				return "", err
			}
			for k, v := range a.Headers {
				if a.Headers[k], err = d.expand(ctx, rc, v); err != nil {
					return "", err
				}
			}
			var body io.Reader
			if a.Body != "" {
				body = strings.NewReader(a.Body)
			}
			req, err := http.NewRequestWithContext(ctx, strings.ToUpper(a.Method), a.URL, body)
			if err != nil {
				return "", err
			}
			req.Header.Set("User-Agent", "BLAVINGAD/1.0")
			for k, v := range a.Headers {
				req.Header.Set(k, v)
			}
			if a.Body != "" && req.Header.Get("Content-Type") == "" && json.Valid([]byte(a.Body)) {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			var hdr strings.Builder
			for _, k := range []string{"Content-Type", "Location", "Content-Length"} {
				if v := resp.Header.Get(k); v != "" {
					fmt.Fprintf(&hdr, "%s: %s\n", k, v)
				}
			}
			return Clip(fmt.Sprintf("HTTP %d\n%s\n%s", resp.StatusCode, hdr.String(), string(data)), 0), nil
		},
	})
}

func directFetch(ctx context.Context, u string, max int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; BLAVINGAD/1.0)")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	text := string(data)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = mail.HTMLToText(text)
	}
	if len(text) > max {
		text = text[:max] + "\n…[truncated]"
	}
	return text, nil
}
