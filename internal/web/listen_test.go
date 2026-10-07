package web

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The dashboard read up to a megabyte of headers from anybody. It reads
// what it can be sent in earnest, and says so to whoever sends more.
func TestTheDashboardReadsOnlySoMuchOfARequestsHeaders(t *testing.T) {
	a := newApp(t, false)
	srv := HTTPServer(a.server.Handler())
	// Streams must stay open, and a slow client must not hold a connection.
	if srv.WriteTimeout != 0 || srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatalf("timeouts: %+v", srv)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	get := func(headerBytes int) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/static/nothing", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Filler", strings.Repeat("a", headerBytes))
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%d bytes of headers: %v", headerBytes, err)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	// More than a browser sends with every cookie it has for the site.
	if status := get(16 << 10); status == http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("16 KB of headers were refused")
	}
	// Go reads a little more than the limit before it gives up.
	if status := get(maxHeaderBytes + 16<<10); status != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("%d KB of headers: status %d", (maxHeaderBytes+16<<10)>>10, status)
	}
}
