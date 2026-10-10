package deploy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	return e.newServiceWith(template, name, composeText, connect, map[string]string{})
}

// newServiceWith is newService with the variables a person typed on the
// form.
func (e *env) newServiceWith(template, name, composeText string, connect bool, typed map[string]string) db.Service {
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
	// What the form held is stored when the service is made, as the page
	// does: the step after it stores only when it generated a value.
	if len(typed) > 0 {
		sealed, err := e.d.SealServiceVariables(typed)
		if err == nil {
			err = e.db.SetServiceVariables(ctx, s.ID, sealed)
		}
		if err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.d.PrepareService(ctx, s, typed, host); err != nil {
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
	e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"service","musdash.resource":"`+s.ID+`","name":"`+project+`-front-1"}}}`))
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
		e.d.applyEvent(ctx, e.server.ID, docker.Client{R: e.fake}, []byte(`{"Action":"`+action+`","Actor":{"Attributes":{"musdash.kind":"service","musdash.resource":"`+s.ID+`","name":"`+container+`"}}}`))
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

// publishedRE reads a TCP port of the server from a template's "ports":
// the number, or the default of the variable that holds it.
var publishedRE = regexp.MustCompile(`(?m)^\s+- "(?:(\d+)|\$\{[A-Z][A-Z0-9_]*:-(\d+)\}):\d+"$`)

// TestCatalogueWithDocker installs templates of the service catalogue on
// the local Docker daemon: each must come up healthy, answer on the
// loopback port of its endpoint and on the ports it publishes, keep its data and generated values across
// a redeploy, and go away with its volumes. It downloads several gigabytes
// of images, so it has its own switch. Without more it installs the
// templates written for musdash; MUSDASH_SERVICES="wordpress,umami" runs
// just those, which is how one of the several hundred imported templates
// is tried. MUSDASH_SERVICE_FILES names files of templates that are not in
// the catalogue yet (as tools/catalog -check writes one), tried the same
// way before they are added. Images that were not there before are removed
// again.
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
	// A stack that never comes up is waited for ten minutes, which is long
	// when many are tried: MUSDASH_SERVICE_START="5m" is how long one may
	// take here.
	if wait, err := time.ParseDuration(os.Getenv("MUSDASH_SERVICE_START")); err == nil && wait > 0 {
		e.d.serviceStartTimeout = wait
	}
	imageRE := regexp.MustCompile(`(?m)^\s+image:\s*["']?([^\s"']+)`)

	files := strings.Fields(os.Getenv("MUSDASH_SERVICE_FILES"))
	var templates []catalog.ServiceTemplate
	for _, tpl := range catalog.Services() {
		if only != "" && !slices.Contains(strings.Split(only, ","), tpl.Key) || only == "" && (tpl.Source != "" || len(files) > 0) {
			continue
		}
		if tpl.Key == "cloudflared" {
			continue // needs a real tunnel token from Cloudflare
		}
		tpl, _ = catalog.Service(tpl.Key)
		templates = append(templates, tpl)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		tpl := catalog.ServiceTemplate{Key: strings.TrimSuffix(filepath.Base(file), ".yaml"), Compose: string(raw)}
		tpl.Name = tpl.Key
		tpl.ConnectEnv = regexp.MustCompile(`(?m)^# connect: true$`).Match(raw)
		templates = append(templates, tpl)
	}
	for _, tpl := range templates {
		t.Run(tpl.Key, func(t *testing.T) {
			var fresh []string
			for _, m := range imageRE.FindAllStringSubmatch(tpl.Compose, -1) {
				if have, _ := dk.HasImage(ctx, m[1]); !have {
					fresh = append(fresh, m[1])
				}
			}
			// What the template's form would have asked for.
			typed := map[string]string{}
			for _, v := range catalog.ScanVariables(tpl.Compose) {
				if v.Required && strings.Contains(v.Name, "EMAIL") {
					typed[v.Name] = "someone@example.test"
				} else if v.Required {
					typed[v.Name] = "entered-by-the-person"
				}
			}
			s := e.newServiceWith(tpl.Key, tpl.Key, tpl.Compose, tpl.ConnectEnv, typed)
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
			// The TCP ports of the server the template publishes itself,
			// for what the proxy cannot carry.
			var published []string
			for _, m := range publishedRE.FindAllStringSubmatch(tpl.Compose, -1) {
				published = append(published, m[1]+m[2])
			}
			if len(endpoints) == 0 && len(published) == 0 && !tpl.ConnectEnv {
				t.Fatal("the template has no endpoint and no port")
			}
			// What the containers said explains a stack that is up and
			// does not answer.
			said := func() string {
				out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", project, "logs", "--tail", "40", "--no-color"}})
				return lastLines(string(out), 60)
			}
			answers := func() {
				t.Helper()
				for _, port := range published {
					conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 5*time.Second)
					for wait := time.Now().Add(2 * time.Minute); err != nil && time.Now().Before(wait); {
						time.Sleep(3 * time.Second)
						conn, err = net.DialTimeout("tcp", "127.0.0.1:"+port, 5*time.Second)
					}
					if err != nil {
						t.Fatalf("the published port %s does not answer: %v\n%s", port, err, said())
					}
					conn.Close()
					t.Logf("the published port %s answers", port)
				}
				for _, ep := range endpoints {
					req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(ep.HostPort)+"/", nil)
					req.Host = ep.Host
					// As the proxy would send it.
					req.Header.Set("X-Forwarded-Proto", "http")
					client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					res, err := client.Do(req)
					// A template with no health check counts as up the
					// moment its container runs, which can be before the
					// program in it listens (one that first migrates its
					// database, say).
					for wait := time.Now().Add(2 * time.Minute); err != nil && time.Now().Before(wait); {
						time.Sleep(3 * time.Second)
						res, err = client.Do(req)
					}
					if err != nil {
						t.Fatalf("%s (%s:%d) does not answer on 127.0.0.1:%d: %v\n%s", ep.Name, ep.ComposeService, ep.Port, ep.HostPort, err, said())
					}
					res.Body.Close()
					if res.StatusCode >= 500 {
						t.Fatalf("%s answers %d\n%s", ep.Name, res.StatusCode, said())
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

// gitStack answers the commands of a deployment of a stack that lives in a
// repository: one service built from the checkout, with a file of the
// checkout mounted.
type gitStack struct {
	project   string
	checkout  string          // the directory of the latest clone
	checkouts []string        // every directory cloned into, by name
	links     map[string]bool // repository paths that are symbolic links
	envFiles  string          // the service's env_file entries in the raw document, as JSON
	missing   map[string]bool // paths the repository does not have
	mounts    string          // further mounts of the service, as JSON, each ending with a comma
	compose   string
	failUp    bool
}

const gitComposeFile = `services:
  web:
    build: .
    environment:
      - SERVICE_FQDN_WEB_8080
    volumes:
      - ./conf/site.conf:/etc/site.conf
`

func (g *gitStack) rawEnvFiles() string {
	if g.envFiles == "" {
		return ""
	}
	return `,"env_file":` + strings.ReplaceAll(g.envFiles, "CHECKOUT", g.checkout)
}

func (g *gitStack) handle(e *env) func(line string, c runner.Cmd) (string, error) {
	return func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker version"):
			return "29.8.0\n", nil
		case strings.HasPrefix(line, "id -"):
			return "1000\n", nil
		case strings.HasPrefix(line, "git clone"):
			// Into a directory of this deployment's own.
			g.checkout = line[strings.LastIndex(line, " ")+1:]
			g.checkouts = append(g.checkouts, g.checkout[strings.LastIndex(g.checkout, "/")+1:])
			e.fake.PutFile(g.checkout+"/deploy/compose.yaml", g.compose)
			return "", nil
		case strings.HasPrefix(line, "ls -1 -- "):
			return strings.Join(g.checkouts, "\n") + "\ncompose.resolved.json\nsandbox.env\n", nil
		case strings.Contains(line, " up --detach") && g.failUp:
			return "", runnertest.Exit("docker", 1, "container web is unhealthy")
		case strings.Contains(line, "rev-parse HEAD"):
			return "0123456789abcdef0123456789abcdef01234567\n", nil
		case strings.Contains(line, "ls-tree HEAD -- "):
			p := line[strings.Index(line, "-- ")+3:]
			if g.missing[p] {
				return "", nil
			}
			if g.links[p] {
				return "120000 blob abc\t" + p + "\n", nil
			}
			return "100644 blob abc\t" + p + "\n", nil
		case strings.Contains(line, "config --format json --no-interpolate"):
			return `{"name":"` + g.project + `","services":{"web":{"build":{"context":"` + g.checkout + `/deploy"},"environment":["SERVICE_FQDN_WEB_8080"]` + g.rawEnvFiles() + `}}}`, nil
		case strings.Contains(line, "config --format json"):
			return `{"name":"` + g.project + `","networks":{"default":{"name":"` + g.project + `_default"}},"services":{"web":{
				"build":{"context":"` + g.checkout + `/deploy","dockerfile":"Dockerfile"},
				"environment":{"SERVICE_FQDN_WEB_8080":"x.example.test"},"networks":{"default":null},
				"volumes":[` + strings.ReplaceAll(g.mounts, "CHECKOUT", g.checkout) + `{"type":"bind","source":"` + g.checkout + `/deploy/conf/site.conf","target":"/etc/site.conf","bind":{"create_host_path":true}}]}}}`, nil
		case strings.HasPrefix(line, "docker inspect"):
			return running, nil
		}
		return "", nil
	}
}

// A development file mounts the source and puts a volume inside it. The
// source is mounted read-only here, so the volume needs its directory to
// be in the repository, and the deployment says so before Docker is asked.
func TestGitServiceWithAMountInsideTheCheckout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://github.com/acme/stack", RepoName: "acme/stack", Branch: "main", ComposePath: "deploy/compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &gitStack{project: ServiceProject(s.ID), compose: gitComposeFile, missing: map[string]bool{"deploy/app/node_modules": true},
		mounts: `{"type":"bind","source":"CHECKOUT/deploy/app","target":"/usr/src/app","bind":{"create_host_path":true}},
			{"type":"volume","target":"/usr/src/app/node_modules","volume":{}},`}
	e.fake.Handle = g.handle(e)

	got := e.deployService(s, 10*time.Second)
	for _, want := range []string{"service web: the mount at /usr/src/app/node_modules lies inside /usr/src/app", "has no deploy/app/node_modules"} {
		if !strings.Contains(got.LastError, want) {
			t.Fatalf("want %q in %q", want, got.LastError)
		}
	}
	for _, c := range e.fake.Calls() {
		if strings.Contains(c, " build") || strings.Contains(c, " up --detach") {
			t.Fatalf("something ran for a stack that cannot start: %s", c)
		}
	}

	// With the directory in the repository there is something to mount on.
	g.missing = nil
	if got = e.deployService(got, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
}

func TestDeployServiceFromGit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://github.com/acme/stack", RepoName: "Acme/Stack", Branch: "main", ComposePath: "deploy/compose.yaml", AutoDeploy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := e.cfg.AppDir(s.ID)
	g := &gitStack{project: ServiceProject(s.ID), links: map[string]bool{}, compose: gitComposeFile}
	e.fake.Handle = g.handle(e)

	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	// What the repository held is what the service now shows.
	first := g.checkout
	if got.Compose != gitComposeFile || got.Commit != "0123456789abcdef0123456789abcdef01234567" || got.RepoName != "acme/stack" ||
		!strings.HasPrefix(first, dir+"/src-") || got.Checkout != path.Base(first) {
		t.Fatalf("compose %q, commit %q, repository %q, checkout %q (cloned into %s)", got.Compose, got.Commit, got.RepoName, got.Checkout, first)
	}
	all := "\n" + strings.Join(e.fake.Calls(), "\n") + "\n"
	order := []string{
		"rm-all " + first, // nothing is there before the clone
		"git clone --depth 1 --single-branch --no-tags --branch main -- https://github.com/acme/stack " + g.checkout,
		"ls-tree HEAD -- deploy/compose.yaml",
		"--mount type=bind,source=" + g.checkout + ",target=" + g.checkout + ",readonly",
		"--project-directory " + g.checkout + "/deploy --file " + g.checkout + "/deploy/compose.yaml config --format json --no-interpolate",
		"ls-tree HEAD -- deploy/conf/site.conf",
		" pull --ignore-buildable",
		" --ansi never build\n",
		" up --detach",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(all[at:], want)
		if i < 0 {
			t.Fatalf("%q is missing or out of order in:%s", want, all)
		}
		at += i
	}
	// The Dockerfile and the build context were checked for links too.
	for _, want := range []string{"ls-tree HEAD -- deploy/Dockerfile", "ls-tree HEAD -- deploy\n"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", strings.TrimSpace(want))
		}
	}
	// A file of the repository is mounted read-only, and the built image
	// has a name of musdash's choosing.
	resolved, mode, _ := e.fake.File(dir + "/compose.resolved.json")
	if mode != 0o600 || !strings.Contains(resolved, `"read_only": true`) || !strings.Contains(resolved, `"image": "`+g.project+`-web"`) {
		t.Fatalf("resolved file (mode %o): %s", mode, resolved)
	}
	// The endpoint the file names got a generated domain at deployment.
	endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || !strings.HasSuffix(endpoints[0].Host, ".sslip.io") || endpoints[0].Port != 8080 {
		t.Fatalf("endpoints: %+v", endpoints)
	}
	// It waited its turn with the server's other builds.
	var lock string
	e.db.QueryRowContext(ctx, `SELECT lock_key FROM jobs WHERE kind = ? ORDER BY rowid DESC LIMIT 1`, JobService).Scan(&lock)
	if lock != "build:"+e.server.ID {
		t.Fatalf("lock key %q", lock)
	}

	// A mounted path that is a link in the repository is refused before
	// anything is built or started. The attempt was cloned next to the
	// checkout the running stack uses, which is left alone.
	g.links["deploy/conf"] = true
	before := len(e.fake.Calls())
	got = e.deployService(got, 10*time.Second)
	if !strings.Contains(got.LastError, "deploy/conf is a symbolic link") || got.Checkout != path.Base(first) {
		t.Fatalf("a linked mount source: %q (checkout %q)", got.LastError, got.Checkout)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.Contains(c, " build") || strings.Contains(c, " up --detach") {
			t.Fatalf("something ran for a stack with a linked mount: %s", c)
		}
		if c == "rm-all "+first {
			t.Fatal("a failed redeployment removed the checkout the running stack has mounted")
		}
	}
	second := g.checkout
	if second == first {
		t.Fatal("the redeployment was cloned over the checkout the running stack has mounted")
	}
	// A redeployment whose stack does not come up may have replaced some
	// containers and not others. Neither its checkout nor the ones before
	// it are touched, by it or by the attempt after it.
	g.links = map[string]bool{}
	g.failUp = true
	if got = e.deployService(got, 10*time.Second); !strings.Contains(got.LastError, "did not come up") {
		t.Fatalf("%+v", got)
	}
	third := g.checkout
	before = len(e.fake.Calls())
	e.deployService(got, 10*time.Second)
	for _, c := range e.fake.Calls()[before:] {
		for _, old := range []string{first, second, third} {
			if c == "rm-all "+old {
				t.Fatalf("a checkout that containers may still have mounted was removed after a failed start: %s", c)
			}
		}
	}
	// A redeployment that works moves the stack to its own checkout and
	// removes all the others.
	g.failUp = false
	before = len(e.fake.Calls())
	if got = e.deployService(got, 10*time.Second); got.Status != db.AppRunning || got.Checkout != path.Base(g.checkout) || got.LastError != "" {
		t.Fatalf("%+v", got)
	}
	after := strings.Join(e.fake.Calls()[before:], "\n")
	for _, old := range []string{first, second, third} {
		if !strings.Contains(after, "rm-all "+old) {
			t.Fatalf("an old checkout was kept: %s", old)
		}
	}
	if strings.Count(after, "rm-all "+g.checkout) != 1 || strings.Contains(after, "rm-all "+dir+"/compose.resolved.json") {
		t.Fatal("the clean-up removed the checkout in use, or something that is not a checkout")
	}
	// A path with characters git reads as more than a name is refused
	// before git is asked about it.
	g.compose = strings.Replace(gitComposeFile, "./conf/site.conf", "./:(top)conf", 1)
	odd := g.handle(e)
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, "config --format json") && !strings.Contains(line, "--no-interpolate") {
			out, err := odd(line, c)
			return strings.Replace(out, "/deploy/conf/site.conf", "/deploy/:(top)conf", 1), err
		}
		return odd(line, c)
	}
	if got = e.deployService(got, 10*time.Second); !strings.Contains(got.LastError, `names the path "deploy/:(top)conf"`) {
		t.Fatalf("a path with pathspec magic: %q", got.LastError)
	}
	for _, c := range e.fake.Calls() {
		if strings.Contains(c, "ls-tree") && strings.Contains(c, ":(top)") {
			t.Fatalf("git was asked about a path it reads as magic: %s", c)
		}
	}
	g.compose = gitComposeFile
	e.fake.Handle = g.handle(e)
	g.links = map[string]bool{"deploy/conf": true}
	// So is a Compose file that is itself a link.
	g.links = map[string]bool{"deploy/compose.yaml": true}
	if got = e.deployService(got, 10*time.Second); !strings.Contains(got.LastError, "deploy/compose.yaml is a symbolic link") {
		t.Fatalf("a linked Compose file: %q", got.LastError)
	}
	// And a repository without the file says so.
	g.links = map[string]bool{}
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "git clone") {
			return "", nil // nothing written
		}
		return g.handle(e)(line, c)
	}
	if got = e.deployService(got, 10*time.Second); !strings.Contains(got.LastError, "no file deploy/compose.yaml on the branch main") {
		t.Fatalf("a missing Compose file: %q", got.LastError)
	}

	// Deleting it removes the image built for it.
	e.fake.Handle = g.handle(e)
	if err := e.d.DestroyService(ctx, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(e.fake.Calls(), "\n"); !strings.Contains(all, "docker rmi "+g.project+"-web") {
		t.Fatal("the built image was not removed")
	}
}

// A stack that lives in a repository, against the real Docker and git: the
// file is read in the sandbox from the checkout, an image is built from the
// repository's Dockerfile, and a file of the repository is mounted
// read-only.
func TestGitServiceWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	repo, gitEnv := makeRepo(t, "stack", map[string]string{
		"deploy/compose.yaml": "services:\n  web:\n    build: ..\n    environment:\n      - SERVICE_FQDN_WEB_80\n    volumes:\n      - ../extra.html:/usr/share/nginx/html/extra.html\n",
		"Dockerfile":          "FROM nginx:alpine\nCOPY index.html /usr/share/nginx/html/index.html\n",
		"index.html":          "built: one\n",
		"extra.html":          "mounted: one\n",
	})
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}
	e.d.extraGitEnv = gitEnv
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://git.test/acme/stack.git", RepoName: "acme/stack", Branch: "main", ComposePath: "deploy/compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	project := ServiceProject(s.ID)
	t.Cleanup(func() {
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", project, "down", "--volumes", "--remove-orphans", "--timeout", "2"}})
		dk.RemoveImage(ctx, project+"-web")
	})
	commit := func(message string) {
		t.Helper()
		for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "--message", message}} {
			cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}

	got := e.deployService(s, 10*time.Minute)
	if got.Status != db.AppRunning {
		t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
	}
	endpoints, _ := e.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || endpoints[0].HostPort < portMin || !strings.HasSuffix(endpoints[0].Host, ".sslip.io") {
		t.Fatalf("endpoints: %+v", endpoints)
	}
	fetch := func(path string) string {
		t.Helper()
		var last error
		for range 20 {
			res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(endpoints[0].HostPort) + path)
			if err == nil {
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				return string(body)
			}
			last = err
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("GET %s: %v", path, last)
		return ""
	}
	if body := fetch("/"); !strings.Contains(body, "built: one") {
		t.Fatalf("the built image serves %q", body)
	}
	if body := fetch("/extra.html"); !strings.Contains(body, "mounted: one") {
		t.Fatalf("the mounted file serves %q", body)
	}
	if got.Commit == "" || !strings.Contains(got.Compose, "SERVICE_FQDN_WEB_80") {
		t.Fatalf("commit %q, compose %q", got.Commit, got.Compose)
	}
	// The container cannot write to the repository's file.
	container := project + "-web-1"
	if err := local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", container, "sh", "-c", "echo changed > /usr/share/nginx/html/extra.html"}}); err == nil {
		t.Fatal("a container wrote to a file of the checkout")
	}

	// A new commit and a redeployment: both the image and the file follow.
	os.WriteFile(filepath.Join(repo, "index.html"), []byte("built: two\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "extra.html"), []byte("mounted: two\n"), 0o644)
	commit("second")
	first, firstCheckout := got.Commit, got.Checkout
	got = e.deployService(got, 10*time.Minute)
	if got.Status != db.AppRunning || got.LastError != "" || got.Commit == first || got.Checkout == firstCheckout || got.Checkout == "" {
		t.Fatalf("%s %s (commit %s, checkout %s)\n%s", got.Status, got.LastError, got.Commit, got.Checkout, e.serviceLog(s))
	}
	if left, _ := filepath.Glob(filepath.Join(e.cfg.AppDir(s.ID), "src-*")); len(left) != 1 || filepath.Base(left[0]) != got.Checkout {
		t.Fatalf("checkouts on disk after a redeployment: %v, want only %s", left, got.Checkout)
	}
	if body := fetch("/"); !strings.Contains(body, "built: two") {
		t.Fatalf("after the second commit the image serves %q\n%s", body, e.serviceLog(s))
	}
	if body := fetch("/extra.html"); !strings.Contains(body, "mounted: two") {
		t.Fatalf("after the second commit the mounted file serves %q", body)
	}

	// A mounted path that is a link out of the repository is refused, and
	// what was running keeps running.
	os.Remove(filepath.Join(repo, "extra.html"))
	if err := os.Symlink("/etc/hostname", filepath.Join(repo, "extra.html")); err != nil {
		t.Fatal(err)
	}
	commit("a link")
	got = e.deployService(got, 10*time.Minute)
	if !strings.Contains(got.LastError, "extra.html is a symbolic link") || got.Status != db.AppRunning {
		t.Fatalf("a linked mount: status %s, error %q", got.Status, got.LastError)
	}
	if body := fetch("/"); !strings.Contains(body, "built: two") {
		t.Fatalf("the running stack was disturbed: %q", body)
	}

	// A file that reads the server through the sandbox's only window, the
	// checkout, finds nothing of the server there.
	os.Remove(filepath.Join(repo, "extra.html"))
	os.WriteFile(filepath.Join(repo, "extra.html"), []byte("mounted: three\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "deploy/compose.yaml"), []byte("include:\n  - /etc/hostname\nservices:\n  web:\n    build: ..\n"), 0o644)
	commit("an include")
	got = e.deployService(got, 10*time.Minute)
	host, _ := os.Hostname()
	if got.LastError == "" || (host != "" && strings.Contains(got.LastError, host)) {
		t.Fatalf("an include of a server file: %q", got.LastError)
	}

	if err := e.d.DestroyService(ctx, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if have, _ := dk.HasImage(ctx, project+"-web"); have {
		t.Fatal("the image built for the deleted service is still there")
	}
	t.Log("a stack from a repository: built, served, redeployed from a new commit, a linked mount refused, deleted with its image")
}

// An env_file is read by the sandbox, which sees the repository and
// nothing else: one that is not a file of the repository is refused before
// the stack gets whatever the sandbox's image has at that path.
func TestEnvFilesMustBeTheRepositorysOwn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://github.com/acme/stack", RepoName: "acme/stack", Branch: "main", ComposePath: "deploy/compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &gitStack{project: ServiceProject(s.ID), compose: gitComposeFile, links: map[string]bool{}}
	e.fake.Handle = g.handle(e)
	for files, want := range map[string]string{
		`[{"path":"/etc/os-release","required":true}]`:       `the env_file "/etc/os-release" is not a file of the repository`,
		`[{"path":"CHECKOUT/../other/env"}]`:                 `is not a file of the repository`,
		`["CHECKOUT/deploy/${NAME}.env"]`:                    `uses a variable`,
		`[{"path":"CHECKOUT/deploy/a b.env"}]`:               `use only letters`,
		`[{"path":"CHECKOUT/deploy/linked.env"}]`:            `deploy/linked.env is a symbolic link`,
		`[{"path":"CHECKOUT"}]`:                              `is not a file of the repository`,
		`[{"path":"CHECKOUT/deploy/app.env"},{"path":"/x"}]`: `the env_file "/x" is not a file`,
	} {
		g.envFiles, g.links = files, map[string]bool{"deploy/linked.env": true}
		before := len(e.fake.Calls())
		got := e.deployService(s, 10*time.Second)
		if !strings.Contains(got.LastError, want) {
			t.Errorf("%s: want %q in %q", files, want, got.LastError)
		}
		for _, c := range e.fake.Calls()[before:] {
			if strings.Contains(c, " up --detach") {
				t.Errorf("%s: the stack was started", files)
			}
		}
	}
	// One of the repository's own is what the sandbox reads.
	g.envFiles = `[{"path":"CHECKOUT/deploy/app.env","required":true}]`
	if got := e.deployService(s, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	if all := strings.Join(e.fake.Calls(), "\n"); !strings.Contains(all, "ls-tree HEAD -- deploy/app.env") {
		t.Error("the env_file was not checked for links")
	}
}

// A stack that was pasted has no files, so its env_file could only be the
// sandbox's own file at that path.
func TestPastedStackWithAnEnvFileIsRefused(t *testing.T) {
	e := newEnv(t)
	s, st := e.scriptedService(false)
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, "config --format json --no-interpolate") {
			return `{"name":"` + st.project + `","services":{"front":{"image":"nginx:alpine","env_file":[{"path":"/etc/os-release","required":true}]}}}`, nil
		}
		return st.handle(line, c)
	}
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "service front: env_file reads variables from a file") {
		t.Fatalf("%s %q", got.Status, got.LastError)
	}
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "docker compose") {
			t.Fatalf("Compose was run on the server for a refused file: %s", c)
		}
	}
}

// TestDomainsAddedToAStack: a domain a person gave to a service of a stack
// is published and routed like one the file names, a service and port is
// published once however many domains lead to it, and a domain for a
// service the file does not have stops the deployment with both names.
func TestDomainsAddedToAStack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	dir := e.cfg.AppDir(s.ID)

	// Saving the file again leaves the person's domains alone.
	cache, err := e.db.AddEndpoint(ctx, e.team, s.ID, "cache", 6379, "cache.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.AddEndpoint(ctx, e.team, s.ID, "front", 80, "second.example.test", true); err != nil {
		t.Fatal(err)
	}
	if err := e.d.PrepareService(ctx, s, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.db.ListEndpoints(ctx, s.ID); len(list) != 3 || list[0].Manual || !list[1].Manual || list[1].ID != cache.ID {
		t.Fatalf("endpoints after the file was saved: %+v", list)
	}

	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	byHost := map[string]db.Endpoint{}
	list, _ := e.db.ListEndpoints(ctx, s.ID)
	for _, ep := range list {
		byHost[ep.Host] = ep
	}
	file, second, redis := byHost["front.site.example.test"], byHost["second.example.test"], byHost["cache.example.test"]
	if file.HostPort == 0 || second.HostPort != file.HostPort || redis.HostPort == 0 || redis.HostPort == file.HostPort {
		t.Fatalf("host ports: the file's %d, the second domain's %d, the cache's %d", file.HostPort, second.HostPort, redis.HostPort)
	}
	resolved, _, _ := e.fake.File(dir + "/compose.resolved.json")
	if n := strings.Count(resolved, `"published": "`+strconv.Itoa(file.HostPort)+`"`); n != 1 {
		t.Fatalf("front's port 80 is published %d times, want once:\n%s", n, resolved)
	}
	if !strings.Contains(resolved, `"published": "`+strconv.Itoa(redis.HostPort)+`"`) || !strings.Contains(resolved, `"target": 6379`) {
		t.Fatalf("the cache's port is not published:\n%s", resolved)
	}
	routed := map[string]string{}
	for _, r := range e.routes().Routes {
		routed[r.Host] = r.Target
	}
	for host, ep := range byHost {
		if routed[host] != "127.0.0.1:"+strconv.Itoa(ep.HostPort) {
			t.Errorf("%s is routed to %q, want port %d", host, routed[host], ep.HostPort)
		}
	}
	// What the file holds is kept for the pages that offer its services.
	members := got.StackMembers()
	if len(members) != 2 || members[0].Name != "cache" || members[1].Name != "front" || members[1].Image != "nginx:alpine" {
		t.Fatalf("layout: %+v (%q)", members, got.Layout)
	}

	// A third domain for a target that is published takes its port at once.
	third, err := e.db.AddEndpoint(ctx, e.team, s.ID, "front", 80, "third.example.test", false)
	if err != nil || third.HostPort != file.HostPort {
		t.Fatalf("a domain for a published target: port %d, want %d (%v)", third.HostPort, file.HostPort, err)
	}
	// A redeployment moves nothing.
	if got = e.deployService(got, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	for _, ep := range mustEndpoints(t, e, s.ID) {
		if before, ok := byHost[ep.Host]; ok && ep.HostPort != before.HostPort {
			t.Errorf("%s moved from port %d to %d", ep.Host, before.HostPort, ep.HostPort)
		}
	}

	// A domain for a service the file does not have: nothing is started.
	if _, err := e.db.AddEndpoint(ctx, e.team, s.ID, "api", 3000, "api.example.test", true); err != nil {
		t.Fatal(err)
	}
	before := len(e.fake.Calls())
	got = e.deployService(got, 10*time.Second)
	if !strings.Contains(got.LastError, `api.example.test leads to the service "api", which the Compose file does not have (it has cache, front)`) {
		t.Fatalf("status %s, error %q", got.Status, got.LastError)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.Contains(c, " up ") || strings.Contains(c, " pull ") {
			t.Fatalf("something was started for a stack with a domain that leads nowhere: %s", c)
		}
	}

	// An endpoint the file names is not removed by hand; one a person added
	// is, and its domain with it.
	if err := e.db.DeleteEndpoint(ctx, s.ID, file.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("removing the file's endpoint: %v", err)
	}
	if err := e.db.DeleteEndpoint(ctx, s.ID, redis.ID); err != nil {
		t.Fatal(err)
	}
	for _, ep := range mustEndpoints(t, e, s.ID) {
		if ep.Host == "cache.example.test" {
			t.Fatal("the removed domain is still there")
		}
	}
}

func mustEndpoints(t *testing.T, e *env, id string) []db.Endpoint {
	t.Helper()
	list, err := e.db.ListEndpoints(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestLayoutGoesWithItsText: the recorded layout describes the stored text.
// A different text empties it, and a deployment that read an older text
// does not write its layout over a newer one.
func TestLayoutGoesWithItsText(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	if s = e.deployService(s, 10*time.Second); len(s.StackMembers()) != 2 {
		t.Fatalf("layout after a deployment: %q", s.Layout)
	}
	// Saved unchanged: kept.
	if err := e.db.UpdateServiceCompose(ctx, e.team, s); err != nil {
		t.Fatal(err)
	}
	if s, _ = e.db.ServiceByID(ctx, s.ID); len(s.StackMembers()) != 2 {
		t.Fatal("saving the same text emptied the layout")
	}
	old := s.Compose
	s.Compose += "\n# a comment\n"
	if err := e.db.UpdateServiceCompose(ctx, e.team, s); err != nil {
		t.Fatal(err)
	}
	if s, _ = e.db.ServiceByID(ctx, s.ID); s.Layout != "" {
		t.Fatalf("a changed text kept the layout of the one before: %q", s.Layout)
	}
	if err := e.db.SetServiceLayout(ctx, s.ID, old, []db.StackMember{{Name: "stale"}}); err != nil {
		t.Fatal(err)
	}
	if s, _ = e.db.ServiceByID(ctx, s.ID); s.Layout != "" {
		t.Fatalf("the layout of an older text was stored: %q", s.Layout)
	}

	// A stack from a repository: read from another branch it is another
	// file, and what the last one held says nothing about it.
	g, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "fromgit", Template: db.TemplateGit,
		RepoURL: "https://git.example.test/acme/stack.git", RepoName: "acme/stack", Branch: "main", ComposePath: "compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE services SET layout = '[{"name":"web"}]' WHERE id = ?`, g.ID); err != nil {
		t.Fatal(err)
	}
	g.AutoDeploy = true
	if err := e.db.UpdateServiceSource(ctx, e.team, g); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.ServiceByID(ctx, g.ID); len(got.StackMembers()) != 1 {
		t.Fatal("saving the same source emptied the layout")
	}
	g.Branch = "next"
	if err := e.db.UpdateServiceSource(ctx, e.team, g); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.ServiceByID(ctx, g.ID); got.Layout != "" {
		t.Fatalf("another branch kept the layout of the one before: %q", got.Layout)
	}
}

// TestOneLoopbackPortGoesToOneTarget: two endpoints that shared a target
// share its port. When they lead to different targets afterwards, only one
// of them keeps it: the same port for two container ports is refused by
// Docker when the stack is started.
func TestOneLoopbackPortGoesToOneTarget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	if _, err := e.db.AddEndpoint(ctx, e.team, s.ID, "front", 80, "second.example.test", false); err != nil {
		t.Fatal(err)
	}
	if s = e.deployService(s, 10*time.Second); s.Status != db.AppRunning {
		t.Fatalf("%+v", s)
	}
	list := mustEndpoints(t, e, s.ID)
	shared := list[0].HostPort
	if shared == 0 || list[1].HostPort != shared {
		t.Fatalf("endpoints: %+v", list)
	}
	// The second domain now leads to the cache, with the port it had.
	if _, err := e.db.Exec(`UPDATE service_endpoints SET compose_service = 'cache', port = 6379 WHERE id = ?`, list[1].ID); err != nil {
		t.Fatal(err)
	}
	if s = e.deployService(s, 10*time.Second); s.Status != db.AppRunning {
		t.Fatalf("%+v", s)
	}
	list = mustEndpoints(t, e, s.ID)
	if list[0].HostPort == list[1].HostPort || (list[0].HostPort != shared && list[1].HostPort != shared) {
		t.Fatalf("after the targets parted: ports %d and %d, of which one should be %d", list[0].HostPort, list[1].HostPort, shared)
	}
	resolved, _, _ := e.fake.File(e.cfg.AppDir(s.ID) + "/compose.resolved.json")
	if n := strings.Count(resolved, `"published": "`+strconv.Itoa(shared)+`"`); n != 1 {
		t.Fatalf("port %d is published %d times:\n%s", shared, n, resolved)
	}

	// A domain removed while a deployment is under way does not fail it.
	removed := false
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if !removed && strings.Contains(line, "config --format json --no-interpolate") {
			removed = true
			if err := e.db.DeleteEndpoint(ctx, s.ID, list[1].ID); err != nil {
				t.Error(err)
			}
		}
		return (&scriptedStack{project: ServiceProject(s.ID), dir: e.cfg.AppDir(s.ID)}).handle(line, c)
	}
	if s = e.deployService(s, 10*time.Second); s.Status != db.AppRunning || s.LastError != "" || !removed {
		t.Fatalf("a deployment during which a domain was removed: %s %q", s.Status, s.LastError)
	}
}

// TestSandboxImageIsFetchedByOneDeploymentAtATime: stacks first deployed
// at the same moment on a server that has never read a Compose file do not
// each pull the image that reads them. Docker fails pulls of one image that
// run side by side, and every one of the stacks failed with them.
func TestSandboxImageIsFetchedByOneDeploymentAtATime(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.newService(db.TemplateCustom, "first", scriptedCompose, false)
	second := e.newService(db.TemplateCustom, "second", scriptedCompose, false)

	var mu sync.Mutex
	have, pulling, pulls, together := false, 0, 0, false
	projectRE := regexp.MustCompile(`--project-name (\S+)`)
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker image inspect --format {{.Id}} docker:"):
			mu.Lock()
			defer mu.Unlock()
			if !have {
				return "", &runner.ExitError{Name: "docker", Code: 1, Stderr: "No such image"}
			}
			return "sha256:1\n", nil
		case strings.HasPrefix(line, "docker pull docker:"):
			mu.Lock()
			pulls++
			pulling++
			together = together || pulling > 1
			mu.Unlock()
			time.Sleep(150 * time.Millisecond)
			mu.Lock()
			pulling--
			have = true
			mu.Unlock()
			return "", nil
		}
		project := ""
		if m := projectRE.FindStringSubmatch(line); m != nil {
			project = m[1]
		}
		return (&scriptedStack{project: project, dir: e.cfg.AppDir(strings.TrimPrefix(project, "musdash-"))}).handle(line, c)
	}
	for _, s := range []db.Service{first, second} {
		if err := e.d.EnqueueService(ctx, s, false); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		a, _ := e.db.ServiceByID(ctx, first.ID)
		b, _ := e.db.ServiceByID(ctx, second.ID)
		if a.Status != db.AppDeploying && b.Status != db.AppDeploying {
			if a.Status != db.AppRunning || b.Status != db.AppRunning {
				t.Fatalf("%s (%s), %s (%s)", a.Status, a.LastError, b.Status, b.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the services did not settle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if together || pulls != 1 {
		t.Fatalf("the sandbox image was pulled %d times, side by side: %v", pulls, together)
	}
}

// TestAFailedPullSaysWhy: the reason Docker gave is in the error, not only
// that the command failed.
func TestAFailedPullSaysWhy(t *testing.T) {
	e := newEnv(t)
	s, st := e.scriptedService(false)
	st.fail = func(line string) error {
		if strings.Contains(line, " pull --ignore-buildable") {
			return &runner.ExitError{Name: "docker", Code: 1}
		}
		return nil
	}
	previous := e.fake.Handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, " pull --ignore-buildable") && c.Stderr != nil {
			io.WriteString(c.Stderr, " front Pulling\n\n front Error pull access denied for nginx, repository does not exist or may require 'docker login'\n")
		}
		return previous(line, c)
	}
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "pull the images") || !strings.Contains(got.LastError, "pull access denied for nginx") {
		t.Fatalf("%s: %q", got.Status, got.LastError)
	}
}
