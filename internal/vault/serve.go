package vault

// Persistent `bw serve` backend. Every `bw` invocation boots node and
// reloads the whole CLI (3–4 s on this box); `bw serve` keeps one process
// alive and answers the same commands over a local REST API in ~10–50 ms.
//
// The server listens on a unix socket inside the CLI data dir (0700), so
// nothing else on the machine can reach it. It is unlocked once with the
// master password (POST /unlock, body only — never argv or our own env).
// Login/logout/config stay on the plain CLI; while they run the server is
// stopped, so the two never write data.json at the same time.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type serveProc struct {
	cmd    *exec.Cmd
	sock   string
	http   *http.Client
	done   chan struct{}
	stderr *bytes.Buffer
}

func serveDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("VAULT_BW_SERVE")))
	return v == "0" || v == "false" || v == "off"
}

// startServe launches `bw serve` and unlocks it. Errors are non-fatal for
// callers: the client simply keeps using the one-shot CLI.
func (c *Client) startServe(ctx context.Context) error {
	if serveDisabled() {
		return errors.New("disabled by VAULT_BW_SERVE")
	}
	c.stopServe()
	sock := filepath.Join(c.cfg.DataDir, "bw-serve.sock")
	os.Remove(sock)
	cmd := exec.Command(c.cfg.Binary, "serve", "--hostname", "unix://"+sock)
	cmd.Env = c.baseEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, Setpgid: true}
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errb
	if err := cmd.Start(); err != nil {
		return err
	}
	p := &serveProc{cmd: cmd, sock: sock, done: make(chan struct{}), stderr: &errb}
	p.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
	}}
	go func() { cmd.Wait(); close(p.done) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-p.done:
			return fmt.Errorf("bw serve exited: %s", cleanErr(errb.String()))
		default:
		}
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			p.kill()
			return errors.New("bw serve did not start in 20s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Unlock the long-lived process with the password in the request body.
	body, _ := json.Marshal(map[string]string{"password": c.cfg.Password})
	data, err := p.do(ctx, http.MethodPost, "/unlock", body)
	if err != nil {
		p.kill()
		return fmt.Errorf("bw serve unlock: %w", err)
	}
	// The server's unlock re-wraps the user key with a new session key: keep
	// it, so the one-shot CLI fallback still works afterwards.
	var u struct {
		Raw string `json:"raw"`
	}
	json.Unmarshal(data, &u)
	c.mu.Lock()
	if u.Raw != "" {
		c.session = strings.TrimSpace(u.Raw)
	}
	c.serve = p
	c.mu.Unlock()
	go func() {
		<-p.done
		c.mu.Lock()
		if c.serve == p {
			c.serve = nil
			slog.Warn("bw serve exited, falling back to the one-shot CLI")
		}
		c.mu.Unlock()
	}()
	return nil
}

func (c *Client) stopServe() {
	c.mu.Lock()
	p := c.serve
	c.serve = nil
	c.mu.Unlock()
	if p != nil {
		p.kill()
	}
}

// Close stops the background server (call on shutdown).
func (c *Client) Close() { c.stopServe() }

func (p *serveProc) kill() {
	if p.cmd.Process != nil {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	os.Remove(p.sock)
}

type serveResp struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// errTransport marks failures of the server itself (not of the command),
// after which the caller may retry with the one-shot CLI.
var errTransport = errors.New("bw serve unreachable")

func (p *serveProc) do(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost:8087"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Host = "localhost:8087" // bw serve rejects other Host headers
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTransport, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTransport, err)
	}
	var r serveResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%w: HTTP %d %s", errTransport, res.StatusCode, cleanErr(string(raw)))
	}
	if !r.Success {
		return nil, errors.New(cleanErr(r.Message))
	}
	return r.Data, nil
}

// serveRoute maps a CLI invocation to the equivalent `bw serve` request.
// ok=false means the command must go through the one-shot CLI.
func serveRoute(args []string) (method, path string, body []byte, ok bool) {
	if len(args) == 0 {
		return
	}
	decode := func(s string) ([]byte, bool) {
		b, err := base64.StdEncoding.DecodeString(s)
		return b, err == nil
	}
	switch args[0] {
	case "status":
		return http.MethodGet, "/status", nil, len(args) == 1
	case "sync":
		return http.MethodPost, "/sync", nil, len(args) == 1
	case "list":
		if len(args) == 2 {
			return http.MethodGet, "/list/object/" + url.PathEscape(args[1]), nil, true
		}
	case "get":
		if len(args) == 3 {
			return http.MethodGet, "/object/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2]), nil, true
		}
	case "delete":
		if len(args) == 3 {
			return http.MethodDelete, "/object/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2]), nil, true
		}
	case "create":
		if len(args) >= 3 {
			b, okb := decode(args[2])
			q := ""
			if len(args) == 5 && args[3] == "--organizationid" {
				q = "?organizationId=" + url.QueryEscape(args[4])
			} else if len(args) != 3 {
				return
			}
			return http.MethodPost, "/object/" + url.PathEscape(args[1]) + q, b, okb
		}
	case "edit":
		if len(args) == 4 {
			b, okb := decode(args[3])
			return http.MethodPut, "/object/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2]), b, okb
		}
	case "generate":
		if len(args) == 4 && args[1] == "-ulns" && args[2] == "--length" {
			return http.MethodGet, "/generate?uppercase=true&lowercase=true&number=true&special=true&length=" + url.QueryEscape(args[3]), nil, true
		}
	}
	return "", "", nil, false
}

// viaServe runs args on the persistent server when possible. handled=false
// means the caller should run the one-shot CLI instead.
func (c *Client) viaServe(ctx context.Context, args []string) (out string, handled bool, err error) {
	c.mu.RLock()
	p := c.serve
	c.mu.RUnlock()
	if p == nil {
		return "", false, nil
	}
	method, path, body, ok := serveRoute(args)
	if !ok {
		return "", false, nil
	}
	start := time.Now()
	data, err := p.do(ctx, method, path, body)
	if d := time.Since(start); d > 3*time.Second {
		slog.Info("slow bw serve call", "cmd", args[0], "took", d.Round(time.Millisecond))
	}
	if errors.Is(err, errTransport) {
		slog.Warn("bw serve failed, using the one-shot CLI", "cmd", args[0], "err", err)
		c.stopServe()
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("bw %s: %s", args[0], err.Error())
	}
	return unwrapServe(data), true, nil
}

// unwrapServe turns a serve payload into what the CLI would have printed:
// lists → the JSON array, strings (totp, generate) → the raw string,
// templates (status) → the inner object, objects → themselves.
func unwrapServe(data json.RawMessage) string {
	var env struct {
		Object   string          `json:"object"`
		Data     json.RawMessage `json:"data"`
		Template json.RawMessage `json:"template"`
	}
	if json.Unmarshal(data, &env) != nil {
		return strings.TrimSpace(string(data))
	}
	switch env.Object {
	case "list", "string":
		var s string
		if env.Object == "string" && json.Unmarshal(env.Data, &s) == nil {
			return s
		}
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return "[]"
		}
		return string(env.Data)
	case "template":
		return string(env.Template)
	}
	return strings.TrimSpace(string(data))
}
