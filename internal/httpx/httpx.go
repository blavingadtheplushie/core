// Package httpx holds the one shared HTTP client for outbound API calls.
// Use httpx.Client (or httpx.WithTimeout for a different deadline) instead of
// declaring a new http.Client in each package.
package httpx

import (
	"net/http"
	"time"
)

// Client is the default outbound client (60 s timeout, shared transport).
var Client = WithTimeout(60 * time.Second)

// WithTimeout returns a client sharing the default transport.
func WithTimeout(d time.Duration) *http.Client {
	return &http.Client{Timeout: d, Transport: http.DefaultTransport}
}
