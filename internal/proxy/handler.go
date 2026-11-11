package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxBody = 32 << 20

// Handler serves the public OpenAI/Anthropic-compatible API.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { p.serve(w, r, "openai", "/chat/completions") })
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) { p.serve(w, r, "anthropic", "/messages") })
	mux.HandleFunc("/v1/models", p.models)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintln(w, "blavingad llm proxy: POST /v1/chat/completions (openai) or /v1/messages (anthropic), GET /v1/models")
			return
		}
		apiError(w, "openai", 404, "not_found", "unknown endpoint")
	})
	return mux
}

func clientKey(r *http.Request) string {
	if k := r.Header.Get("x-api-key"); k != "" {
		return strings.TrimSpace(k)
	}
	a := r.Header.Get("Authorization")
	if len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return ""
}

func apiError(w http.ResponseWriter, style string, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if style == "anthropic" {
		json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": typ, "message": msg, "code": typ}})
}

func (p *Proxy) auth(w http.ResponseWriter, r *http.Request, style string) (Key, bool) {
	k, ok := p.lookupKey(clientKey(r))
	if !ok {
		apiError(w, style, 401, "authentication_error", "invalid or revoked API key")
	}
	return k, ok
}

func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	k, ok := p.auth(w, r, "openai")
	if !ok {
		return
	}
	var data []map[string]any
	for _, a := range p.Aliases() {
		if len(k.Models) == 0 || contains(k.Models, a) {
			data = append(data, map[string]any{"id": a, "object": "model", "owned_by": "blavingad"})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request, style, path string) {
	if r.Method != http.MethodPost {
		apiError(w, style, 405, "invalid_request_error", "POST only")
		return
	}
	key, ok := p.auth(w, r, style)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		apiError(w, style, 400, "invalid_request_error", "cannot read body")
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		apiError(w, style, 400, "invalid_request_error", "body must be a JSON object")
		return
	}
	model, _ := body["model"].(string)
	stream, _ := body["stream"].(bool)
	if model == "" {
		apiError(w, style, 400, "invalid_request_error", "model is required")
		return
	}
	if len(key.Models) > 0 && !contains(key.Models, model) {
		apiError(w, style, 403, "permission_error", "this key may not use model "+model)
		return
	}
	cands := p.candidates(style, model)
	if len(cands) == 0 {
		apiError(w, style, 404, "not_found_error", fmt.Sprintf("no %s upstream serves model %q", style, model))
		return
	}
	var lastStatus int
	var lastBody []byte
	for _, c := range cands {
		if r.Context().Err() != nil {
			return
		}
		body["model"] = c.model
		out, _ := json.Marshal(body)
		start := time.Now()
		resp, err := p.send(r.Context(), r, c.up, path, out)
		if err != nil {
			why := "network: " + err.Error()
			p.markDown(c.up.Name, why)
			p.logUsage(key.ID, c.up.Name, c.model, 0, stream, start, usage{}, why)
			slog.Warn("llm proxy upstream failed", "upstream", c.up.Name, "err", err)
			lastStatus, lastBody = 502, nil
			continue
		}
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if retryable(resp.StatusCode, b) {
				why := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, clip(string(b), 200))
				p.markDown(c.up.Name, why)
				p.logUsage(key.ID, c.up.Name, c.model, resp.StatusCode, stream, start, usage{}, why)
				slog.Warn("llm proxy fallback", "upstream", c.up.Name, "status", resp.StatusCode)
				lastStatus, lastBody = resp.StatusCode, b
				continue
			}
			p.logUsage(key.ID, c.up.Name, c.model, resp.StatusCode, stream, start, usage{}, clip(string(b), 200))
			copyHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			w.Write(b)
			return
		}
		p.markUp(c.up.Name)
		u := relay(w, resp)
		resp.Body.Close()
		p.logUsage(key.ID, c.up.Name, c.model, resp.StatusCode, stream, start, u, "")
		return
	}
	if lastBody != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastStatus)
		w.Write(lastBody)
		return
	}
	apiError(w, style, 502, "api_error", "every upstream failed")
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (p *Proxy) send(ctx context.Context, in *http.Request, u Upstream, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a := in.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	key, err := p.upstreamKey(ctx, u)
	if err != nil {
		return nil, err
	}
	if u.Style == "anthropic" {
		v := in.Header.Get("anthropic-version")
		if v == "" {
			v = "2023-06-01"
		}
		req.Header.Set("anthropic-version", v)
		if b := in.Header.Get("anthropic-beta"); b != "" {
			req.Header.Set("anthropic-beta", b)
		}
		if key != "" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("Authorization", "Bearer "+key)
		}
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range u.Headers {
		req.Header.Set(k, v)
	}
	return p.client.Do(req)
}

func (p *Proxy) upstreamKey(ctx context.Context, u Upstream) (string, error) {
	switch {
	case u.KeyRef == "":
		return "", nil
	case u.KeyRef == "config":
		return p.configKey, nil
	case p.resolve == nil:
		return "", fmt.Errorf("vault unavailable for %s", u.Name)
	}
	v, err := p.resolve(ctx, u.KeyRef)
	if err != nil {
		return "", fmt.Errorf("key %s: %w", u.KeyRef, err)
	}
	return v, nil
}

func copyHeaders(w http.ResponseWriter, h http.Header) {
	for _, k := range []string{"Content-Type", "Cache-Control", "Request-Id", "X-Request-Id"} {
		if v := h.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
}
