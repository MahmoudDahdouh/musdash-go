package deploy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// The stacks under test/testdata/stacks are files as a person writes them
// for plain `docker compose`: no SERVICE_FQDN variable in any of them.

func readStack(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../test/testdata/stacks/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// publicPort finds a port of this machine that is free now and that a
// stack may publish.
func publicPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if ValidPublicPort(port) {
			return port
		}
	}
	t.Fatal("no free port a stack may publish")
	return 0
}

func httpGet(port int) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return string(body), nil
}

// TestStacksWithDocker deploys a small, a medium and a large stack on the
// local Docker daemon and gives their services domains from outside the
// file, as the Domains tab does.
//
//	MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestStacksWithDocker -v -timeout 20m
func TestStacksWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}
	down := func(s db.Service) {
		t.Cleanup(func() {
			local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", ServiceProject(s.ID), "down", "--volumes", "--remove-orphans", "--timeout", "2"}})
		})
	}
	endpoint := func(s db.Service, host string) db.Endpoint {
		t.Helper()
		for _, ep := range mustEndpoints(t, e, s.ID) {
			if ep.Host == host {
				return ep
			}
		}
		t.Fatalf("no endpoint for %s", host)
		return db.Endpoint{}
	}
	add := func(s db.Service, service string, port int, host string) {
		t.Helper()
		if _, err := e.db.AddEndpoint(ctx, e.team, s.ID, service, port, host, false); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("one service, no port named", func(t *testing.T) {
		s := e.newService(db.TemplateCustom, "one", readStack(t, "one.yaml"), false)
		down(s)
		got := e.deployService(s, 5*time.Minute)
		if got.Status != db.AppRunning || got.Members != "hello" {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		if m := got.StackMembers(); len(m) != 1 || m[0].Name != "hello" || len(m[0].Ports) != 0 {
			t.Fatalf("layout: %+v", m)
		}
		if n := len(mustEndpoints(t, e, s.ID)); n != 0 {
			t.Fatalf("a file with no address variable has %d endpoints", n)
		}
		// The domain is the person's, on a port the file never mentions.
		add(got, "hello", 80, "hello.one.example.test")
		if ep := endpoint(got, "hello.one.example.test"); ep.HostPort != 0 {
			t.Fatalf("a port nothing publishes yet has host port %d", ep.HostPort)
		}
		if got = e.deployService(got, 5*time.Minute); got.Status != db.AppRunning {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		ep := endpoint(got, "hello.one.example.test")
		if body, err := httpGet(ep.HostPort); err != nil || !strings.Contains(body, "Hostname:") {
			t.Fatalf("hello on 127.0.0.1:%d: %q %v", ep.HostPort, body, err)
		}
		routed := false
		for _, r := range e.routes().Routes {
			routed = routed || (r.Host == "hello.one.example.test" && r.Target == "127.0.0.1:"+strconv.Itoa(ep.HostPort))
		}
		if !routed {
			t.Fatalf("routes: %+v", e.routes().Routes)
		}
	})

	t.Run("a web service and its cache", func(t *testing.T) {
		text := readStack(t, "web-cache.yaml")
		// Without the value the file insists on, Compose says so in the
		// file's own words and nothing is started.
		missing := e.newService(db.TemplateCustom, "shop0", text, false)
		down(missing)
		if got := e.deployService(missing, 5*time.Minute); got.Status != db.AppFailed || !strings.Contains(got.LastError, "say what the page greets with") {
			t.Fatalf("a stack without its required variable: %s %s", got.Status, got.LastError)
		}

		outside := publicPort(t)
		s := e.newServiceWith(db.TemplateCustom, "shop", text, false, map[string]string{"GREETING": "Bonjour", "WEB_PORT": strconv.Itoa(outside)})
		down(s)
		add(s, "web", 8080, "shop.example.test")
		add(s, "web", 8080, "www.shop.example.test")
		got := e.deployService(s, 8*time.Minute)
		if got.Status != db.AppRunning || got.Members != "cache,web" {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		members := got.StackMembers()
		if len(members) != 2 || len(members[0].Ports) != 1 || members[0].Ports[0] != 6379 || len(members[1].Ports) != 1 || members[1].Ports[0] != 8080 {
			t.Fatalf("layout: %+v", members)
		}
		first, second := endpoint(got, "shop.example.test"), endpoint(got, "www.shop.example.test")
		if first.HostPort == 0 || first.HostPort != second.HostPort {
			t.Fatalf("two domains for one port: %d and %d", first.HostPort, second.HostPort)
		}
		for what, port := range map[string]int{"its domain's loopback port": first.HostPort, "the port its file publishes": outside} {
			if body, err := httpGet(port); err != nil || !strings.Contains(body, "Bonjour from web") {
				t.Fatalf("web on %s (%d): %q %v", what, port, body, err)
			}
		}
		// What the server says of the stack: both up, healthy, and the
		// file's own port named as reachable from outside.
		list, err := dk.StackContainers(ctx, s.ID)
		if err != nil || len(list) != 2 {
			t.Fatalf("containers: %+v %v", list, err)
		}
		for _, c := range list {
			if c.State != "running" || !strings.Contains(c.Status, "(healthy)") {
				t.Errorf("%s: %s, %s", c.Service, c.State, c.Status)
			}
		}
		if web := list[1]; web.Service != "web" || len(web.Published) != 1 || web.Published[0] != strconv.Itoa(outside)+" → 8080" {
			t.Fatalf("web's published ports: %+v", web)
		}
		// A domain for a service the file does not have: refused, and what
		// runs keeps answering.
		add(got, "api", 3000, "api.shop.example.test")
		if got = e.deployService(got, 5*time.Minute); !strings.Contains(got.LastError, `leads to the service "api", which the Compose file does not have`) || got.Status != db.AppRunning {
			t.Fatalf("a domain that leads nowhere: %s %s", got.Status, got.LastError)
		}
		if body, err := httpGet(first.HostPort); err != nil || !strings.Contains(body, "Bonjour") {
			t.Fatalf("after the refused deployment: %q %v", body, err)
		}
	})

	t.Run("twelve services", func(t *testing.T) {
		s := e.newService(db.TemplateCustom, "big", readStack(t, "twelve.yaml"), false)
		down(s)
		hosts := map[string]string{"gateway.big.example.test": "gateway", "api.big.example.test": "api", "admin.big.example.test": "admin", "also.big.example.test": "gateway"}
		for host, service := range hosts {
			add(s, service, 8080, host)
		}
		got := e.deployService(s, 8*time.Minute)
		// The one that ran once and ended does not make the stack degraded.
		if got.Status != db.AppRunning || len(got.MemberNames()) != 12 || len(got.StackMembers()) != 12 {
			t.Fatalf("%s %s (%d members)\n%s", got.Status, got.LastError, len(got.MemberNames()), e.serviceLog(s))
		}
		ports := map[int]bool{}
		for host, service := range hosts {
			ep := endpoint(got, host)
			ports[ep.HostPort] = true
			if body, err := httpGet(ep.HostPort); err != nil || strings.TrimSpace(body) != service {
				t.Errorf("%s on %d answers %q %v, want %s", host, ep.HostPort, body, err, service)
			}
		}
		if len(ports) != 3 {
			t.Fatalf("four domains for three services are on %d ports, want 3", len(ports))
		}
		list, err := dk.StackContainers(ctx, s.ID)
		if err != nil || len(list) != 12 {
			t.Fatalf("%d containers %v", len(list), err)
		}
		finished := 0
		for _, c := range list {
			if c.Service == "migrate" && c.State == "exited" && strings.HasPrefix(c.Status, "Exited (0)") {
				finished++
			} else if c.State != "running" {
				t.Errorf("%s: %s, %s", c.Service, c.State, c.Status)
			}
		}
		if finished != 1 {
			t.Fatal("the migration is not listed as finished")
		}
		// A redeployment moves no port.
		if got = e.deployService(got, 8*time.Minute); got.Status != db.AppRunning {
			t.Fatalf("%s %s", got.Status, got.LastError)
		}
		for host := range hosts {
			if ep := endpoint(got, host); !ports[ep.HostPort] {
				t.Errorf("%s moved to port %d", host, ep.HostPort)
			}
		}
	})
}
