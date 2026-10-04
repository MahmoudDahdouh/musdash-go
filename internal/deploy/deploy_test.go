package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
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
	if _, err := d.AddDomain(ctx, db.KindApp, app.ID, "shop.example.com", true, true); err != nil {
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
	dep.Probe = probe
	dep.healthEvery = 10 * time.Millisecond
	dep.drain = 0
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
	ctx := context.Background()
	app, err := e.db.AppByID(ctx, e.app.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	dep, err := e.d.Enqueue(ctx, app, "manual")
	if err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := e.db.DeploymentByID(ctx, dep.ID)
		if got.Status == db.DeploySuccess || got.Status == db.DeployFailed {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("deployment did not finish")
	return dep
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
		return proxy.File{}
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
	runAt := indexOf(calls, "docker run --detach --name "+newC)
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
	if err := e.d.Stop(context.Background(), app); err != nil {
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
	e.deploy()
	app := e.reload()
	if err := e.d.Destroy(context.Background(), app); err != nil {
		t.Fatal(err)
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
	file := BuildRoutes(rows, "ops@example.com", "dash.example.com", "127.0.0.1:8000")
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
	raw, _ = json.Marshal(BuildRoutes(nil, "", "", ""))
	if string(raw) != `{"routes":[]}` {
		t.Fatalf("empty file: %s", raw)
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

	e.d.applyEvent(ctx, ev("die", "musdash-"+e.app.ID+"-olddeploy"))
	if got := e.reload().Status; got != db.AppRunning {
		t.Fatalf("an old container's exit changed the status to %s", got)
	}
	e.d.applyEvent(ctx, ev("die", container))
	if got := e.reload().Status; got != db.AppExited {
		t.Fatalf("status %s after the serving container died", got)
	}
	e.d.applyEvent(ctx, ev("start", container))
	if got := e.reload().Status; got != db.AppRunning {
		t.Fatalf("status %s after Docker restarted the container", got)
	}

	e.db.SetAppStatus(ctx, e.app.ID, db.AppDeploying)
	e.d.applyEvent(ctx, ev("die", container))
	if got := e.reload().Status; got != db.AppDeploying {
		t.Fatalf("an event overwrote a deployment in progress: %s", got)
	}
	e.d.applyEvent(ctx, []byte("not json"))
	e.d.applyEvent(ctx, ev("exec_create: sh", container))
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
