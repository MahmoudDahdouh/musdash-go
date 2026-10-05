// Package test holds whole-binary tests.
package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Idle memory targets from the spec, in megabytes.
const (
	serverLimitMB = 30
	proxyLimitMB  = 20
)

// TestIdleRSS builds the release binary, starts the control plane and the
// proxy, uses each a little, lets them settle and fails when either holds
// more resident memory than its target.
//
// Linux is the deployment target and gives the authoritative number
// (`make rss-linux`); see assertRSS for how other systems are treated.
func TestIdleRSS(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped with -short")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "musdash")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "../cmd/musdash")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	t.Run("server", func(t *testing.T) {
		addr := freeAddr(t)
		pid := start(t, bin, "server", "-data", filepath.Join(dir, "data"), "-listen", addr)
		base := "http://" + addr
		waitReady(t, base+"/healthz")

		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
		page := fetch(t, client, base+"/setup")
		token := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)
		if token == nil {
			t.Fatal("no CSRF token on the setup page")
		}
		res, err := client.PostForm(base+"/setup", url.Values{
			"_csrf": {token[1]}, "name": {"RSS test"}, "email": {"rss@example.com"}, "password": {"an idle memory test"},
		})
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		for _, path := range []string{"/", "/projects", "/account", "/static/app.css", "/static/htmx.min.js"} {
			fetch(t, client, base+path)
		}
		client.CloseIdleConnections()
		assertRSS(t, pid, serverLimitMB)
	})

	t.Run("proxy", func(t *testing.T) {
		// A table of the size a busy small server has: fifty hosts, some
		// routed by path, some behind a password.
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "upstream "+r.URL.Path)
		}))
		defer upstream.Close()
		target := strings.TrimPrefix(upstream.URL, "http://")
		hash, err := bcrypt.GenerateFromPassword([]byte("the right password"), 10)
		if err != nil {
			t.Fatal(err)
		}
		type route struct {
			Host        string `json:"host"`
			Path        string `json:"path,omitempty"`
			StripPrefix bool   `json:"strip_prefix,omitempty"`
			Target      string `json:"target"`
			AuthUser    string `json:"auth_user,omitempty"`
			AuthHash    string `json:"auth_hash,omitempty"`
		}
		var routes []route
		for i := range 50 {
			host := "app" + strconv.Itoa(i) + ".example.com"
			routes = append(routes, route{Host: host, Target: target})
			if i%5 == 0 {
				routes = append(routes, route{Host: host, Path: "/api", StripPrefix: true, Target: target})
			}
			if i%10 == 0 {
				routes = append(routes, route{Host: host, Path: "/admin", Target: target, AuthUser: "ada", AuthHash: string(hash)})
			}
		}
		raw, _ := json.Marshal(map[string]any{"routes": routes})
		proxyDir := filepath.Join(dir, "data", "proxy")
		if err := os.MkdirAll(proxyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proxyDir, "routes.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}

		addr := freeAddr(t)
		pid := start(t, bin, "proxy", "-data", filepath.Join(dir, "data"), "-http", addr, "-https=")
		waitReady(t, "http://"+addr+"/")
		client := &http.Client{Timeout: 30 * time.Second}
		ask := func(host, path, password string) int {
			req, _ := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
			req.Host = host
			if password != "" {
				req.SetBasicAuth("ada", password)
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			return res.StatusCode
		}
		for range 5 {
			fetch(t, client, "http://"+addr+"/")
		}
		// Used as it would be: plain routes, routes by path, a password
		// that is right many times over and wrong a few times.
		for i := range 50 {
			host := "app" + strconv.Itoa(i) + ".example.com"
			if code := ask(host, "/", ""); code != http.StatusOK {
				t.Fatalf("%s: %d", host, code)
			}
		}
		for i := 0; i < 50; i += 10 {
			host := "app" + strconv.Itoa(i) + ".example.com"
			if code := ask(host, "/api/users", ""); code != http.StatusOK {
				t.Fatalf("%s/api: %d", host, code)
			}
			if code := ask(host, "/admin", ""); code != http.StatusUnauthorized {
				t.Fatalf("%s/admin without a password: %d", host, code)
			}
			for n := range 3 {
				if code := ask(host, "/admin", "a wrong guess "+strconv.Itoa(n)); code != http.StatusUnauthorized {
					t.Fatalf("%s/admin with a wrong password: %d", host, code)
				}
			}
			for range 20 {
				if code := ask(host, "/admin/page", "the right password"); code != http.StatusOK {
					t.Fatalf("%s/admin with the password: %d", host, code)
				}
			}
		}
		client.CloseIdleConnections()
		assertRSS(t, pid, proxyLimitMB)
	})
}

// freeAddr returns a loopback address with a port nothing is using.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// start runs the binary and stops it when the test ends.
func start(t *testing.T, bin string, args ...string) int {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// No GOMEMLIMIT or GOGC here: the binary must meet the target with the
	// settings it applies to itself.
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=", "GOGC=", "MUSDASH_MASTER_KEY=")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
		}
	})
	return cmd.Process.Pid
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := http.Get(url); err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not answer", url)
}

func fetch(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return string(body)
}

// assertRSS waits for the process to settle, then compares its resident
// memory with the limit.
//
// The targets are for Linux, where musdash is deployed, and are enforced
// exactly there. Other systems count a process's memory differently (macOS
// includes more of the mapped binary), so there the figure is held to a
// looser ceiling that still catches a real regression.
func assertRSS(t *testing.T, pid, limitMB int) {
	t.Helper()
	time.Sleep(3 * time.Second)
	kb, err := rssKB(pid)
	if err != nil {
		t.Fatal(err)
	}
	mb := float64(kb) / 1024
	limit := float64(limitMB)
	if runtime.GOOS != "linux" {
		limit *= 1.3
		t.Logf("idle RSS %.1f MB on %s/%s (target %d MB applies on Linux; sanity ceiling here %.0f MB). Run `make rss-linux` for the real figure.",
			mb, runtime.GOOS, runtime.GOARCH, limitMB, limit)
	} else {
		t.Logf("idle RSS %.1f MB (limit %d MB, %s/%s)", mb, limitMB, runtime.GOOS, runtime.GOARCH)
	}
	if mb > limit {
		t.Fatalf("idle RSS %.1f MB exceeds %.0f MB", mb, limit)
	}
}

// rssKB returns a process's resident set size in kilobytes.
func rssKB(pid int) (int, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				return strconv.Atoi(strings.Fields(rest)[0])
			}
		}
		return 0, fmt.Errorf("no VmRSS for pid %d", pid)
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
