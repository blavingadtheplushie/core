// Package web serves the admin dashboard (same design language as Fold) and
// its JSON API, protected by OIDC.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"blavingad/internal/agent"
	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/mail"
	"blavingad/internal/memory"
	"blavingad/internal/scheduler"
	"blavingad/internal/selfupdate"
	"blavingad/internal/store"
	"blavingad/internal/vault"
)

//go:embed static
var staticFS embed.FS

type Hooks struct {
	SlackStatus   func() map[string]any
	VaultStatus   func() map[string]any
	VaultSync     func(ctx context.Context) ([]string, error)
	Vault         *vault.Client
	RunSchedule   func(scheduler.Schedule)
	RunSelfReview func()
	ReloadSched   func()
	LLM           *llm.Client
}

type Server struct {
	cfg       *config.Config
	st        *store.Store
	mem       *memory.Service
	runner    *agent.Runner
	reg       *core.Registry
	approvals *core.Approvals
	access    *core.Access
	bus       *core.Bus
	self      *selfupdate.Manager
	mail      *mail.Service
	hooks     Hooks
	oc        oidcClient
	started   time.Time
	updating  atomic.Bool
}

func New(cfg *config.Config, st *store.Store, mem *memory.Service, runner *agent.Runner, reg *core.Registry, ap *core.Approvals,
	access *core.Access, bus *core.Bus, self *selfupdate.Manager, ml *mail.Service, hooks Hooks) *Server {
	return &Server{cfg: cfg, st: st, mem: mem, runner: runner, reg: reg, approvals: ap, access: access, bus: bus, self: self, mail: ml, hooks: hooks, started: time.Now()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

type userKey struct{}

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(userKey{}).(*User)
	return u
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			writeErr(w, 401, "authentication required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// Cookie auth + state change: refuse cross-origin requests.
			if origin := r.Header.Get("Origin"); origin != "" {
				o, err := url.Parse(origin)
				if err != nil || o.Host != r.Host {
					writeErr(w, 403, "origin denied")
					return
				}
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	a := s.auth

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": s.self.Version, "uptime": time.Since(s.started).Round(time.Second).String()})
	})
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("GET /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth/callback", s.handleCallback)
	mux.HandleFunc("POST /api/auth/password", s.handlePassword)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	mux.HandleFunc("GET /api/overview", a(s.overview))
	mux.HandleFunc("GET /api/events", a(s.events))

	mux.HandleFunc("GET /api/runs", a(s.listRuns))
	mux.HandleFunc("GET /api/runs/{id}", a(s.getRun))
	mux.HandleFunc("POST /api/runs/{id}/cancel", a(s.cancelRun))

	mux.HandleFunc("POST /api/chat", a(s.chat))
	mux.HandleFunc("GET /api/chat/threads", a(s.chatThreads))
	mux.HandleFunc("GET /api/chat/threads/{id}", a(s.chatThread))

	mux.HandleFunc("GET /api/approvals", a(s.listApprovals))
	mux.HandleFunc("POST /api/approvals/{id}", a(s.decideApproval))

	mux.HandleFunc("GET /api/memories", a(s.listMemories))
	mux.HandleFunc("POST /api/memories", a(s.createMemory))
	mux.HandleFunc("PATCH /api/memories/{id}", a(s.updateMemory))
	mux.HandleFunc("DELETE /api/memories/{id}", a(s.deleteMemory))
	mux.HandleFunc("GET /api/people", a(s.listPeople))
	mux.HandleFunc("PATCH /api/people/{id}", a(s.updatePerson))
	mux.HandleFunc("GET /api/channels", a(s.listChannels))
	mux.HandleFunc("POST /api/channels/{id}/digest", a(s.digestChannel))

	mux.HandleFunc("GET /api/tools", a(s.listTools))
	mux.HandleFunc("PATCH /api/tools/{name}", a(s.updateTool))
	mux.HandleFunc("DELETE /api/tools/{name}", a(s.deleteTool))

	mux.HandleFunc("GET /api/skills", a(s.listSkills))
	mux.HandleFunc("POST /api/skills", a(s.saveSkill))
	mux.HandleFunc("PATCH /api/skills/{name}", a(s.toggleSkill))
	mux.HandleFunc("DELETE /api/skills/{name}", a(s.deleteSkill))
	mux.HandleFunc("GET /api/persona", a(s.getPersona))
	mux.HandleFunc("PUT /api/persona", a(s.putPersona))

	mux.HandleFunc("GET /api/evolution", a(s.getEvolution))
	mux.HandleFunc("POST /api/evolution/review", a(s.triggerReview))
	mux.HandleFunc("POST /api/evolution/rollback", a(s.rollback))
	mux.HandleFunc("POST /api/evolution/update", a(s.updateSelf))

	mux.HandleFunc("GET /api/schedules", a(s.listSchedules))
	mux.HandleFunc("POST /api/schedules", a(s.createSchedule))
	mux.HandleFunc("PATCH /api/schedules/{id}", a(s.updateSchedule))
	mux.HandleFunc("DELETE /api/schedules/{id}", a(s.deleteSchedule))
	mux.HandleFunc("POST /api/schedules/{id}/run", a(s.runSchedule))

	mux.HandleFunc("GET /api/deployments", a(s.listDeployments))
	mux.HandleFunc("GET /api/tunnel", a(s.getTunnel))
	mux.HandleFunc("POST /api/tunnel/routes", a(s.addTunnelRoute))
	mux.HandleFunc("DELETE /api/tunnel/routes", a(s.removeTunnelRoute))
	mux.HandleFunc("GET /api/emails", a(s.listEmails))
	mux.HandleFunc("GET /api/emails/{id}", a(s.getEmail))
	mux.HandleFunc("POST /api/emails/refresh", a(s.refreshEmails))

	mux.HandleFunc("GET /api/access", a(s.getAccess))
	mux.HandleFunc("PUT /api/access/default", a(s.setAccessDefault))
	mux.HandleFunc("PUT /api/access/members/{id}", a(s.setAccessMember))
	mux.HandleFunc("DELETE /api/access/members/{id}", a(s.deleteAccessMember))
	mux.HandleFunc("POST /api/access/profiles", a(s.saveAccessProfile))
	mux.HandleFunc("DELETE /api/access/profiles/{name}", a(s.deleteAccessProfile))

	mux.HandleFunc("GET /api/vault", a(s.getVault))
	mux.HandleFunc("POST /api/vault/sync", a(s.syncVault))
	mux.HandleFunc("GET /api/vault/items", a(s.listVaultItems))
	mux.HandleFunc("POST /api/vault/items", a(s.saveVaultItem))
	mux.HandleFunc("DELETE /api/vault/items/{id}", a(s.deleteVaultItem))

	mux.HandleFunc("GET /api/llm/models", a(s.llmModels))
	mux.HandleFunc("POST /api/llm/test", a(s.llmTest))

	mux.HandleFunc("GET /api/settings", a(s.getSettings))
	mux.HandleFunc("PATCH /api/settings", a(s.patchSettings))
	mux.HandleFunc("GET /api/audit", a(s.listAudit))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "unknown route") })

	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != "/" && !strings.Contains(p[1:], "/") {
			if _, err := fs.Stat(sub, p[1:]); err == nil {
				if strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		files.ServeHTTP(w, r)
	})
	return s.secureHeaders(mux)
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if s.secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{Addr: s.cfg.WebAddr, Handler: s.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("web listening", "addr", s.cfg.WebAddr, "url", s.cfg.PublicURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("web: %w", err)
	}
	return nil
}

// events streams the bus over Server-Sent Events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, unsub := s.bus.Subscribe()
	defer unsub()
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
