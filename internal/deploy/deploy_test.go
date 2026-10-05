package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
	"golang.org/x/crypto/ssh"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

// fixedRunners hands the same fake to every server.
type fixedRunners struct{ r runner.Runner }

func (f fixedRunners) Runner(context.Context, db.Server) (runner.Runner, error) { return f.r, nil }

// stubProbe answers health probes from a function.
type stubProbe struct {
	mu sync.Mutex
	fn func() error
}

func (p *stubProbe) result() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fn == nil {
		return nil
	}
	return p.fn()
}
func (p *stubProbe) HTTP(context.Context, int, string) error { return p.result() }
func (p *stubProbe) TCP(context.Context, int) error          { return p.result() }

type env struct {
	t      *testing.T
	db     *db.DB
	d      *Deployer
	fake   *runnertest.Fake
	probe  *stubProbe
	cfg    *config.Config
	team   string
	server db.Server
	app    db.App
}

// running is what `docker inspect` reports for a healthy container.
const running = `{"Status":"running","Running":true,"ExitCode":0}`

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_, team, err := d.CreateFirstUser(ctx, "o@example.com", "O", "hash")
	if err != nil {
		t.Fatal(err)
	}
	server, err := d.EnsureLocalServer(ctx, team, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	project, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, project.ID)
	app, err := d.CreateApp(ctx, team, db.App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx:alpine", Port: 80, HealthTimeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddDomain(ctx, team, server.ID, db.Domain{ResourceKind: db.KindApp, ResourceID: app.ID, Host: "shop.example.com", TLS: true, RedirectWWW: true}); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, _ := secret.New(secret.RandomBytes(secret.KeySize))
	fake := &runnertest.Fake{Handle: func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker inspect") {
			return running, nil
		}
		return "", nil
	}}
	fake.PutFile(cfg.ProxyPIDPath(), "4242\n")
	probe := &stubProbe{}
	q := jobs.New(d.DB, log, 2)
	dep := New(d, box, q, fixedRunners{fake}, cfg, log, "127.0.0.1:8000")
	// No test writes to GitHub: the ones about comments put their own in.
	dep.Comments = nil
	dep.Probe = probe
	dep.healthEvery = 10 * time.Millisecond
	dep.drain = 0
	dep.pollWait = 20 * time.Millisecond
	dep.Register()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		q.Stop(stop)
	})
	return &env{t: t, db: d, d: dep, fake: fake, probe: probe, cfg: cfg, team: team, server: server, app: app}
}

// deploy queues a deployment and waits for it to finish.
func (e *env) deploy() db.Deployment {
	e.t.Helper()
	return e.deployWithin(15 * time.Second)
}

func (e *env) deployWithin(limit time.Duration) db.Deployment {
	e.t.Helper()
	ctx := context.Background()
	app, err := e.db.AppByID(ctx, e.app.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	dep, err := e.d.Enqueue(ctx, app, "manual")
	if err != nil {
		e.t.Fatal(err)
	}
	return e.wait(dep, limit)
}

// wait waits for a queued deployment to finish.
func (e *env) wait(dep db.Deployment, limit time.Duration) db.Deployment {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		got, _ := e.db.DeploymentByID(context.Background(), dep.ID)
		if got.Status == db.DeploySuccess || got.Status == db.DeployFailed {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("deployment did not finish")
	return dep
}

// rollback queues a rollback to an earlier deployment and waits for it.
func (e *env) rollback(to db.Deployment) db.Deployment {
	e.t.Helper()
	dep, err := e.d.Rollback(context.Background(), e.reload(), to)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.wait(dep, 15*time.Second)
}

func (e *env) reload() db.App {
	e.t.Helper()
	app, err := e.db.AppByID(context.Background(), e.app.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return app
}

func (e *env) routes() proxy.File {
	e.t.Helper()
	raw, _, ok := e.fake.File(e.cfg.RoutesPath())
	if !ok {
		// A test against the real Docker writes it to disk.
		onDisk, err := os.ReadFile(e.cfg.RoutesPath())
		if err != nil {
			return proxy.File{}
		}
		raw = string(onDisk)
	}
	var f proxy.File
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) log(dep db.Deployment) string {
	raw, _ := os.ReadFile(e.cfg.DeployLogPath(dep.ID))
	return string(raw)
}

// indexOf returns the position of the first call with the prefix, or -1.
func indexOf(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestFirstDeploy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sealed, _ := e.d.Box.SealString("s3cret value")
	buildOnly, _ := e.d.Box.SealString("x")
	e.db.ReplaceEnvVars(ctx, db.KindApp, e.app.ID, []db.EnvVar{{Key: "API_KEY", Value: sealed}, {Key: "BUILD_ARG", Value: buildOnly, BuildTime: true}})
	e.db.AddStorage(ctx, db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageVolume, Source: "data", Target: "/data"})
	fileContent, _ := e.d.Box.SealString("server { listen 80; }")
	file, _ := e.db.AddStorage(ctx, db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageFile, Target: "/etc/nginx/conf.d/default.conf", Content: fileContent})

	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("deployment %s: %s\n%s", dep.Status, dep.Error, e.log(dep))
	}

	app := e.reload()
	container := ContainerName(app.ID, dep.ID)
	if app.Status != db.AppRunning || app.Container != container || app.DeployedImage != "nginx:alpine" {
		t.Fatalf("app after deploy: %+v", app)
	}
	if app.HostPort < portMin || app.HostPort > portMax {
		t.Fatalf("host port %d outside the range", app.HostPort)
	}

	calls := e.fake.Calls()
	order := []string{"docker pull nginx:alpine", "docker network inspect", "docker rm --force " + container, "docker run", "docker inspect", "kill -HUP 4242"}
	last := -1
	for _, prefix := range order {
		i := indexOf(calls, prefix)
		if i <= last {
			t.Fatalf("%q is missing or out of order in:\n%s", prefix, strings.Join(calls, "\n"))
		}
		last = i
	}

	run := calls[indexOf(calls, "docker run")]
	envPath := filepath.Join(e.cfg.AppDir(app.ID), "env")
	for _, want := range []string{
		"--name " + container,
		"--network " + NetworkName(app.EnvironmentID),
		"--network-alias web",
		"--publish 127.0.0.1:" + strconv.Itoa(app.HostPort) + ":80",
		"--env-file " + envPath,
		"--mount type=volume,source=" + VolumeName(app.ID, "data") + ",target=/data",
		"--mount type=bind,source=" + filepath.Join(e.cfg.AppDir(app.ID), "files", file.ID) + ",target=/etc/nginx/conf.d/default.conf",
		"--label musdash.resource=" + app.ID,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run is missing %q:\n%s", want, run)
		}
	}
	if strings.Contains(run, "s3cret") {
		t.Error("a secret value appeared on the docker command line")
	}

	body, mode, ok := e.fake.File(envPath)
	if !ok || body != "API_KEY=s3cret value\n" || mode != 0o600 {
		t.Errorf("env file: %q mode %o", body, mode)
	}
	if content, _, _ := e.fake.File(filepath.Join(e.cfg.AppDir(app.ID), "files", file.ID)); content != "server { listen 80; }" {
		t.Errorf("file mount content %q", content)
	}

	routes := e.routes().Routes
	want := []proxy.Route{
		{Host: "shop.example.com", Target: "127.0.0.1:" + strconv.Itoa(app.HostPort), TLS: true},
		{Host: "www.shop.example.com", RedirectTo: "shop.example.com", TLS: true},
	}
	if len(routes) != 2 || routes[0] != want[0] || routes[1] != want[1] {
		t.Fatalf("routes: %+v", routes)
	}
	if log := e.log(dep); !strings.Contains(log, "Pulling nginx:alpine") || !strings.Contains(log, "Deployed.") {
		t.Errorf("log:\n%s", log)
	}
}

func TestRedeploySwitchesThenStopsOld(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	before := e.reload()
	second := e.deploy()
	if second.Status != db.DeploySuccess {
		t.Fatalf("second deployment failed: %s", second.Error)
	}
	after := e.reload()
	oldC, newC := ContainerName(e.app.ID, first.ID), ContainerName(e.app.ID, second.ID)
	if after.Container != newC || after.HostPort == before.HostPort {
		t.Fatalf("container %s port %d (was %d)", after.Container, after.HostPort, before.HostPort)
	}

	calls := e.fake.Calls()
	// Find the second reload: traffic must move before the old container stops.
	hups := 0
	switchAt := -1
	for i, c := range calls {
		if strings.HasPrefix(c, "kill -HUP") {
			if hups++; hups == 2 {
				switchAt = i
			}
		}
	}
	stopAt := indexOf(calls, "docker stop --time 30 "+oldC)
	rmAt := -1
	for i := stopAt + 1; stopAt >= 0 && i < len(calls); i++ {
		if calls[i] == "docker rm --force "+oldC {
			rmAt = i
		}
	}
	if switchAt < 0 || stopAt < switchAt || rmAt < stopAt {
		t.Fatalf("order wrong: switch %d, stop old %d, remove old %d\n%s", switchAt, stopAt, rmAt, strings.Join(calls, "\n"))
	}
	if got := e.routes().Routes[0].Target; got != "127.0.0.1:"+strconv.Itoa(after.HostPort) {
		t.Fatalf("route still points at %s", got)
	}
}

func TestFailedPull(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker pull") {
			return "", runnertest.Exit("docker", 1, "manifest unknown")
		}
		return running, nil
	}
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "pull nginx:alpine") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if indexOf(e.fake.Calls(), "docker run") >= 0 {
		t.Fatal("a container was started after a failed pull")
	}
	if app := e.reload(); app.Status != db.AppFailed || app.Container != "" {
		t.Fatalf("app: %+v", app)
	}
	if !strings.Contains(e.log(dep), "Failed:") {
		t.Fatal("the log does not say the deployment failed")
	}
}

func TestFailedHealthCheckKeepsOldContainer(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	before := e.reload()
	routesBefore, _, _ := e.fake.File(e.cfg.RoutesPath())

	e.probe.mu.Lock()
	e.probe.fn = func() error { return errors.New("connection refused") }
	e.probe.mu.Unlock()
	second := e.deploy()
	if second.Status != db.DeployFailed || !strings.Contains(second.Error, "did not pass within 1s") {
		t.Fatalf("%s %q", second.Status, second.Error)
	}

	after := e.reload()
	if after.Container != before.Container || after.HostPort != before.HostPort || after.Status != db.AppRunning {
		t.Fatalf("the old container must keep serving: %+v", after)
	}
	routesAfter, _, _ := e.fake.File(e.cfg.RoutesPath())
	if routesAfter != routesBefore {
		t.Fatal("routes changed after a failed deploy")
	}
	calls := e.fake.Calls()
	newC := ContainerName(e.app.ID, second.ID)
	runAt := indexOf(calls, "docker run --detach --pull never --name "+newC)
	removed := false
	for _, c := range calls[runAt+1:] {
		if c == "docker rm --force "+newC {
			removed = true
		}
		if strings.Contains(c, ContainerName(e.app.ID, first.ID)) && (strings.HasPrefix(c, "docker stop") || strings.HasPrefix(c, "docker rm")) {
			t.Fatalf("the old container was touched: %s", c)
		}
	}
	if !removed {
		t.Fatal("the failed container was left behind")
	}
}

func TestContainerExitsBeforeHealthy(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker inspect"):
			return `{"Status":"exited","Running":false,"ExitCode":2}`, nil
		case strings.HasPrefix(line, "docker logs"):
			return "panic: missing DATABASE_URL\n", nil
		}
		return "", nil
	}
	started := time.Now()
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "exited with status 2") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if time.Since(started) > 900*time.Millisecond {
		t.Fatal("an exited container must fail the deploy at once, not after the timeout")
	}
	if !strings.Contains(e.log(dep), "panic: missing DATABASE_URL") {
		t.Fatalf("the container's output is not in the log:\n%s", e.log(dep))
	}
}

func TestPortCollisionRetries(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var ports []string
	portRE := regexp.MustCompile(`--publish 127\.0\.0\.1:(\d+):80`)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker run") {
			mu.Lock()
			defer mu.Unlock()
			ports = append(ports, portRE.FindStringSubmatch(line)[1])
			if len(ports) == 1 {
				return "", runnertest.Exit("docker", 125, "Bind for 127.0.0.1:20000 failed: port is already allocated")
			}
		}
		return running, nil
	}
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if len(ports) != 2 || ports[0] == ports[1] {
		t.Fatalf("ports tried: %v", ports)
	}
	if got := strconv.Itoa(e.reload().HostPort); got != ports[1] {
		t.Fatalf("recorded port %s, container runs on %s", got, ports[1])
	}
}

func TestStopRemovesRouteFirst(t *testing.T) {
	e := newEnv(t)
	dep := e.deploy()
	app := e.reload()
	if err := e.d.Stop(context.Background(), app.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.reload(); got.Status != db.AppStopped || got.Container != "" || got.HostPort != 0 {
		t.Fatalf("app after stop: %+v", got)
	}
	if n := len(e.routes().Routes); n != 0 {
		t.Fatalf("%d routes left after stop", n)
	}
	calls := e.fake.Calls()
	container := ContainerName(app.ID, dep.ID)
	hup, stop := -1, indexOf(calls, "docker stop --time 30 "+container)
	for i, c := range calls {
		if strings.HasPrefix(c, "kill -HUP") {
			hup = i
		}
	}
	if stop < 0 || hup > stop || hup < 0 {
		t.Fatalf("the route must go before the container: reload %d, stop %d", hup, stop)
	}
}

func TestDestroyKeepsNothingButVolumes(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	app := e.reload()
	if err := e.d.Destroy(context.Background(), app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.cfg.DeployLogPath(first.ID)); err == nil {
		t.Fatal("a deployment log outlived its app")
	}
	if _, err := e.db.AppByID(context.Background(), app.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("app row still exists: %v", err)
	}
	if doms, _ := e.db.ListDomains(context.Background(), db.KindApp, app.ID); len(doms) != 0 {
		t.Fatal("domains were left behind")
	}
	if _, _, ok := e.fake.File(filepath.Join(e.cfg.AppDir(app.ID), "env")); ok {
		t.Fatal("the env file was left on the server")
	}
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "docker volume rm") {
			t.Fatal("a data volume was deleted")
		}
	}
}

func TestProxyDownDoesNotFailTheDeploy(t *testing.T) {
	e := newEnv(t)
	e.fake.RemoveAll(context.Background(), e.cfg.ProxyPIDPath())
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if !strings.Contains(e.log(dep), "the proxy is not running") {
		t.Fatalf("the log must say the proxy is down:\n%s", e.log(dep))
	}
	if len(e.routes().Routes) == 0 {
		t.Fatal("routes must still be written, ready for when the proxy starts")
	}
}

func TestDeploysOfOneAppShareALock(t *testing.T) {
	e := newEnv(t)
	e.deploy()
	var key string
	e.db.QueryRow(`SELECT lock_key FROM jobs WHERE kind = 'deploy' LIMIT 1`).Scan(&key)
	if key != "deploy:"+e.app.ID {
		t.Fatalf("lock key %q", key)
	}
}

func TestEnvFileRejectsLineBreaks(t *testing.T) {
	e := newEnv(t)
	sealed, _ := e.d.Box.SealString("line1\nINJECTED=1")
	e.db.ReplaceEnvVars(context.Background(), db.KindApp, e.app.ID, []db.EnvVar{{Key: "KEY", Value: sealed}})
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "line break") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
}

func TestHealthURLStaysOnLoopback(t *testing.T) {
	good := map[string]string{
		"/":                 "http://127.0.0.1:20417/",
		"/healthz":          "http://127.0.0.1:20417/healthz",
		"/api/ready?deep=1": "http://127.0.0.1:20417/api/ready?deep=1",
		"/a%20b":            "http://127.0.0.1:20417/a%20b",
	}
	for path, want := range good {
		if got, err := HealthURL(20417, path); err != nil || got != want {
			t.Errorf("HealthURL(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	// Each of these, glued after "http://127.0.0.1:<port>", would point the
	// request at another host.
	for _, path := range []string{"@evil.example.com/", "//evil.example.com/", "", "healthz", ":80@evil.example.com/", "/a b", "/a\nHost: evil", "http://evil.example.com/", "\\evil"} {
		if got, err := HealthURL(20417, path); err == nil {
			t.Errorf("HealthURL(%q) accepted: %s", path, got)
		}
	}
}

func TestBindMountOfProtectedPathFailsTheDeploy(t *testing.T) {
	e := newEnv(t)
	e.db.AddStorage(context.Background(), db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageBind, Source: e.cfg.MasterKeyPath(), Target: "/key"})
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "cannot be mounted") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if indexOf(e.fake.Calls(), "docker run") >= 0 {
		t.Fatal("a container was started with the master key mounted")
	}
}

func TestParseEnv(t *testing.T) {
	got, err := ParseEnv("# comment\n\nB=2\nA = spaced value \nexport C=\"quoted # not a comment\"\nD='single'\nE=\nB=override\nURL=postgres://u:p@h/db?x=1\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "spaced value", "B": "override", "C": "quoted # not a comment", "D": "single", "E": "", "URL": "postgres://u:p@h/db?x=1"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, v := range got {
		if want[v.Key] != v.Value {
			t.Errorf("%s = %q, want %q", v.Key, v.Value, want[v.Key])
		}
		if i > 0 && got[i-1].Key >= v.Key {
			t.Error("variables are not sorted by key")
		}
	}
	for _, bad := range []string{"NOEQUALS", "1BAD=x", "BAD-KEY=x", "=x", "A B=x"} {
		if _, err := ParseEnv(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: want an error naming line 1, got %v", bad, err)
		}
	}
	if out := FormatEnv(got); !strings.HasPrefix(out, "A=spaced value\nB=override\n") {
		t.Errorf("FormatEnv: %q", out)
	}
}

func TestBuildRoutes(t *testing.T) {
	rows := []db.RouteRow{
		{Host: "example.com", TLS: true, RedirectWWW: true, HostPort: 20001},
		{Host: "www.other.com", TLS: true, RedirectWWW: true, HostPort: 20002},
		{Host: "www.example.com", TLS: true, HostPort: 20003}, // already routed: no redirect over it
		{Host: "abc.203.0.113.7.sslip.io", HostPort: 20004},
	}
	file, skipped := BuildRoutes(rows, "ops@example.com", "dash.example.com", "127.0.0.1:8000")
	if len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	byHost := map[string]proxy.Route{}
	for _, r := range file.Routes {
		if _, dup := byHost[r.Host]; dup {
			t.Fatalf("host %s routed twice", r.Host)
		}
		byHost[r.Host] = r
	}
	checks := map[string]proxy.Route{
		"dash.example.com":         {Host: "dash.example.com", Target: "127.0.0.1:8000", TLS: true},
		"example.com":              {Host: "example.com", Target: "127.0.0.1:20001", TLS: true},
		"www.example.com":          {Host: "www.example.com", Target: "127.0.0.1:20003", TLS: true},
		"www.other.com":            {Host: "www.other.com", Target: "127.0.0.1:20002", TLS: true},
		"other.com":                {Host: "other.com", RedirectTo: "www.other.com", TLS: true},
		"abc.203.0.113.7.sslip.io": {Host: "abc.203.0.113.7.sslip.io", Target: "127.0.0.1:20004"},
	}
	if len(byHost) != len(checks) {
		t.Fatalf("routes: %+v", file.Routes)
	}
	for host, want := range checks {
		if byHost[host] != want {
			t.Errorf("%s: %+v, want %+v", host, byHost[host], want)
		}
	}
	raw, _ := json.Marshal(file)
	if _, err := proxy.Parse(bytes.NewReader(raw)); err != nil {
		t.Fatalf("the proxy rejects the generated file: %v", err)
	}
	// An empty server still produces a valid file with an empty list.
	empty, _ := BuildRoutes(nil, "", "", "")
	raw, _ = json.Marshal(empty)
	if string(raw) != `{"routes":[]}` {
		t.Fatalf("empty file: %s", raw)
	}
}

func TestBuildRoutesByPath(t *testing.T) {
	rows := []db.RouteRow{
		{Host: "example.com", TLS: true, RedirectWWW: true, HostPort: 20001},
		{Host: "example.com", Path: "/api", StripPrefix: true, TLS: true, RedirectWWW: true, AuthUser: "ada", AuthHash: "$2a$04$abcdefghijklmnopqrstuuJ8nZ0mK8zXc5l1o9vQ2r3s4t5u6v7w8", HostPort: 20002},
		// Without a path there is nothing to take off.
		{Host: "plain.example.com", StripPrefix: true, HostPort: 20003},
		{Host: "bad.example.com", Path: "/a/../b", HostPort: 20004},
	}
	file, skipped := BuildRoutes(rows, "", "", "")
	if len(skipped) != 1 || skipped[0] != "bad.example.com/a/../b" {
		t.Fatalf("skipped %v", skipped)
	}
	got := map[string]proxy.Route{}
	for _, r := range append(append([]proxy.Route{}, file.Routes...), file.Guarded...) {
		if _, dup := got[r.Host+r.Path]; dup {
			t.Fatalf("%s%s routed twice", r.Host, r.Path)
		}
		got[r.Host+r.Path] = r
	}
	// A proxy from before paths and passwords reads "routes" and nothing
	// else. It must not learn of a host that has either, not even of that
	// host's open route: it would serve the whole host through it.
	for _, r := range file.Routes {
		if r.Host == "example.com" {
			t.Fatalf("an older proxy would serve %s%s, without the path or the password", r.Host, r.Path)
		}
	}
	if len(file.Guarded) != 2 || len(file.Routes) != 2 {
		t.Fatalf("%d routes for every proxy, %d for ones that know paths and passwords", len(file.Routes), len(file.Guarded))
	}
	raw, _ := json.Marshal(file)
	var old struct {
		Routes []struct {
			Host, Target string
		} `json:"routes"`
	}
	json.Unmarshal(raw, &old)
	for _, r := range old.Routes {
		if r.Host == "example.com" && r.Target != "" {
			t.Fatal("the file as an older proxy reads it routes the guarded host")
		}
	}
	if tab, err := proxy.Parse(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	} else if rt, ok := tab.Lookup("example.com", "/api/x"); !ok || rt.AuthUser != "ada" {
		t.Fatalf("this proxy does not read the guarded host: %+v", rt)
	}
	want := map[string]proxy.Route{
		"example.com":       {Host: "example.com", Target: "127.0.0.1:20001", TLS: true},
		"example.com/api":   {Host: "example.com", Path: "/api", StripPrefix: true, Target: "127.0.0.1:20002", TLS: true, AuthUser: "ada", AuthHash: rows[1].AuthHash},
		"www.example.com":   {Host: "www.example.com", RedirectTo: "example.com", TLS: true}, // once, for the host
		"plain.example.com": {Host: "plain.example.com", Target: "127.0.0.1:20003"},
	}
	if len(got) != len(want) {
		t.Fatalf("routes: %+v", file.Routes)
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("%s: %+v, want %+v", key, got[key], w)
		}
	}
}

func TestBuildRoutesSkipsWhatTheProxyWouldRefuse(t *testing.T) {
	// 250 characters is a valid host; with "www." in front it is not.
	long := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example"
	if len(long) != 251 || !proxy.ValidHost(long) {
		t.Fatalf("test host is %d characters, valid=%v", len(long), proxy.ValidHost(long))
	}
	rows := []db.RouteRow{
		{Host: long, RedirectWWW: true, HostPort: 20001},
		{Host: "bad_host.example.com", HostPort: 20002},
		{Host: "good.example.com", HostPort: 20003},
	}
	file, skipped := BuildRoutes(rows, "", "", "")
	if len(skipped) != 2 || skipped[0] != "www."+long || skipped[1] != "bad_host.example.com" {
		t.Fatalf("skipped %v", skipped)
	}
	raw, _ := json.Marshal(file)
	tab, err := proxy.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("one bad domain broke the whole file: %v", err)
	}
	if _, ok := tab.Lookup("good.example.com", "/"); !ok || tab.Len() != 2 {
		t.Fatalf("the good routes were not published: %d routes", tab.Len())
	}
}

func TestFailedSwitchRollsBackAndKeepsOldContainer(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	before := e.reload()
	e.fake.FailWrite = func(path string) error {
		if path == e.cfg.RoutesPath() {
			return errors.New("disk full")
		}
		return nil
	}
	second := e.deploy()
	if second.Status != db.DeployFailed || !strings.Contains(second.Error, "publish the new routes") {
		t.Fatalf("%s %q", second.Status, second.Error)
	}
	after := e.reload()
	if after.Container != before.Container || after.HostPort != before.HostPort || after.Status != db.AppRunning {
		t.Fatalf("the app must be back on its previous container: %+v", after)
	}
	oldC, newC := ContainerName(e.app.ID, first.ID), ContainerName(e.app.ID, second.ID)
	removedNew := false
	for _, c := range e.fake.Calls() {
		if c == "docker stop --time 30 "+oldC {
			t.Fatal("the previous container was stopped although traffic never moved")
		}
		if c == "docker rm --force "+newC {
			removedNew = true
		}
	}
	if !removedNew {
		t.Fatal("the new container was left running")
	}
}

func TestProxyThatRefusesTheSignalStillGetsTheRoutes(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "kill -HUP") {
			return "", runnertest.Exit("kill", 1, "kill: (4242) - Operation not permitted")
		}
		return running, nil
	}
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if len(e.routes().Routes) == 0 {
		t.Fatal("routes were not written")
	}
	if strings.Contains(e.log(dep), "proxy is not running") {
		t.Fatal("a live proxy that refused the signal was reported as not running")
	}
}

func TestStalePIDFileIsNotSignalled(t *testing.T) {
	e := newEnv(t)
	// The pid in the file now belongs to some other program.
	e.fake.PutFile("/proc/4242/comm", "postgres\n")
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if indexOf(e.fake.Calls(), "kill -HUP") >= 0 {
		t.Fatal("SIGHUP was sent to a process that is not musdash")
	}
	e.fake.PutFile("/proc/4242/comm", "musdash-linux-a\n")
	e.deploy()
	if indexOf(e.fake.Calls(), "kill -HUP 4242") < 0 {
		t.Fatal("the real proxy was not signalled")
	}
}

func TestStopThatFailsCanBeRunAgain(t *testing.T) {
	e := newEnv(t)
	dep := e.deploy()
	container := ContainerName(e.app.ID, dep.ID)
	failing := true
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker stop") && failing {
			return "", runnertest.Exit("docker", 1, "Cannot connect to the Docker daemon")
		}
		return running, nil
	}
	if err := e.d.Stop(context.Background(), e.app.ID); err == nil {
		t.Fatal("want an error")
	}
	// The route is gone, but the container is still remembered.
	if got := e.reload(); got.Container != container || got.Status != db.AppStopped || got.HostPort != 0 {
		t.Fatalf("after a failed stop: %+v", got)
	}
	if len(e.routes().Routes) != 0 {
		t.Fatal("the route must be withdrawn even though the container could not be stopped")
	}
	// A "die" event for a stopped app must not flip it to "exited".
	e.d.applyEvent(context.Background(), e.server.ID, docker.Client{R: e.fake}, []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"app","musdash.resource":"`+e.app.ID+`","name":"`+container+`"}}}`))
	if got := e.reload().Status; got != db.AppStopped {
		t.Fatalf("status %s", got)
	}
	failing = false
	if err := e.d.Stop(context.Background(), e.app.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.reload(); got.Container != "" {
		t.Fatalf("container still recorded: %+v", got)
	}
}

func TestStopAndDestroyAreRefusedDuringADeploy(t *testing.T) {
	e := newEnv(t)
	e.deploy()
	// Hold the next deployment in its health check.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	e.probe.mu.Lock()
	e.probe.fn = func() error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		default:
			return errors.New("not yet")
		}
	}
	e.probe.mu.Unlock()
	ctx := context.Background()
	e.db.Exec(`UPDATE apps SET health_timeout = 30 WHERE id = ?`, e.app.ID)
	app := e.reload()
	dep, err := e.d.Enqueue(ctx, app, "manual")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := e.d.Stop(ctx, e.app.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Stop during a deploy: %v", err)
	}
	if err := e.d.Destroy(ctx, e.app.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Destroy during a deploy: %v", err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := e.db.DeploymentByID(ctx, dep.ID); got.Status == db.DeploySuccess {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := e.d.Stop(ctx, e.app.ID); err != nil {
		t.Fatalf("Stop after the deploy: %v", err)
	}
}

func TestDeployOfDeletedAppLeavesNoContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// The app is deleted while its deployment waits for the health check.
	deleted := false
	e.probe.mu.Lock()
	e.probe.fn = func() error {
		if !deleted {
			deleted = true
			e.db.DeleteApp(ctx, e.app.ID)
		}
		return nil
	}
	e.probe.mu.Unlock()
	app, _ := e.db.AppByID(ctx, e.app.ID)
	dep, err := e.d.Enqueue(ctx, app, "manual")
	if err != nil {
		t.Fatal(err)
	}
	container := ContainerName(e.app.ID, dep.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		calls := e.fake.Calls()
		run := indexOf(calls, "docker run")
		if run >= 0 {
			for _, c := range calls[run+1:] {
				if c == "docker rm --force "+container {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the container of a deleted app was left running")
}

func TestReconcileRemovesOrphans(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dep := e.deploy()
	current := ContainerName(e.app.ID, dep.ID)
	// A deployment that is still running elsewhere in the queue.
	active, _ := e.db.CreateDeployment(ctx, db.Deployment{AppID: e.app.ID, Image: "nginx"})
	e.db.StartDeployment(ctx, active.ID)
	// One that finished long ago, and one for an app that no longer exists.
	old, _ := e.db.CreateDeployment(ctx, db.Deployment{AppID: e.app.ID, Image: "nginx"})
	e.db.Exec(`UPDATE deployments SET status = 'success', finished_at = 100 WHERE id = ?`, old.ID)
	// One that succeeded a moment ago and has been replaced since: its
	// container may still be draining. And one that failed a moment ago:
	// nothing of it is wanted, however lately it failed.
	draining, _ := e.db.CreateDeployment(ctx, db.Deployment{AppID: e.app.ID, Image: "nginx"})
	e.db.Exec(`UPDATE deployments SET status = 'success', finished_at = ? WHERE id = ?`, time.Now().Unix()-1, draining.ID)
	failed, _ := e.db.CreateDeployment(ctx, db.Deployment{AppID: e.app.ID, Image: "nginx"})
	e.db.Exec(`UPDATE deployments SET status = 'failed', finished_at = ? WHERE id = ?`, time.Now().Unix()-1, failed.ID)

	rows := []string{
		current + "\trunning\tapp\t" + e.app.ID + "\t" + dep.ID,
		"musdash-" + e.app.ID + "-" + active.ID + "\trunning\tapp\t" + e.app.ID + "\t" + active.ID,
		"musdash-" + e.app.ID + "-" + old.ID + "\trunning\tapp\t" + e.app.ID + "\t" + old.ID,
		"musdash-" + e.app.ID + "-" + draining.ID + "\trunning\tapp\t" + e.app.ID + "\t" + draining.ID,
		"musdash-" + e.app.ID + "-" + failed.ID + "\trunning\tapp\t" + e.app.ID + "\t" + failed.ID,
		"musdash-gone-xyz\texited\tapp\tgone\txyz",
		"musdash-db1-abc\trunning\tdatabase\tdb1\tabc",
	}
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return strings.Join(rows, "\n") + "\n", nil
		}
		return running, nil
	}
	r, _ := e.d.Runners.Runner(ctx, e.server)
	before := len(e.fake.Calls())
	if err := e.d.Reconcile(ctx, e.server, dockerClient(r)); err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, c := range e.fake.Calls()[before:] {
		if name, ok := strings.CutPrefix(c, "docker rm --force "); ok {
			removed = append(removed, name)
		}
	}
	want := []string{"musdash-" + e.app.ID + "-" + old.ID, "musdash-" + e.app.ID + "-" + failed.ID, "musdash-gone-xyz"}
	if !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v (the serving container, the containers of an active deployment and of one that just succeeded, and other kinds must stay)", removed, want)
	}
}

// The dashboard is killed while a deployment waits for its new container's
// health check. A deploy job runs once, so at the next start the job is
// failed, then its deployment, and then the container it had started must
// go: nothing else would ever remove it, and the app would have two.
func TestContainerOfADeploymentTheProcessDiedInIsRemoved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	serving := ContainerName(e.app.ID, e.deploy().ID)

	// What the dead process left: its job and its deployment still marked
	// as running, and the container it had started.
	dead, _ := e.db.CreateDeployment(ctx, db.Deployment{AppID: e.app.ID, Image: "nginx"})
	e.db.StartDeployment(ctx, dead.ID)
	e.db.SetAppStatus(ctx, e.app.ID, db.AppDeploying)
	e.db.Exec(`INSERT INTO jobs (id, kind, payload, status, attempts, max_attempts, lock_key, run_after, created_at, started_at)
		VALUES ('deadjob', ?, ?, 'running', 1, 1, ?, 0, 1, 1)`, JobDeploy, `{"deployment_id":"`+dead.ID+`"}`, "app:"+e.app.ID)
	left := ContainerName(e.app.ID, dead.ID)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return serving + "\trunning\tapp\t" + e.app.ID + "\t" + strings.TrimPrefix(serving, "musdash-"+e.app.ID+"-") + "\n" +
				left + "\trunning\tapp\t" + e.app.ID + "\t" + dead.ID + "\n", nil
		}
		return running, nil
	}

	// What start-up does, in its order (cmd/musdash): the queue fails the
	// job that has used its one attempt, the deployments without a job are
	// failed, the app is no longer "deploying", and the monitor's first act
	// is to reconcile.
	q := jobs.New(e.db.DB, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stop, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	q.Stop(stop)
	var jobStatus string
	e.db.QueryRow(`SELECT status FROM jobs WHERE id = 'deadjob'`).Scan(&jobStatus)
	if jobStatus != jobs.StatusFailed {
		t.Fatalf("the dead process's job is %q, want it failed and not run again", jobStatus)
	}
	if err := e.db.FailStaleDeployments(ctx, "musdash stopped before this deployment ran"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.DeploymentByID(ctx, dead.ID); got.Status != db.DeployFailed {
		t.Fatalf("the deployment is %q", got.Status)
	}
	if err := e.db.ResetStuckDeploying(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := e.d.Runners.Runner(ctx, e.server)
	before := len(e.fake.Calls())
	if err := e.d.Reconcile(ctx, e.server, dockerClient(r)); err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, c := range e.fake.Calls()[before:] {
		if name, ok := strings.CutPrefix(c, "docker rm --force "); ok {
			removed = append(removed, name)
		}
	}
	if !slices.Equal(removed, []string{left}) {
		t.Fatalf("removed %v, want only %s: the container the dead process had started", removed, left)
	}
	if app := e.reload(); app.Container != serving || app.Status != db.AppRunning {
		t.Fatalf("the app: %s %s", app.Status, app.Container)
	}
}

func TestRestartingContainerFailsTheDeployAtOnce(t *testing.T) {
	e := newEnv(t)
	e.db.Exec(`UPDATE apps SET health_timeout = 30 WHERE id = ?`, e.app.ID)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker inspect") {
			// Docker reports a crash-looping container as running.
			return `{"Status":"restarting","Running":true,"ExitCode":1}`, nil
		}
		return "", nil
	}
	started := time.Now()
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "exited with status 1") || time.Since(started) > 3*time.Second {
		t.Fatalf("%s %q after %v", dep.Status, dep.Error, time.Since(started))
	}
}

func TestTCPProbeNeedsARealListener(t *testing.T) {
	t.Run("local", func(t *testing.T) { tcpProbe(t, newLocalProbe()) })
	// Through a server's SSH connection, where a connection has no read
	// deadline: the check must still end by itself.
	t.Run("over ssh", func(t *testing.T) {
		srv := sshtest.Start(t)
		r, err := runner.DialSSH(context.Background(), runner.SSHConfig{Host: srv.Host, Port: srv.Port, User: "deploy",
			Signer: srv.Signer, HostKey: srv.HostKey.Marshal(), WorkDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		tcpProbe(t, newDialProbe(r.Dial))
	})
}

func tcpProbe(t *testing.T, probe Probe) {
	ctx := context.Background()
	serve := func(handle func(net.Conn)) int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go handle(c)
			}
		}()
		return ln.Addr().(*net.TCPAddr).Port
	}
	// What Docker's port proxy does when nothing listens in the container:
	// accept, then close.
	closesAtOnce := serve(func(c net.Conn) { c.Close() })
	// A server waiting for a request, such as an HTTP server.
	waits := serve(func(c net.Conn) { time.Sleep(2 * time.Second); c.Close() })
	// A server that speaks first, such as MySQL or SMTP.
	greets := serve(func(c net.Conn) { c.Write([]byte("220 ready\r\n")); time.Sleep(time.Second); c.Close() })

	if err := probe.TCP(ctx, closesAtOnce); err == nil {
		t.Error("a connection that is dropped at once passed the check")
	}
	started := time.Now()
	if err := probe.TCP(ctx, waits); err != nil {
		t.Errorf("a waiting server failed the check: %v", err)
	}
	if took := time.Since(started); took > 1500*time.Millisecond {
		t.Errorf("the check waited %s for a server that says nothing first", took)
	}
	if err := probe.TCP(ctx, greets); err != nil {
		t.Errorf("a greeting server failed the check: %v", err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := probe.TCP(ctx, closed); err == nil {
		t.Error("a closed port passed the check")
	}
}

func TestMonitorEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dep := e.deploy()
	container := ContainerName(e.app.ID, dep.ID)
	ev := func(action, name string) []byte {
		return []byte(`{"Action":"` + action + `","Actor":{"Attributes":{"musdash.kind":"app","musdash.resource":"` + e.app.ID + `","name":"` + name + `"}}}`)
	}

	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev("die", "musdash-"+e.app.ID+"-olddeploy"))
	if got := e.reload().Status; got != db.AppRunning {
		t.Fatalf("an old container's exit changed the status to %s", got)
	}
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev("die", container))
	if got := e.reload().Status; got != db.AppExited {
		t.Fatalf("status %s after the serving container died", got)
	}
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev("start", container))
	if got := e.reload().Status; got != db.AppRunning {
		t.Fatalf("status %s after Docker restarted the container", got)
	}

	e.db.SetAppStatus(ctx, e.app.ID, db.AppDeploying)
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev("die", container))
	if got := e.reload().Status; got != db.AppDeploying {
		t.Fatalf("an event overwrote a deployment in progress: %s", got)
	}
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, []byte("not json"))
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev("exec_create: sh", container))
}

func TestReconcile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dep := e.deploy()
	container := ContainerName(e.app.ID, dep.ID)
	list := container + "\texited\tapp\t" + e.app.ID + "\t" + dep.ID + "\n"
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return list, nil
		}
		return running, nil
	}
	r, _ := e.d.Runners.Runner(ctx, e.server)
	dk := dockerClient(r)
	if err := e.d.Reconcile(ctx, e.server, dk); err != nil {
		t.Fatal(err)
	}
	if got := e.reload().Status; got != db.AppExited {
		t.Fatalf("status %s, want exited", got)
	}
	list = strings.Replace(list, "exited", "running", 1)
	e.d.Reconcile(ctx, e.server, dk)
	if got := e.reload().Status; got != db.AppRunning {
		t.Fatalf("status %s, want running", got)
	}
	// The container vanished entirely (removed by hand).
	list = ""
	e.d.Reconcile(ctx, e.server, dk)
	if got := e.reload().Status; got != db.AppExited {
		t.Fatalf("status %s, want exited", got)
	}
}

func TestLogIsCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for range 64 { // 4 MB
		l.Write(chunk)
	}
	l.Step("Failed: %s", "the end")
	l.Close()
	raw, _ := os.ReadFile(path)
	if len(raw) > maxLogBytes+200 {
		t.Fatalf("log is %d bytes", len(raw))
	}
	if !bytes.Contains(raw, []byte("[log truncated")) || !bytes.HasSuffix(raw, []byte("Failed: the end\n")) {
		t.Fatal("the log must note the truncation and still end with the final step")
	}
}

func TestFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	l, _ := OpenLog(path)
	l.Write([]byte("one\n"))

	var mu sync.Mutex
	finished := false
	var out bytes.Buffer
	lw := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) })
	done := make(chan error, 1)
	go func() {
		done <- Follow(context.Background(), path, lw, func() bool { mu.Lock(); defer mu.Unlock(); return finished })
	}()
	time.Sleep(50 * time.Millisecond)
	l.Write([]byte("two\n"))
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	if out.String() != "one\ntwo\n" {
		t.Fatalf("mid-stream: %q", out.String())
	}
	l.Write([]byte("three\n"))
	finished = true
	mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Follow did not end after the deployment finished")
	}
	if out.String() != "one\ntwo\nthree\n" {
		t.Fatalf("final: %q", out.String())
	}

	// A cancelled reader stops promptly.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- Follow(ctx, path, io.Discard, func() bool { return false }) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not stop when cancelled")
	}
}

func dockerClient(r runner.Runner) docker.Client { return docker.Client{R: r} }

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// remoteServer adds a server that is reached over SSH and is really this
// machine: an SSH server inside the test, with a data directory of its own.
// It returns the server's row, the pool that reaches it and the SSH server.
func (e *env) remoteServer() (db.Server, *servers.Pool, *sshtest.Server) {
	e.t.Helper()
	ctx := context.Background()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		e.t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(private)
	srv := sshtest.StartWithKey(e.t, signer)
	sealed, _ := e.d.Box.Seal(pem.EncodeToMemory(block))
	key, err := e.db.CreateSSHKey(ctx, e.team, "server key", servers.HostKeyLine(signer.PublicKey()), sealed)
	if err != nil {
		e.t.Fatal(err)
	}
	server, err := e.db.CreateServer(ctx, db.Server{TeamID: e.team, Name: "second", Host: srv.Host, Port: srv.Port, SSHUser: "deploy",
		SSHKeyID: key.ID, DataDir: filepath.Join(e.t.TempDir(), "remote-data"), IP: "198.51.100.20"})
	if err != nil {
		e.t.Fatal(err)
	}
	pool := servers.New()
	pool.DB, pool.Box = e.db, e.d.Box
	e.t.Cleanup(func() { pool.Forget(server.ID) })
	first, _, err := pool.FirstContact(ctx, server)
	if err != nil {
		e.t.Fatal(err)
	}
	first.Close()
	server, _ = e.db.ServerByID(ctx, server.ID)
	return server, pool, srv
}

// The same deployment, to a server reached over SSH. The "remote" server
// is this machine, so Docker is real and so is everything in between: the
// protocol, the quoting, the paths, the forwarded health check.
func TestDeployToARemoteServerWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	server, pool, srv := e.remoteServer()
	e.d.Runners = pool
	e.d.Probe = nil // through the server's connection, as in production
	e.d.healthEvery = 300 * time.Millisecond
	e.db.Exec(`UPDATE apps SET server_id = ?, health_timeout = 60, health_path = '/' WHERE id = ?`, server.ID, e.app.ID)
	secretValue := "s3cret-value-" + secret.RandomID()
	sealed, _ := e.d.Box.SealString(secretValue)
	if err := e.db.ReplaceEnvVars(ctx, db.KindApp, e.app.ID, []db.EnvVar{{Key: "API_TOKEN", Value: sealed}}); err != nil {
		t.Fatal(err)
	}
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	t.Cleanup(func() {
		out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"ps", "--all", "--quiet", "--filter", "label=" + docker.LabelResource + "=" + e.app.ID}})
		for _, id := range strings.Fields(string(out)) {
			dk.Remove(ctx, id)
		}
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", NetworkName(e.app.EnvironmentID)}})
	})

	dep := e.deployWithin(5 * time.Minute)
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	app := e.reload()
	res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(app.HostPort) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "nginx") {
		t.Fatalf("the app answers %q", body)
	}
	// The container has the variable; no command line on the way did.
	out, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", app.Container, "printenv", "API_TOKEN"}})
	if err != nil || strings.TrimSpace(string(out)) != secretValue {
		t.Fatalf("the variable in the container: %q %v", out, err)
	}
	for _, line := range srv.Commands() {
		if strings.Contains(line, secretValue) {
			t.Fatalf("a secret crossed in a command line: %s", line)
		}
	}
	// What a deployment writes is under the server's data directory, and
	// nothing of it under the control plane's.
	if left, _ := filepath.Glob(filepath.Join(server.DataDir, "apps", app.ID, "*")); len(left) == 0 {
		t.Fatal("nothing was written under the server's data directory")
	}
	if _, err := os.Stat(e.cfg.AppDir(app.ID)); !os.IsNotExist(err) {
		t.Fatal("the deployment wrote into the control plane's data directory")
	}
	routes, err := os.ReadFile(filepath.Join(server.DataDir, "proxy", "routes.json"))
	if err != nil || !strings.Contains(string(routes), "shop.example.com") || !strings.Contains(string(routes), strconv.Itoa(app.HostPort)) {
		t.Fatalf("the server's routes file: %q %v", routes, err)
	}
	if _, err := os.Stat(e.cfg.RoutesPath()); err == nil {
		if local, _ := os.ReadFile(e.cfg.RoutesPath()); strings.Contains(string(local), "shop.example.com") {
			t.Fatal("the app's route was written to the control plane's proxy")
		}
	}

	// Stop and delete work over the same connection.
	if err := e.d.Stop(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Destroy(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(server.DataDir, "apps", app.ID)); !os.IsNotExist(err) {
		t.Fatal("the app's directory on the server was left behind")
	}
	t.Logf("deployed over SSH: %d commands crossed, files under %s", len(srv.Commands()), server.DataDir)
}

// What a server reports is about its own containers. One that names an
// app on another server (a build server knows the app's id and can guess
// its container's name) changes nothing.
func TestAServerCannotReportOnAnothersContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.deploy()
	app := e.reload()
	told := 0
	e.d.Notify = func(string, notify.Event) { told++ }
	ev := []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"app","musdash.resource":"` + app.ID + `","name":"` + app.Container + `","exitCode":"1"}}}`)
	e.d.applyEvent(ctx, "another-server", docker.Client{R: e.fake}, ev)
	if got := e.reload(); got.Status != db.AppRunning || told != 0 {
		t.Fatalf("another server's event: status %s, %d notices", got.Status, told)
	}
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, ev)
	if got := e.reload(); got.Status != db.AppExited || told != 1 {
		t.Fatalf("the app's own server's event: status %s, %d notices", got.Status, told)
	}
}

// A monitor for a server that was removed ends, instead of trying again
// every minute for as long as the process runs.
func TestMonitorEndsWhenItsServerIsGone(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		e.d.Monitor(ctx, db.Server{ID: "removed", Name: "removed", Kind: db.ServerSSH})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the monitor of a removed server kept going")
	}
	if ctx.Err() != nil {
		t.Fatal("it ended only with its context")
	}
}

// The dashboard's own address stays the dashboard's whatever the domains
// table holds: an app served from it would have its origin in a browser.
func TestBuildRoutesKeepsTheDashboardsAddress(t *testing.T) {
	rows := []db.RouteRow{
		{Host: "dash.example.com", HostPort: 20001},
		{Host: "dash.example.com", Path: "/app", HostPort: 20002},
		{Host: "www.dash.example.com", RedirectWWW: true, HostPort: 20003},
		{Host: "other.example.com", HostPort: 20004},
	}
	file, skipped := BuildRoutes(rows, "", "dash.example.com", "127.0.0.1:8000")
	if len(skipped) != 2 {
		t.Fatalf("skipped %v", skipped)
	}
	raw, _ := json.Marshal(file)
	tab, err := proxy.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/app", "/app/x"} {
		if rt, _ := tab.Lookup("dash.example.com", path); rt.Target != "127.0.0.1:8000" {
			t.Errorf("dash.example.com%s goes to %q", path, rt.Target)
		}
	}
}
