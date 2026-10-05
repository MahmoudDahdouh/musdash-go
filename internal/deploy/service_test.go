package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

// newService creates a service in the test environment and prepares it as
// saving its Compose file would: generated values and endpoints with a
// domain each.
func (e *env) newService(template, name, composeText string, connect bool) db.Service {
	e.t.Helper()
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: name, Template: template, Compose: composeText, ConnectEnv: connect,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	host := func(endpoint string) (string, bool) {
		return strings.ToLower(strings.ReplaceAll(endpoint, "_", "-")) + "." + name + ".example.test", false
	}
	if err := e.d.PrepareService(ctx, s, map[string]string{}, host); err != nil {
		e.t.Fatal(err)
	}
	s, _ = e.db.ServiceByID(ctx, s.ID)
	return s
}

// deployService queues a deployment and waits for the service to settle.
func (e *env) deployService(s db.Service, limit time.Duration) db.Service {
	e.t.Helper()
	ctx := context.Background()
	if err := e.d.EnqueueService(ctx, s, false); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		got, err := e.db.ServiceByID(ctx, s.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		if got.Status != db.AppDeploying {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("the service did not settle")
	return s
}

func (e *env) serviceLog(s db.Service) string {
	raw, _ := os.ReadFile(e.cfg.ServiceLogPath(s.ID))
	return string(raw)
}

const smallStack = `services:
  front:
    image: nginx:alpine
    environment:
      - SERVICE_FQDN_FRONT_80
      - PUBLIC_URL=${SERVICE_URL_FRONT}
      - CACHE_PASSWORD=${SERVICE_PASSWORD_CACHE}
      - GREETING=${GREETING:-hello}
    volumes:
      - pages:/usr/share/nginx/html
    depends_on:
      - cache
  cache:
    image: redis:7-alpine
    command: sh -c 'exec redis-server --requirepass "$$REDIS_PASSWORD"'
    environment:
      - REDIS_PASSWORD=${SERVICE_PASSWORD_CACHE}
    healthcheck:
      test: ["CMD-SHELL", "REDISCLI_AUTH=$$REDIS_PASSWORD redis-cli ping | grep -q PONG"]
      interval: 2s
      retries: 20
volumes:
  pages:
`

// TestServiceWithDocker runs a two-container stack on the local Docker
// daemon: deploy, reach its endpoint, redeploy keeping data and secrets,
// stop, and delete with its volume.
func TestServiceWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}

	// The environment already has an app called "web". A stack that joins
	// the environment's network may not bring a service of that name.
	clash := e.newService(db.TemplateCustom, "clash", strings.ReplaceAll(strings.ReplaceAll(smallStack, "front:", "web:"), "FRONT", "WEB"), true)
	if got := e.deployService(clash, 5*time.Minute); got.Status != db.AppFailed || !strings.Contains(got.LastError, `"web" would share its name on the environment's network with the app web`) {
		t.Fatalf("a name clash on the environment's network: %s %s", got.Status, got.LastError)
	}
	if err := e.d.DestroyService(ctx, clash.ID, true); err != nil {
		t.Fatal(err)
	}

	s := e.newService(db.TemplateCustom, "site", smallStack, true)
	project := ServiceProject(s.ID)
	t.Cleanup(func() {
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", project, "down", "--volumes", "--remove-orphans", "--timeout", "2"}})
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", NetworkName(s.EnvironmentID)}})
	})

	got := e.deployService(s, 5*time.Minute)
	if got.Status != db.AppRunning {
		t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
	}
	if got.Members != "cache,front" {
		t.Fatalf("members %q", got.Members)
	}
	endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || endpoints[0].ComposeService != "front" || endpoints[0].Port != 80 || endpoints[0].HostPort < portMin {
		t.Fatalf("endpoints: %+v", endpoints)
	}
	fetch := func() (string, error) {
		res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(endpoints[0].HostPort) + "/")
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body), nil
	}
	exec := func(service string, script string) string {
		t.Helper()
		out, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", project + "-" + service + "-1", "sh", "-c", script}})
		if err != nil {
			t.Fatalf("exec in %s: %v", service, err)
		}
		return strings.TrimSpace(string(out))
	}
	// An empty volume is filled from the image, so nginx's own page is
	// there; replace it to see the volume survive a redeploy.
	if body, err := fetch(); err != nil || !strings.Contains(body, "nginx") {
		t.Fatalf("the endpoint does not answer on its loopback port: %q %v", body, err)
	}
	exec("front", `echo "kept across a redeploy" > /usr/share/nginx/html/index.html`)

	// The variables reached the containers, generated once and shared.
	vars, _ := e.d.ServiceVariables(got)
	password := vars["SERVICE_PASSWORD_CACHE"]
	if len(password) != 32 {
		t.Fatalf("generated password %q", password)
	}
	if env := exec("front", "env"); !strings.Contains(env, "CACHE_PASSWORD="+password) || !strings.Contains(env, "PUBLIC_URL=http://front.site.example.test") ||
		!strings.Contains(env, "SERVICE_FQDN_FRONT_80=front.site.example.test") || !strings.Contains(env, "GREETING=hello") {
		t.Fatalf("front's environment:\n%s", env)
	}
	if exec("cache", `echo "$REDIS_PASSWORD"`) != password {
		t.Fatal("the two containers did not get the same generated value")
	}
	// The containers carry musdash's labels and the endpoint is published
	// on the loopback interface only.
	listed, _ := dk.List(ctx)
	if serviceStatus(listed, s.ID) != db.AppRunning {
		t.Fatalf("status from the container list: %s", serviceStatus(listed, s.ID))
	}
	ports, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"port", project + "-front-1"}})
	if strings.TrimSpace(string(ports)) != "80/tcp -> 127.0.0.1:"+strconv.Itoa(endpoints[0].HostPort) {
		t.Fatalf("published ports of front: %s", ports)
	}
	if out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"port", project + "-cache-1"}}); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("cache is published: %s", out)
	}
	// Connected to the environment: reachable by service name from there.
	out, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"run", "--rm", "--network", NetworkName(s.EnvironmentID), "nginx:alpine", "wget", "-qO-", "http://front/"}})
	if err != nil || !strings.Contains(string(out), "kept across a redeploy") {
		t.Fatalf("from the environment's network: %q %v", out, err)
	}
	// The resolved file holds the secrets and is private.
	if info, err := os.Stat(e.cfg.AppDir(s.ID) + "/compose.resolved.json"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("resolved file: %v %v", info, err)
	}
	// The routes carry the endpoint's domain.
	routes := e.routes()
	found := false
	for _, r := range routes.Routes {
		found = found || (r.Host == "front.site.example.test" && r.Target == "127.0.0.1:"+strconv.Itoa(endpoints[0].HostPort))
	}
	if !found {
		t.Fatalf("no route for the endpoint: %+v", routes.Routes)
	}

	// A person's own variable, then a redeploy: same port, same password,
	// same data.
	vars["GREETING"] = "bonjour"
	sealed, _ := e.d.SealServiceVariables(vars)
	e.db.SetServiceVariables(ctx, s.ID, sealed)
	got, _ = e.db.ServiceByID(ctx, s.ID)
	if got = e.deployService(got, 5*time.Minute); got.Status != db.AppRunning {
		t.Fatalf("redeploy: %s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
	}
	again, _ := e.db.ListEndpoints(ctx, s.ID)
	if again[0].HostPort != endpoints[0].HostPort {
		t.Fatalf("the endpoint moved from port %d to %d", endpoints[0].HostPort, again[0].HostPort)
	}
	if env := exec("front", "env"); !strings.Contains(env, "GREETING=bonjour") || !strings.Contains(env, "CACHE_PASSWORD="+password) {
		t.Fatalf("after the redeploy:\n%s", env)
	}
	if body, err := fetch(); err != nil || !strings.Contains(body, "kept across a redeploy") {
		t.Fatalf("after the redeploy the endpoint serves %q %v", body, err)
	}

	// A file that asks for too much is refused, and the stack keeps running.
	bad := got
	bad.Compose = strings.Replace(smallStack, "image: nginx:alpine", "image: nginx:alpine\n    privileged: true\n    pid: host", 1)
	e.db.UpdateServiceCompose(ctx, e.team, bad)
	bad, _ = e.db.ServiceByID(ctx, s.ID)
	// The service is still what its running containers make it; the
	// refusal is its last error.
	if bad = e.deployService(bad, 2*time.Minute); bad.Status != db.AppRunning || !strings.Contains(bad.LastError, `"privileged" is not allowed`) || !strings.Contains(bad.LastError, `"pid" is not allowed`) {
		t.Fatalf("a privileged stack: %s %s", bad.Status, bad.LastError)
	}
	if body, err := fetch(); err != nil || !strings.Contains(body, "kept across a redeploy") {
		t.Fatalf("the running stack was disturbed by a refused file: %q %v", body, err)
	}

	// Stop: nothing answers, the route is gone; the volume stays.
	if err := e.d.StopService(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fetch(); err == nil {
		t.Fatal("the endpoint still answers after Stop")
	}
	for _, r := range e.routes().Routes {
		if r.Host == "front.site.example.test" {
			t.Fatal("a stopped service is still routed")
		}
	}
	volume := project + "_pages"
	hasVolume := func() bool {
		return local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"volume", "inspect", volume}}) == nil
	}
	if !hasVolume() {
		t.Fatal("stopping removed the volume")
	}

	if err := e.d.DestroyService(ctx, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if hasVolume() {
		t.Fatal("the volume is still there although its deletion was asked for")
	}
	listed, _ = dk.List(ctx)
	for _, c := range listed {
		if c.Resource == s.ID {
			t.Fatalf("a container of the deleted service is left: %+v", c)
		}
	}
	if _, err := e.db.ServiceByID(ctx, s.ID); err == nil {
		t.Fatal("the service's row is still there")
	}
	t.Logf("a two-container stack: deployed, reached on 127.0.0.1:%d, redeployed, refused when privileged, stopped, deleted", endpoints[0].HostPort)
}

// scriptedStack answers the commands of a service deployment the way a
// server with Docker would, for the stack "front + cache".
type scriptedStack struct {
	project string
	dir     string
	fail    func(line string) error // optional failure injection
}

func (s *scriptedStack) handle(line string, _ runner.Cmd) (string, error) {
	if s.fail != nil {
		if err := s.fail(line); err != nil {
			return "", err
		}
	}
	switch {
	case strings.HasPrefix(line, "docker version"):
		return "29.8.0\n", nil
	case strings.HasPrefix(line, "id -u"):
		return "1000\n", nil
	case strings.HasPrefix(line, "id -g"):
		return "1000\n", nil
	case strings.Contains(line, "config --format json --no-interpolate"):
		return `{"name":"` + s.project + `","services":{
			"front":{"image":"nginx:alpine","environment":["SERVICE_FQDN_FRONT_80","CACHE_PASSWORD=${SERVICE_PASSWORD_CACHE}"]},
			"cache":{"image":"redis:7-alpine","environment":["REDIS_PASSWORD=${SERVICE_PASSWORD_CACHE}"]}}}`, nil
	case strings.Contains(line, "config --format json"):
		return `{"name":"` + s.project + `","networks":{"default":{"name":"` + s.project + `_default"}},
			"volumes":{"pages":{"name":"` + s.project + `_pages"}},
			"services":{
			"front":{"image":"nginx:alpine","environment":{"SERVICE_FQDN_FRONT_80":"front.site.example.test","CACHE_PASSWORD":"resolved-secret"},"networks":{"default":null},
				"volumes":[{"type":"volume","source":"pages","target":"/usr/share/nginx/html"}]},
			"cache":{"image":"redis:7-alpine","environment":{"REDIS_PASSWORD":"resolved-secret"},"networks":{"default":null}}}}`, nil
	case strings.HasPrefix(line, "docker inspect"):
		return running, nil
	}
	return "", nil
}

const scriptedCompose = `services:
  front:
    image: nginx:alpine
    environment:
      - SERVICE_FQDN_FRONT_80
      - CACHE_PASSWORD=${SERVICE_PASSWORD_CACHE}
  cache:
    image: redis:7-alpine
    environment:
      - REDIS_PASSWORD=${SERVICE_PASSWORD_CACHE}
`

func (e *env) scriptedService(connect bool) (db.Service, *scriptedStack) {
	e.t.Helper()
	s := e.newService(db.TemplateCustom, "site", scriptedCompose, connect)
	st := &scriptedStack{project: ServiceProject(s.ID), dir: e.cfg.AppDir(s.ID)}
	e.fake.Handle = st.handle
	return s, st
}

func TestDeployService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	dir := e.cfg.AppDir(s.ID)
	project := ServiceProject(s.ID)

	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning || got.Members != "cache,front" {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	vars, _ := e.d.ServiceVariables(got)
	password := vars["SERVICE_PASSWORD_CACHE"]
	if len(password) != 32 {
		t.Fatalf("generated: %v", vars)
	}

	calls := e.fake.Calls()
	compose := "docker compose --project-name " + project + " --project-directory " + dir + " --file " + dir + "/compose.resolved.json --ansi never "
	order := []string{
		"docker image inspect --format {{.Id}} docker:29-cli",
		"docker run --rm --interactive --network none --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --pids-limit 128 --user 1000:1000",
		" --file - config --format json",
		compose + "pull --ignore-buildable",
		compose + "up --detach --remove-orphans --wait --wait-timeout 600",
	}
	last := -1
	for _, want := range order {
		i := -1
		for j := last + 1; j < len(calls); j++ {
			if strings.Contains(calls[j], want) {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("%q is missing or out of order in:\n%s", want, strings.Join(calls, "\n"))
		}
		last = i
	}
	// The file is loaded twice, once to see where variables are used and
	// once filled in, and never by Compose on the server itself.
	all := strings.Join(calls, "\n")
	if n := strings.Count(all, "config --format json"); n != 2 {
		t.Fatalf("the sandbox ran %d times, want 2", n)
	}
	// No value of any variable is on a command line.
	if strings.Contains(all, password) || strings.Contains(all, "resolved-secret") {
		t.Fatal("a secret appeared on a command line")
	}
	// The sandbox is given the variables through a private file and sees
	// nothing of the server.
	for _, c := range calls {
		if strings.Contains(c, "config --format json") && (strings.Contains(c, "--mount") || strings.Contains(c, "-v ") || strings.Contains(c, "docker.sock") || !strings.Contains(c, "--env-file "+dir+"/sandbox.env")) {
			t.Fatalf("sandbox command: %s", c)
		}
	}
	envFile, mode, ok := e.fake.File(dir + "/sandbox.env")
	if !ok || mode != 0o600 || !strings.Contains(envFile, "SERVICE_PASSWORD_CACHE="+password+"\n") || !strings.Contains(envFile, "SERVICE_FQDN_FRONT_80=front.site.example.test\n") {
		t.Fatalf("variables file (mode %o):\n%s", mode, envFile)
	}

	// What is started is the checked document plus musdash's additions.
	resolved, mode, ok := e.fake.File(dir + "/compose.resolved.json")
	if !ok || mode != 0o600 {
		t.Fatalf("resolved file missing or not private (mode %o)", mode)
	}
	endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || endpoints[0].ComposeService != "front" || endpoints[0].Port != 80 || endpoints[0].HostPort < portMin || endpoints[0].HostPort > portMax {
		t.Fatalf("endpoints: %+v", endpoints)
	}
	for _, want := range []string{
		`"musdash.kind": "service"`, `"musdash.resource": "` + s.ID + `"`, `"restart": "unless-stopped"`,
		`"host_ip": "127.0.0.1"`, `"published": "` + strconv.Itoa(endpoints[0].HostPort) + `"`,
	} {
		if !strings.Contains(resolved, want) {
			t.Errorf("the resolved file is missing %s", want)
		}
	}
	// Not connected: the environment's network is not mentioned at all.
	if strings.Contains(resolved, "musdash-environment") || strings.Contains(all, "docker network") {
		t.Error("a stack that was not connected to the environment joined its network")
	}
	// The endpoint's domain is routed to its loopback port.
	found := false
	for _, r := range e.routes().Routes {
		found = found || (r.Host == "front.site.example.test" && r.Target == "127.0.0.1:"+strconv.Itoa(endpoints[0].HostPort))
	}
	if !found {
		t.Fatalf("routes: %+v", e.routes().Routes)
	}

	// A second deployment keeps the generated value and the port.
	if got = e.deployService(got, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	vars2, _ := e.d.ServiceVariables(got)
	again, _ := e.db.ListEndpoints(ctx, s.ID)
	if vars2["SERVICE_PASSWORD_CACHE"] != password || again[0].HostPort != endpoints[0].HostPort {
		t.Fatal("a redeploy changed a generated value or the endpoint's port")
	}
}

func TestServiceConnectedToTheEnvironment(t *testing.T) {
	e := newEnv(t)
	s, _ := e.scriptedService(true)
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	resolved, _, _ := e.fake.File(e.cfg.AppDir(s.ID) + "/compose.resolved.json")
	if !strings.Contains(resolved, `"name": "`+NetworkName(s.EnvironmentID)+`"`) || !strings.Contains(resolved, `"external": true`) {
		t.Fatalf("the stack did not join the environment's network:\n%s", resolved)
	}
	if !strings.Contains(strings.Join(e.fake.Calls(), "\n"), "docker network inspect "+NetworkName(s.EnvironmentID)) && !strings.Contains(strings.Join(e.fake.Calls(), "\n"), NetworkName(s.EnvironmentID)+"\n") {
		t.Fatal("the environment's network was not made sure of")
	}

	// A second connected stack with the same service names is refused,
	// before anything of it is started.
	other := e.newService(db.TemplateCustom, "second", scriptedCompose, true)
	st := &scriptedStack{project: ServiceProject(other.ID)}
	e.fake.Handle = st.handle
	before := len(e.fake.Calls())
	got = e.deployService(other, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, `would share its name on the environment's network with the service site`) {
		t.Fatalf("%+v", got)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.Contains(c, " up ") || strings.Contains(c, " pull ") {
			t.Fatalf("a clashing stack was started: %s", c)
		}
	}
	// Nor can an app or a database later take a name the connected stack
	// answers to there.
	ctx := context.Background()
	if _, err := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: s.EnvironmentID, ServerID: e.server.ID, Name: "cache", Image: "nginx", Port: 80}); !errors.Is(err, db.ErrNameTaken) {
		t.Fatalf("an app took the name of a connected stack's service: %v", err)
	}
	if _, err := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: s.EnvironmentID, ServerID: e.server.ID, Name: "site", Image: "nginx", Port: 80}); !errors.Is(err, db.ErrNameTaken) {
		t.Fatalf("an app took a service's own name: %v", err)
	}
}

func TestServiceThatAsksForTooMuchIsRefused(t *testing.T) {
	e := newEnv(t)
	s, st := e.scriptedService(false)
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, "config --format json") && !strings.Contains(line, "--no-interpolate") {
			return `{"name":"` + st.project + `","services":{"front":{"image":"nginx:alpine","privileged":true,
				"volumes":[{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock","bind":{}}]},
				"cache":{"image":"redis:7-alpine","network_mode":"host"}}}`, nil
		}
		return st.handle(line, c)
	}
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppFailed {
		t.Fatalf("%+v", got)
	}
	for _, want := range []string{`service front: "privileged" is not allowed`, `/var/run/docker.sock cannot be mounted`, `service cache: the network mode "host" is not allowed`} {
		if !strings.Contains(got.LastError, want) {
			t.Errorf("the error does not mention %q:\n%s", want, got.LastError)
		}
	}
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "docker compose") {
			t.Fatalf("Compose was run on the server for a refused file: %s", c)
		}
	}
	if _, _, ok := e.fake.File(e.cfg.AppDir(s.ID) + "/compose.resolved.json"); ok {
		t.Fatal("a refused document was written as the file to start from")
	}
}

func TestServiceFailuresAreExplained(t *testing.T) {
	e := newEnv(t)
	s, st := e.scriptedService(false)

	// Compose rejects the file: its message is what the person sees.
	st.fail = func(line string) error {
		if strings.Contains(line, "config --format json") {
			return runnertest.Exit("docker", 15, "validating stdin: services.front.ports must be a list")
		}
		return nil
	}
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "services.front.ports must be a list") {
		t.Fatalf("%+v", got)
	}

	// The stack does not come up: the containers' last output is attached.
	st.fail = nil
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.Contains(line, " up --detach"):
			return "", runnertest.Exit("docker", 1, "container musdash-x-front-1 is unhealthy")
		case strings.Contains(line, " logs --tail"):
			return "front-1  | nginx: [emerg] bind() to 0.0.0.0:80 failed\n", nil
		}
		return st.handle(line, c)
	}
	got = e.deployService(got, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "did not come up") || !strings.Contains(got.LastError, "bind() to 0.0.0.0:80 failed") {
		t.Fatalf("%+v", got)
	}

	// A port of the server is taken by something else: other ports are
	// tried without bothering the person.
	ups := 0
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, " up --detach") {
			if ups++; ups == 1 {
				if c.Stderr != nil {
					io.WriteString(c.Stderr, "Error response from daemon: Bind for 127.0.0.1:20001 failed: port is already allocated\n")
				}
				return "", runnertest.Exit("docker", 1, "port is already allocated")
			}
		}
		return st.handle(line, c)
	}
	before, _ := e.db.ListEndpoints(context.Background(), s.ID)
	loadsBefore := countCalls(e.fake.Calls(), "config --format json")
	got = e.deployService(got, 10*time.Second)
	after, _ := e.db.ListEndpoints(context.Background(), s.ID)
	if got.Status != db.AppRunning || ups != 2 || after[0].HostPort == before[0].HostPort {
		t.Fatalf("after a taken port: %+v, %d attempts, port %d then %d", got, ups, before[0].HostPort, after[0].HostPort)
	}
	// The second attempt starts from the document that was checked, not
	// from one loaded again.
	if loads := countCalls(e.fake.Calls(), "config --format json") - loadsBefore; loads != 2 {
		t.Fatalf("the file was loaded %d times for one deployment, want 2", loads)
	}
}

func countCalls(calls []string, part string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

// A redeployment that fails before anything is started leaves the stack
// from before running. It must stay reachable, and say so.
func TestFailedRedeployKeepsWhatWasServing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, st := e.scriptedService(false)
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	routed := func() bool {
		if err := e.d.SyncRoutes(ctx, e.server); err != nil && !errors.Is(err, ErrProxyDown) {
			t.Fatal(err)
		}
		for _, r := range e.routes().Routes {
			if r.Host == "front.site.example.test" {
				return true
			}
		}
		return false
	}
	if !routed() {
		t.Fatal("not routed after the first deployment")
	}

	project := ServiceProject(s.ID)
	ps := project + "-front-1\trunning\tservice\t" + s.ID + "\t\tUp 2 minutes\n" +
		project + "-cache-1\trunning\tservice\t" + s.ID + "\t\tUp 2 minutes\n" +
		// A migration that ran and ended well is not a container that is down.
		project + "-migrate-1\texited\tservice\t" + s.ID + "\t\tExited (0) 2 minutes ago\n"
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker ps"):
			return ps, nil
		case strings.Contains(line, "config --format json"):
			return "", runnertest.Exit("docker", 15, "yaml: line 3: did not find expected key")
		}
		return st.handle(line, c)
	}
	got = e.deployService(got, 10*time.Second)
	if got.Status != db.AppRunning || !strings.Contains(got.LastError, "did not find expected key") {
		t.Fatalf("a failed redeploy over a running stack: status %q, error %q", got.Status, got.LastError)
	}
	if !routed() {
		t.Fatal("the running stack lost its route because a redeployment failed")
	}

	// With a container that really is down, the stack is degraded, and
	// still routed.
	ps = strings.Replace(ps, "cache-1\trunning", "cache-1\texited", 1)
	ps = strings.Replace(ps, "Up 2 minutes\n"+project+"-migrate", "Exited (1) 5 seconds ago\n"+project+"-migrate", 1)
	got = e.deployService(got, 10*time.Second)
	if got.Status != db.AppDegraded || !routed() {
		t.Fatalf("status %q, routed %v", got.Status, routed())
	}

	// Nothing running at all: that is a failed service.
	ps = ""
	if got = e.deployService(got, 10*time.Second); got.Status != db.AppFailed {
		t.Fatalf("status %q", got.Status)
	}
}

// Stop pressed while a deployment waits its turn is the last word.
func TestQueuedDeployGivesWayToStop(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	if err := e.db.SetServiceState(ctx, s.ID, db.AppStopped, ""); err != nil {
		t.Fatal(err)
	}
	before := len(e.fake.Calls())
	if err := e.d.runServiceJob(ctx, []byte(`{"id":"`+s.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if calls := e.fake.Calls()[before:]; len(calls) != 0 {
		t.Fatalf("a stopped service was deployed by a job queued before the stop: %v", calls)
	}
	if got, _ := e.db.ServiceByID(ctx, s.ID); got.Status != db.AppStopped {
		t.Fatalf("status %q", got.Status)
	}

	// A deployment queued behind another shows as deploying while it runs,
	// whatever the first one left.
	if err := e.db.SetServiceState(ctx, s.ID, db.AppRunning, ""); err != nil {
		t.Fatal(err)
	}
	seen := ""
	inner := e.fake.Handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker version") {
			got, _ := e.db.ServiceByID(ctx, s.ID)
			seen = got.Status
		}
		return inner(line, c)
	}
	if err := e.d.runServiceJob(ctx, []byte(`{"id":"`+s.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if seen != db.AppDeploying {
		t.Fatalf("status while the second deployment ran: %q", seen)
	}
}

func TestStopAndDestroyService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	e.deployService(s, 10*time.Second)
	project := ServiceProject(s.ID)

	if err := e.d.StopService(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.ServiceByID(ctx, s.ID); got.Status != db.AppStopped {
		t.Fatalf("after stop: %+v", got)
	}
	if indexOf(e.fake.Calls(), "docker compose --project-name "+project) < 0 || !strings.Contains(strings.Join(e.fake.Calls(), "\n"), " stop --timeout 60") {
		t.Fatal("the stack was not stopped through Compose")
	}
	for _, r := range e.routes().Routes {
		if r.Host == "front.site.example.test" {
			t.Fatal("a stopped service is still routed")
		}
	}
	// Containers exiting because of the stop are not a failure.
	e.d.applyEvent(ctx, docker.Client{R: e.fake}, []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"service","musdash.resource":"`+s.ID+`","name":"`+project+`-front-1"}}}`))
	if got, _ := e.db.ServiceByID(ctx, s.ID); got.Status != db.AppStopped {
		t.Fatalf("status %s after the containers of a stopped stack exited", got.Status)
	}

	before := len(e.fake.Calls())
	if err := e.d.DestroyService(ctx, s.ID, false); err != nil {
		t.Fatal(err)
	}
	down := ""
	for _, c := range e.fake.Calls()[before:] {
		if strings.Contains(c, " down ") {
			down = c
		}
	}
	if down == "" || strings.Contains(down, "--volumes") {
		t.Fatalf("down without being asked to delete data: %q", down)
	}
	if _, err := e.db.ServiceByID(ctx, s.ID); err == nil {
		t.Fatal("the row is still there")
	}
	var domains int
	e.db.QueryRow(`SELECT count(*) FROM domains WHERE resource_kind = 'service'`).Scan(&domains)
	if domains != 0 {
		t.Fatalf("%d domains of a deleted service are left", domains)
	}

	// With the data: the volumes go too.
	s2, _ := e.scriptedService2("second")
	e.deployService(s2, 10*time.Second)
	if err := e.d.DestroyService(ctx, s2.ID, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.fake.Calls(), "\n"), "--project-name "+ServiceProject(s2.ID)+" --project-directory "+e.cfg.AppDir(s2.ID)+" --file "+e.cfg.AppDir(s2.ID)+"/compose.resolved.json --ansi never down --remove-orphans --timeout 60 --volumes") {
		t.Fatal("the volumes were kept although their deletion was asked for")
	}

	// A service that was never deployed has nothing to take down.
	never := e.newService(db.TemplateCustom, "never", scriptedCompose, false)
	before = len(e.fake.Calls())
	if err := e.d.DestroyService(ctx, never.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.HasPrefix(c, "docker compose") {
			t.Fatalf("Compose was run for a service that never started: %s", c)
		}
	}
}

func (e *env) scriptedService2(name string) (db.Service, *scriptedStack) {
	e.t.Helper()
	s := e.newService(db.TemplateCustom, name, scriptedCompose, false)
	st := &scriptedStack{project: ServiceProject(s.ID), dir: e.cfg.AppDir(s.ID)}
	e.fake.Handle = st.handle
	return s, st
}

func TestServiceStatusFollowsItsContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, st := e.scriptedService(false)
	e.deployService(s, 10*time.Second)
	project := ServiceProject(s.ID)
	states := map[string]string{"front": "running", "cache": "exited"}
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return project + "-front-1\t" + states["front"] + "\tservice\t" + s.ID + "\t\n" +
				project + "-cache-1\t" + states["cache"] + "\tservice\t" + s.ID + "\t\n" +
				"musdash-other-x-1\texited\tservice\tother\t\n", nil
		}
		return st.handle(line, c)
	}
	event := func(action, container string) {
		e.d.applyEvent(ctx, docker.Client{R: e.fake}, []byte(`{"Action":"`+action+`","Actor":{"Attributes":{"musdash.kind":"service","musdash.resource":"`+s.ID+`","name":"`+container+`"}}}`))
	}
	status := func() string {
		got, _ := e.db.ServiceByID(ctx, s.ID)
		return got.Status
	}
	event("die", project+"-cache-1")
	if status() != db.AppDegraded {
		t.Fatalf("one of two containers down: %s", status())
	}
	states["front"] = "exited"
	event("die", project+"-front-1")
	if status() != db.AppExited {
		t.Fatalf("all containers down: %s", status())
	}
	// A degraded stack keeps its routes; one that is down loses them at the
	// next publication.
	states["front"], states["cache"] = "running", "running"
	event("start", project+"-cache-1")
	if status() != db.AppRunning {
		t.Fatalf("all containers up again: %s", status())
	}

	// Reconcile at start-up reads the same list.
	states["cache"] = "exited"
	r, _ := e.d.Runners.Runner(ctx, e.server)
	if err := e.d.Reconcile(ctx, e.server, dockerClient(r)); err != nil {
		t.Fatal(err)
	}
	if status() != db.AppDegraded {
		t.Fatalf("after reconcile: %s", status())
	}
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "docker rm") {
			t.Fatalf("a service's container was removed as an orphan: %s", c)
		}
	}
}

func TestPrepareServiceGeneratesOnceAndTracksEndpoints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	text := "services:\n  app:\n    image: x\n    environment:\n      - SERVICE_FQDN_APP_3000\n      - A=${SERVICE_PASSWORD_A}\n      - B=${SERVICE_HEX_64_B}\n      - U=${SERVICE_USER_DB}\n      - URL=${SERVICE_URL_API_8080}\n"
	s := e.newService(db.TemplateCustom, "site", text, false)
	vars, err := e.d.ServiceVariables(s)
	if err != nil || len(vars) != 3 || len(vars["SERVICE_PASSWORD_A"]) != 32 || len(vars["SERVICE_HEX_64_B"]) != 64 || len(vars["SERVICE_USER_DB"]) != 16 {
		t.Fatalf("generated: %v %v", vars, err)
	}
	// Sealed in the database.
	if strings.Contains(s.Variables, vars["SERVICE_PASSWORD_A"]) {
		t.Fatal("the generated values are stored in the clear")
	}
	endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 2 || endpoints[0].Name != "API" || endpoints[0].Host != "api.site.example.test" || endpoints[1].Name != "APP" {
		t.Fatalf("endpoints: %+v", endpoints)
	}

	// The file changes: one address goes, one variable is added. What was
	// generated before stays as it was.
	s.Compose = "services:\n  app:\n    image: x\n    environment:\n      - SERVICE_FQDN_APP_3000\n      - A=${SERVICE_PASSWORD_A}\n      - C=${SERVICE_PASSWORD_C}\n"
	before := vars["SERVICE_PASSWORD_A"]
	if err := e.d.PrepareService(ctx, s, vars, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	stored, _ := e.db.ServiceByID(ctx, s.ID)
	vars2, _ := e.d.ServiceVariables(stored)
	if vars2["SERVICE_PASSWORD_A"] != before || len(vars2["SERVICE_PASSWORD_C"]) != 32 {
		t.Fatalf("after a change: %v", vars2)
	}
	endpoints, _ = e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || endpoints[0].Name != "APP" || endpoints[0].Host != "app.site.example.test" {
		t.Fatalf("endpoints after a change: %+v", endpoints)
	}
	var domains int
	e.db.QueryRow(`SELECT count(*) FROM domains WHERE resource_kind = 'service'`).Scan(&domains)
	if domains != 1 {
		t.Fatalf("%d service domains, want 1: the removed endpoint's domain must go", domains)
	}

	// An endpoint without a domain cannot be deployed: its variable would
	// be empty.
	e.db.Exec(`DELETE FROM domains WHERE resource_kind = 'service'`)
	st := &scriptedStack{project: ServiceProject(s.ID)}
	e.fake.Handle = st.handle
	e.db.UpdateServiceCompose(ctx, e.team, db.Service{ID: s.ID, Compose: s.Compose, Variables: stored.Variables})
	stored, _ = e.db.ServiceByID(ctx, s.ID)
	if got := e.deployService(stored, 10*time.Second); got.Status != db.AppFailed || !strings.Contains(got.LastError, "SERVICE_FQDN_APP_3000 has no domain") {
		t.Fatalf("%+v", got)
	}
}

// TestCatalogueWithDocker installs templates of the service catalogue on
// the local Docker daemon: each must come up healthy, answer on the
// loopback port of its endpoint, keep its data and generated values across
// a redeploy, and go away with its volumes. It downloads several gigabytes
// of images, so it has its own switch; MUSDASH_SERVICES="wordpress,minio"
// runs just those. Images that were not there before are removed again.
func TestCatalogueWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST_SERVICES") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST_SERVICES=1 to install the service catalogue on the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}
	only := os.Getenv("MUSDASH_SERVICES")
	imageRE := regexp.MustCompile(`(?m)^\s+image:\s*(\S+)`)

	for _, tpl := range catalog.Services() {
		if only != "" && !slices.Contains(strings.Split(only, ","), tpl.Key) {
			continue
		}
		if tpl.Key == "cloudflared" {
			continue // needs a real tunnel token from Cloudflare
		}
		t.Run(tpl.Key, func(t *testing.T) {
			var fresh []string
			for _, m := range imageRE.FindAllStringSubmatch(tpl.Compose, -1) {
				if have, _ := dk.HasImage(ctx, m[1]); !have {
					fresh = append(fresh, m[1])
				}
			}
			s := e.newService(tpl.Key, tpl.Key, tpl.Compose, tpl.ConnectEnv)
			project := ServiceProject(s.ID)
			t.Cleanup(func() {
				local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", project, "down", "--volumes", "--remove-orphans", "--timeout", "5"}})
				for _, image := range fresh {
					dk.RemoveImage(ctx, image)
				}
			})
			started := time.Now()
			got := e.deployService(s, 30*time.Minute)
			if got.Status != db.AppRunning {
				t.Fatalf("%s %s\n%s", got.Status, got.LastError, lastLines(e.serviceLog(s), 30))
			}
			endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
			if len(endpoints) == 0 {
				t.Fatal("the template has no endpoint")
			}
			answers := func() {
				t.Helper()
				for _, ep := range endpoints {
					req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(ep.HostPort)+"/", nil)
					req.Host = ep.Host
					// As the proxy would send it.
					req.Header.Set("X-Forwarded-Proto", "http")
					client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					res, err := client.Do(req)
					if err != nil {
						t.Fatalf("%s (%s:%d) does not answer on 127.0.0.1:%d: %v", ep.Name, ep.ComposeService, ep.Port, ep.HostPort, err)
					}
					res.Body.Close()
					if res.StatusCode >= 500 {
						t.Fatalf("%s answers %d", ep.Name, res.StatusCode)
					}
					t.Logf("%s: %s:%d answers %d on 127.0.0.1:%d", ep.Name, ep.ComposeService, ep.Port, res.StatusCode, ep.HostPort)
				}
			}
			answers()
			vars, _ := e.d.ServiceVariables(got)

			// A redeploy: same values, same ports, still answering.
			if got = e.deployService(got, 30*time.Minute); got.Status != db.AppRunning {
				t.Fatalf("redeploy: %s %s\n%s", got.Status, got.LastError, lastLines(e.serviceLog(s), 30))
			}
			vars2, _ := e.d.ServiceVariables(got)
			for name, value := range vars {
				if vars2[name] != value {
					t.Errorf("%s changed across a redeploy", name)
				}
			}
			again, _ := e.db.ListEndpoints(ctx, s.ID)
			for i := range endpoints {
				if again[i].HostPort != endpoints[i].HostPort {
					t.Errorf("endpoint %s moved to another port", endpoints[i].Name)
				}
			}
			answers()

			if err := e.d.DestroyService(ctx, s.ID, true); err != nil {
				t.Fatal(err)
			}
			out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"volume", "ls", "--quiet", "--filter", "name=" + project + "_"}})
			if strings.TrimSpace(string(out)) != "" {
				t.Fatalf("volumes left after deleting with data: %s", out)
			}
			t.Logf("%s installed, redeployed and deleted in %s", tpl.Name, time.Since(started).Round(time.Second))
		})
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
