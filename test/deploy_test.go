package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeployWithDocker is the phase 1 acceptance test. It runs the real
// binary against the real Docker daemon: deploy an nginx image, reach it
// through the proxy by host name, redeploy while requests keep arriving and
// require that none fail, then stop the app and require its route to go.
//
// It needs Docker and pulls an image, so it runs only when asked:
//
//	MUSDASH_DOCKER_TEST=1 go test ./test -run TestDeployWithDocker -v
func TestDeployWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skipf("Docker is not reachable: %v", err)
	}

	dir := t.TempDir()
	// Docker Desktop shares only some host folders with its VM; file mounts
	// need the data directory to be one of them.
	data := filepath.Join(dir, "data")
	bin := filepath.Join(dir, "musdash")
	build := exec.Command("go", "build", "-o", bin, "../cmd/musdash")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	serverAddr, proxyAddr := freeAddr(t), freeAddr(t)
	start(t, bin, "server", "-data", data, "-listen", serverAddr)
	base := "http://" + serverAddr
	waitReady(t, base+"/healthz")
	start(t, bin, "proxy", "-data", data, "-http", proxyAddr, "-https=")
	waitReady(t, "http://"+proxyAddr+"/")

	jar, _ := cookiejar.New(nil)
	ui := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	csrfRE := regexp.MustCompile(`name="_csrf" value="([^"]+)"`)
	token := func(path string) string {
		t.Helper()
		m := csrfRE.FindStringSubmatch(fetch(t, ui, base+path))
		if m == nil {
			t.Fatalf("no CSRF token on %s", path)
		}
		return m[1]
	}
	post := func(tokenPage, path string, form url.Values) *http.Response {
		t.Helper()
		form.Set("_csrf", token(tokenPage))
		res, err := ui.PostForm(base+path, form)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res
	}

	post("/setup", "/setup", url.Values{"name": {"E2E"}, "email": {"e2e@example.com"}, "password": {"an end to end test"}})
	res := post("/projects/new", "/projects", url.Values{"name": {"E2E"}})
	projectPath := res.Request.URL.Path // followed the redirect to /projects/<id>
	newAppPage := fetch(t, ui, base+projectPath)
	envID := regexp.MustCompile(`apps/new\?env=([a-z2-7]+)`).FindStringSubmatch(newAppPage)
	if envID == nil {
		t.Fatalf("no New app link on %s", projectPath)
	}

	const host = "e2e.127.0.0.1.sslip.io"
	res = post(projectPath+"/apps/new?env="+envID[1], projectPath+"/apps", url.Values{
		"env": {envID[1]}, "name": {"web"}, "image": {"nginx:alpine"}, "port": {"80"}, "domain": {host}, "deploy": {"1"},
	})
	deployPath := res.Request.URL.Path // /apps/<id>/deployments/<dep>
	appPath := strings.Split(deployPath, "/deployments/")[0]
	if !strings.HasPrefix(appPath, "/apps/") {
		t.Fatalf("unexpected redirect after creating the app: %s", deployPath)
	}
	// Everything below is scoped to this test's own app and environment, so
	// containers of another musdash on this machine are never touched.
	mine := "label=musdash.resource=" + strings.TrimPrefix(appPath, "/apps/")
	t.Cleanup(func() { removeManaged(t, mine, "musdash-"+envID[1]) })

	waitDeployment := func(path string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute) // the first run pulls the image
		for time.Now().Before(deadline) {
			status := fetch(t, ui, base+path+"/status")
			switch {
			case strings.Contains(status, "Succeeded"):
				return
			case strings.Contains(status, "Failed"):
				t.Fatalf("deployment failed:\n%s", fetch(t, ui, base+path+"/stream"))
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatal("deployment did not finish in time")
	}
	waitDeployment(deployPath)

	// Through the proxy, by host name.
	viaProxy := func() (int, string, error) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/", nil)
		req.Host = host
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return 0, "", err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		return res.StatusCode, string(body), err
	}
	code, body, err := viaProxy()
	if err != nil || code != 200 || !strings.Contains(body, "Welcome to nginx") {
		t.Fatalf("through the proxy: %d %v\n%s", code, err, body)
	}
	t.Logf("nginx answered through the proxy on %s", host)

	// The container is published on loopback only.
	out, _ := exec.Command("docker", "ps", "--filter", mine, "--format", "{{.Ports}}").Output()
	if ports := strings.TrimSpace(string(out)); !strings.HasPrefix(ports, "127.0.0.1:") {
		t.Fatalf("container ports: %q, want a 127.0.0.1 binding only", ports)
	}

	// An app whose port setting is wrong must fail its deploy. Docker's own
	// port proxy accepts connections even when nothing listens inside the
	// container, so a check that only connects would wrongly pass.
	res = post(projectPath+"/apps/new?env="+envID[1], projectPath+"/apps", url.Values{
		"env": {envID[1]}, "name": {"wrongport"}, "image": {"nginx:alpine"}, "port": {"8080"},
	})
	wrongPath := res.Request.URL.Path
	wrong := "label=musdash.resource=" + strings.TrimPrefix(wrongPath, "/apps/")
	t.Cleanup(func() { removeManaged(t, wrong, "musdash-"+envID[1]) })
	post(wrongPath+"/settings", wrongPath+"/settings", url.Values{"name": {"wrongport"}, "image": {"nginx:alpine"}, "port": {"8080"}, "health_timeout": {"5"}})
	res = post(wrongPath, wrongPath+"/deploy", url.Values{})
	wrongDeploy := res.Request.URL.Path
	deadline := time.Now().Add(time.Minute)
	for {
		status := fetch(t, ui, base+wrongDeploy+"/status")
		if strings.Contains(status, "Failed") {
			break
		}
		if strings.Contains(status, "Succeeded") {
			t.Fatal("an app listening on the wrong port was reported healthy")
		}
		if time.Now().After(deadline) {
			t.Fatal("the wrong-port deployment did not finish")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if log := fetch(t, ui, base+wrongDeploy+"/stream"); !strings.Contains(log, "nothing is listening") {
		t.Fatalf("the log does not explain the failure:\n%s", log)
	}
	out, _ = exec.Command("docker", "ps", "--all", "--quiet", "--filter", wrong).Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatal("the failed container was left behind")
	}
	t.Log("a deploy with the wrong port failed and cleaned up")

	// Redeploy while a client keeps sending requests; none may fail.
	ctx, stopLoad := context.WithCancel(context.Background())
	var sent, failed atomic.Int64
	var firstFailure atomic.Value
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				code, _, err := viaProxy()
				sent.Add(1)
				if err != nil || code != 200 {
					failed.Add(1)
					firstFailure.CompareAndSwap(nil, fmt.Sprintf("status %d, err %v", code, err))
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	res = post(appPath, appPath+"/deploy", url.Values{})
	waitDeployment(res.Request.URL.Path)
	// Keep the load on through the old container's drain and stop.
	time.Sleep(6 * time.Second)
	stopLoad()
	wg.Wait()
	t.Logf("redeploy under load: %d requests, %d failed", sent.Load(), failed.Load())
	if failed.Load() != 0 {
		t.Fatalf("%d of %d requests failed during the redeploy; first: %v", failed.Load(), sent.Load(), firstFailure.Load())
	}
	if sent.Load() < 100 {
		t.Fatalf("only %d requests were sent; the load was too thin to prove anything", sent.Load())
	}

	// Exactly one container remains: the old one was removed.
	out, _ = exec.Command("docker", "ps", "--all", "--filter", mine, "--format", "{{.Names}}").Output()
	if names := strings.Fields(string(out)); len(names) != 1 {
		t.Fatalf("containers after the redeploy: %v, want exactly one", names)
	}

	// Runtime logs reach the browser.
	logClient := &http.Client{Jar: jar}
	logCtx, cancelLog := context.WithTimeout(context.Background(), 4*time.Second)
	req, _ := http.NewRequestWithContext(logCtx, http.MethodGet, base+appPath+"/logs/stream", nil)
	logRes, err := logClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := io.ReadAtLeast(logRes.Body, buf, 64)
	logRes.Body.Close()
	cancelLog()
	if !strings.Contains(string(buf[:n]), "event: line") {
		t.Fatalf("no log lines in the stream: %q", buf[:n])
	}

	// Stop: the route goes and the container with it.
	post(appPath, appPath+"/stop", url.Values{})
	code, body, _ = viaProxy()
	if code != http.StatusNotFound || !strings.Contains(body, "Nothing is deployed") {
		t.Fatalf("after stop: %d %s", code, body)
	}
	out, _ = exec.Command("docker", "ps", "--all", "--quiet", "--filter", mine).Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatal("a container survived the stop")
	}
}

// removeManaged deletes the containers matching the label filter and the
// named network.
func removeManaged(t *testing.T, filter, network string) {
	out, err := exec.Command("docker", "ps", "--all", "--quiet", "--filter", filter).Output()
	if err == nil {
		for _, id := range strings.Fields(string(out)) {
			if out, err := exec.Command("docker", "rm", "--force", id).CombinedOutput(); err != nil {
				t.Logf("cleanup container %s: %v %s", id, err, out)
			}
		}
	}
	exec.Command("docker", "network", "rm", network).Run()
}
