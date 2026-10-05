package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// writeRoutes writes a routes file and returns its path.
func writeRoutes(t *testing.T, path string, routes ...Route) {
	t.Helper()
	raw, _ := json.Marshal(File{Routes: routes})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// backend starts an upstream that reports what it received.
func backend(t *testing.T, name string) (target string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|host=%s|xff=%s|proto=%s|xfhost=%s|path=%s", name, r.Host,
			r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"), r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// backendFunc starts an upstream with a handler of the test's own.
func backendFunc(t *testing.T, h http.HandlerFunc) (target string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// request sends one request with the given Host header to a handler.
func request(h http.Handler, method, host, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newProxy(t *testing.T, https bool, routes ...Route) (*Proxy, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.json")
	writeRoutes(t, path, routes...)
	p, err := New(path, https, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	return p, path
}

func TestRoutesByHost(t *testing.T) {
	a, b := backend(t, "a"), backend(t, "b")
	p, _ := newProxy(t, false, Route{Host: "a.example.com", Target: a}, Route{Host: "b.example.com", Target: b})

	rec := request(p.HTTP(), "GET", "A.Example.com:8080", "/x?y=1", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "a|host=A.Example.com:8080|") || !strings.HasSuffix(rec.Body.String(), "path=/x?y=1") {
		t.Fatalf("a: %d %s", rec.Code, rec.Body)
	}
	if rec := request(p.HTTP(), "GET", "b.example.com", "/", nil); !strings.HasPrefix(rec.Body.String(), "b|") {
		t.Fatalf("b: %s", rec.Body)
	}
	rec = request(p.HTTP(), "GET", "unknown.example.com", "/", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Nothing is deployed") {
		t.Fatalf("unknown host: %d %s", rec.Code, rec.Body)
	}
}

func TestForwardedHeadersCannotBeSpoofed(t *testing.T) {
	a := backend(t, "a")
	p, _ := newProxy(t, false, Route{Host: "a.example.com", Target: a})
	rec := request(p.HTTP(), "GET", "a.example.com", "/", http.Header{
		"X-Forwarded-For":   {"6.6.6.6"},
		"X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host":  {"evil.example.com"},
		"Forwarded":         {"for=6.6.6.6"},
	})
	body := rec.Body.String()
	// httptest.NewRequest sets RemoteAddr to 192.0.2.1.
	if !strings.Contains(body, "xff=192.0.2.1|") || !strings.Contains(body, "proto=http|") || !strings.Contains(body, "xfhost=a.example.com|") {
		t.Fatalf("forwarded headers: %s", body)
	}
}

func TestHTTPSRedirectAndWWW(t *testing.T) {
	a := backend(t, "a")
	routes := []Route{
		{Host: "example.com", Target: a, TLS: true},
		{Host: "www.example.com", RedirectTo: "example.com", TLS: true},
		{Host: "plain.example.com", Target: a},
		{Host: "www.plain.example.com", RedirectTo: "plain.example.com"},
	}
	p, _ := newProxy(t, true, routes...)

	cases := []struct{ handler, host, wantLocation string }{
		{"http", "example.com", "https://example.com/p?q=1"},
		{"http", "www.example.com", "https://example.com/p?q=1"},
		{"https", "www.example.com", "https://example.com/p?q=1"},
		{"http", "www.plain.example.com", "http://plain.example.com/p?q=1"},
	}
	for _, c := range cases {
		h := p.HTTP()
		if c.handler == "https" {
			h = p.HTTPS()
		}
		rec := request(h, "POST", c.host, "/p?q=1", nil)
		// 308 keeps the method, so a redirected POST is not turned into GET.
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != c.wantLocation {
			t.Errorf("%s %s: %d → %q, want 308 → %q", c.handler, c.host, rec.Code, rec.Header().Get("Location"), c.wantLocation)
		}
	}

	if rec := request(p.HTTP(), "GET", "plain.example.com", "/", nil); rec.Code != 200 {
		t.Errorf("HTTP-only host over HTTP: %d", rec.Code)
	}
	if rec := request(p.HTTPS(), "GET", "example.com", "/", nil); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "a|") {
		t.Errorf("TLS host over HTTPS: %d %s", rec.Code, rec.Body)
	}

	// Without a TLS listener, a TLS route is served rather than redirected
	// to a port nobody listens on.
	noTLS, _ := newProxy(t, false, routes...)
	if rec := request(noTLS.HTTP(), "GET", "example.com", "/", nil); rec.Code != 200 {
		t.Errorf("TLS route with HTTPS off: %d", rec.Code)
	}
}

func TestRedirectIgnoresHostHeaderTricks(t *testing.T) {
	a := backend(t, "a")
	p, _ := newProxy(t, true, Route{Host: "example.com", Target: a, TLS: true})
	// The Location is built from the route, never from the raw Host header.
	rec := request(p.HTTP(), "GET", "EXAMPLE.com:80", "//evil.example.net/x", nil)
	if loc := rec.Header().Get("Location"); loc != "https://example.com//evil.example.net/x" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestBadGatewayWhenTargetIsDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	p, _ := newProxy(t, false, Route{Host: "down.example.com", Target: dead})
	rec := request(p.HTTP(), "GET", "down.example.com", "/", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "not responding") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestReloadSwapsAndKeepsOldOnBadFile(t *testing.T) {
	a, b := backend(t, "a"), backend(t, "b")
	p, path := newProxy(t, false, Route{Host: "app.example.com", Target: a})

	writeRoutes(t, path, Route{Host: "app.example.com", Target: b})
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if rec := request(p.HTTP(), "GET", "app.example.com", "/", nil); !strings.HasPrefix(rec.Body.String(), "b|") {
		t.Fatalf("after reload: %s", rec.Body)
	}

	for name, content := range map[string]string{
		"half-written": `{"routes":[{"host":"app.exa`,
		"bad target":   `{"routes":[{"host":"app.example.com","target":"10.0.0.5:80"}]}`,
		"duplicate":    `{"routes":[{"host":"x.example.com","target":"127.0.0.1:1"},{"host":"X.example.com","target":"127.0.0.1:2"}]}`,
	} {
		os.WriteFile(path, []byte(content), 0o600)
		if err := p.Reload(); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if rec := request(p.HTTP(), "GET", "app.example.com", "/", nil); !strings.HasPrefix(rec.Body.String(), "b|") {
			t.Errorf("%s: the old routes were dropped", name)
		}
	}

	os.Remove(path)
	if err := p.Reload(); err != nil || p.Table().Len() != 0 {
		t.Fatalf("a missing file must be an empty table: %v, %d routes", err, p.Table().Len())
	}
}

func TestChangedDetectsARewrittenFile(t *testing.T) {
	a := backend(t, "a")
	p, path := newProxy(t, false, Route{Host: "app.example.com", Target: a})
	if p.Changed() {
		t.Fatal("a file that was just loaded is reported as changed")
	}
	writeRoutes(t, path, Route{Host: "app.example.com", Target: a}, Route{Host: "second.example.com", Target: a})
	if !p.Changed() {
		t.Fatal("a rewritten file is not noticed")
	}
	if err := p.Reload(); err != nil || p.Changed() || p.Table().Len() != 2 {
		t.Fatalf("after reload: err %v, changed %v, %d routes", err, p.Changed(), p.Table().Len())
	}
	// A bad file keeps the old table and is retried on the next poll.
	os.WriteFile(path, []byte("{"), 0o600)
	if !p.Changed() || p.Reload() == nil || p.Table().Len() != 2 {
		t.Fatal("a bad file must leave the table alone")
	}
	os.Remove(path)
	if !p.Changed() {
		t.Fatal("a removed file is not noticed")
	}
}

func TestHostPolicy(t *testing.T) {
	p, _ := newProxy(t, true,
		Route{Host: "secure.example.com", Target: "127.0.0.1:1", TLS: true},
		Route{Host: "plain.example.com", Target: "127.0.0.1:1"})
	ctx := context.Background()
	if err := p.hostPolicy(ctx, "secure.example.com"); err != nil {
		t.Errorf("TLS host refused: %v", err)
	}
	for _, host := range []string{"plain.example.com", "other.example.com", "", "1.2.3.4"} {
		if p.hostPolicy(ctx, host) == nil {
			t.Errorf("certificate would be requested for %q", host)
		}
	}
}

func TestParseValidation(t *testing.T) {
	bad := map[string]string{
		"both target and redirect": `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","redirect_to":"b.example.com"}]}`,
		"redirect to itself":       `{"routes":[{"host":"a.example.com","redirect_to":"a.example.com"}]}`,
		"no target":                `{"routes":[{"host":"a.example.com"}]}`,
		"ip as host":               `{"routes":[{"host":"1.2.3.4","target":"127.0.0.1:1"}]}`,
		"underscore host":          `{"routes":[{"host":"a_b.example.com","target":"127.0.0.1:1"}]}`,
		"host with path":           `{"routes":[{"host":"a.example.com/x","target":"127.0.0.1:1"}]}`,
		"port zero":                `{"routes":[{"host":"a.example.com","target":"127.0.0.1:0"}]}`,
		"hostname target":          `{"routes":[{"host":"a.example.com","target":"localhost:80"}]}`,
		"public target":            `{"routes":[{"host":"a.example.com","target":"8.8.8.8:80"}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	good := `{"email":"ops@example.com","routes":[{"host":"A.Example.com.","target":"127.0.0.1:20000","tls":true},{"host":"localhost","target":"[::1]:8000"}]}`
	tab, err := Parse(strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if rt, ok := tab.Lookup("a.example.com:443", "/"); !ok || !rt.TLS || tab.Email != "ops@example.com" {
		t.Fatalf("lookup failed: %+v %v", rt, ok)
	}
}

func TestWebSocketUpgradePasses(t *testing.T) {
	// An upstream that completes a protocol upgrade and echoes one line.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "expected upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		line, _ := buf.ReadString('\n')
		buf.WriteString("echo:" + line)
		buf.Flush()
	}))
	defer upstream.Close()

	p, _ := newProxy(t, false, Route{Host: "ws.example.com", Target: strings.TrimPrefix(upstream.URL, "http://")})
	front := httptest.NewServer(p.HTTP())
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /socket HTTP/1.1\r\nHost: ws.example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	rd := bufio.NewReader(conn)
	status, _ := rd.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status line %q", status)
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	fmt.Fprint(conn, "ping\n")
	if got, _ := rd.ReadString('\n'); got != "echo:ping\n" {
		t.Fatalf("got %q", got)
	}
}
