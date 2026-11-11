package proxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// InternalKeyID identifies the agent's own calls in the usage log.
const InternalKeyID = "internal"

// InternalKey is an in-memory key (regenerated each boot, never stored)
// that lets the agent itself call the proxy over loopback.
func (p *Proxy) InternalKey() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.internal == "" {
		b := make([]byte, 24)
		rand.Read(b)
		p.internal = "sk-blv-int-" + hex.EncodeToString(b)
	}
	return p.internal
}

func (p *Proxy) isInternal(k string) bool {
	in := p.InternalKey()
	return k != "" && subtle.ConstantTimeCompare([]byte(k), []byte(in)) == 1
}

// HasStyle reports whether an enabled upstream speaks this API style, so the
// agent only routes through the proxy when it can actually be served.
func (p *Proxy) HasStyle(style string) bool {
	for _, u := range p.Upstreams() {
		if u.Enabled && u.Style == style {
			return true
		}
	}
	return false
}

// LocalURL turns a listen address (":8090", "0.0.0.0:8090") into a loopback
// base URL ("http://127.0.0.1:8090/v1"); "" when the proxy is off.
func LocalURL(addr string) string {
	if addr == "" || addr == "off" {
		return ""
	}
	host, port := "127.0.0.1", addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if h := addr[:i]; h != "" && h != "0.0.0.0" && h != "[::]" {
			host = h
		}
		port = addr[i+1:]
	}
	return "http://" + host + ":" + port + "/v1"
}
