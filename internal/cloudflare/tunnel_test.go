package cloudflare

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI emulates the few Cloudflare endpoints used, with one zone
// example.com and one tunnel "tun1".
type fakeAPI struct {
	mu      sync.Mutex
	config  map[string]any
	records map[string]dnsRecord // by name
	nextID  int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) {
		b, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": v})
		w.Write(b)
	}
	body, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/cfd_tunnel/tun1/configurations") && r.Method == "GET":
		ok(map[string]any{"config": f.config})
	case strings.HasSuffix(p, "/cfd_tunnel/tun1/configurations") && r.Method == "PUT":
		var v struct{ Config map[string]any }
		json.Unmarshal(body, &v)
		f.config = v.Config
		ok(map[string]any{})
	case p == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			ok([]map[string]string{{"id": "z1", "name": "example.com"}})
		} else {
			ok([]any{})
		}
	case p == "/zones/z1/dns_records" && r.Method == "GET":
		if rec, found := f.records[r.URL.Query().Get("name")]; found {
			ok([]dnsRecord{rec})
		} else {
			ok([]any{})
		}
	case strings.HasPrefix(p, "/zones/z1/dns_records") && (r.Method == "POST" || r.Method == "PUT"):
		var rec dnsRecord
		json.Unmarshal(body, &rec)
		f.nextID++
		rec.ID = strings.TrimPrefix(p, "/zones/z1/dns_records/")
		if r.Method == "POST" {
			rec.ID = "r" + string(rune('0'+f.nextID))
		}
		f.records[rec.Name] = rec
		ok(rec)
	case strings.HasPrefix(p, "/zones/z1/dns_records/") && r.Method == "DELETE":
		id := strings.TrimPrefix(p, "/zones/z1/dns_records/")
		for n, rec := range f.records {
			if rec.ID == id {
				delete(f.records, n)
			}
		}
		ok(map[string]string{"id": id})
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"no route"}]}`))
	}
}

func setup(t *testing.T) (*Client, *fakeAPI) {
	f := &fakeAPI{
		config: map[string]any{"warp-routing": map[string]any{"enabled": false},
			"ingress": []any{map[string]any{"hostname": "old.example.com", "service": "http://localhost:81"}, map[string]any{"service": "http_status:404"}}},
		records: map[string]dnsRecord{"taken.example.com": {ID: "rx", Type: "A", Name: "taken.example.com", Content: "1.2.3.4"},
			"moved.example.com": {ID: "ry", Type: "CNAME", Name: "moved.example.com", Content: "elsewhere.net"}},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Client{Token: "t", Account: "acc", BaseURL: srv.URL}, f
}

func TestRoutesLifecycle(t *testing.T) {
	c, f := setup(t)
	ctx := context.Background()
	msg, err := c.AddRoute(ctx, "tun1", Route{Hostname: "https://App.Example.com/", Service: "3000"})
	if err != nil || !strings.Contains(msg, "route added: https://app.example.com → http://localhost:3000") || !strings.Contains(msg, "DNS CNAME created") {
		t.Fatalf("add: %q %v", msg, err)
	}
	if rec := f.records["app.example.com"]; rec.Content != "tun1.cfargotunnel.com" || rec.Type != "CNAME" {
		t.Errorf("dns = %+v", rec)
	}
	routes, _ := c.Routes(ctx, "tun1")
	if len(routes) != 2 {
		t.Fatalf("routes = %+v", routes)
	}
	ingress := f.config["ingress"].([]any)
	if last := ingress[len(ingress)-1].(map[string]any); last["service"] != "http_status:404" || last["hostname"] != nil {
		t.Errorf("the catch-all must stay last: %v", ingress)
	}
	if f.config["warp-routing"] == nil {
		t.Error("other config fields must be kept")
	}
	// Same hostname again: replaced, not duplicated; DNS untouched.
	msg, _ = c.AddRoute(ctx, "tun1", Route{Hostname: "app.example.com", Service: "http://localhost:4000"})
	if routes, _ = c.Routes(ctx, "tun1"); len(routes) != 2 || !strings.Contains(msg, "updated") || !strings.Contains(msg, "already points") {
		t.Errorf("update: %q %+v", msg, routes)
	}
	// An existing A record is not overwritten; a foreign CNAME is.
	if _, err := c.AddRoute(ctx, "tun1", Route{Hostname: "taken.example.com", Service: "8080"}); err == nil || !strings.Contains(err.Error(), "A record") {
		t.Errorf("A record conflict: %v", err)
	}
	if msg, err := c.AddRoute(ctx, "tun1", Route{Hostname: "moved.example.com", Service: "8080"}); err != nil || !strings.Contains(msg, "was elsewhere.net") {
		t.Errorf("cname update: %q %v", msg, err)
	}
	if _, err := c.AddRoute(ctx, "tun1", Route{Hostname: "x.other.org", Service: "80"}); err == nil || !strings.Contains(err.Error(), "no Cloudflare zone") {
		t.Errorf("unknown zone: %v", err)
	}
	msg, err = c.RemoveRoute(ctx, "tun1", "app.example.com")
	if err != nil || !strings.Contains(msg, "DNS record deleted") {
		t.Errorf("remove: %q %v", msg, err)
	}
	if _, found := f.records["app.example.com"]; found {
		t.Error("dns record must be deleted")
	}
	if _, err := c.RemoveRoute(ctx, "tun1", "nope.example.com"); err == nil {
		t.Error("removing an unknown route must fail")
	}
}

func TestNormalizeAndToken(t *testing.T) {
	for _, bad := range [][2]string{{"not a host", "80"}, {"app.example.com", "ftp://x"}, {"app.example.com", "localhost:80"}} {
		if _, _, err := Normalize(bad[0], bad[1]); err == nil {
			t.Errorf("%v should be rejected", bad)
		}
	}
	tok := base64.StdEncoding.EncodeToString([]byte(`{"a":"acc42","t":"tun-9","s":"secret"}`))
	if a, id, ok := ParseTunnelToken(tok); !ok || a != "acc42" || id != "tun-9" {
		t.Errorf("token: %s %s %v", a, id, ok)
	}
	if _, _, ok := ParseTunnelToken("garbage"); ok {
		t.Error("garbage token")
	}
}
