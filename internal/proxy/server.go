package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ListenAndServe runs the proxy on its own address (never the admin
// dashboard's). Errors are logged, never fatal: the agent boots anyway.
func (p *Proxy) ListenAndServe(ctx context.Context, addr string) {
	if addr == "" || addr == "off" {
		return
	}
	srv := &http.Server{Addr: addr, Handler: p.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("llm proxy disabled", "err", err)
		return
	}
	p.listening.Store(true)
	defer p.listening.Store(false)
	slog.Info("llm proxy listening", "addr", addr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		slog.Error("llm proxy disabled", "err", err)
	}
}

// Listening reports whether the proxy listener is up.
func (p *Proxy) Listening() bool { return p.listening.Load() }
