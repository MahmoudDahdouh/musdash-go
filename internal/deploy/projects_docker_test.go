package deploy

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// The folders under test/testdata/projects are repositories as a person
// has them: one with a Dockerfile, and two with a Compose file written for
// plain `docker compose up`, a small one and a large one.

// readProject reads a test project as the files of a repository.
func readProject(t *testing.T, name string) map[string]string {
	t.Helper()
	root := "../../test/testdata/projects/" + name
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("read the project %s: %d files, %v", name, len(files), err)
	}
	return files
}

// TestProjectsWithDocker deploys each test project from a Git repository
// on the local Docker daemon, the way the dashboard's Git form does: the
// first as an app built from its Dockerfile, the other two as stacks
// (Build with: Docker Compose).
//
//	MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestProjectsWithDocker -v -timeout 30m
func TestProjectsWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}

	t.Run("a Dockerfile", func(t *testing.T) {
		e := newEnv(t)
		_, gitEnv := makeRepo(t, "dockerfile", readProject(t, "dockerfile"))
		e.d.Runners = fixedRunners{local}
		e.d.Probe = newLocalProbe()
		e.d.extraGitEnv = gitEnv
		e.d.healthEvery = 300 * time.Millisecond
		e.db.Exec(`UPDATE apps SET port = 8080, health_timeout = 90, health_path = '/' WHERE id = ?`, e.app.ID)
		e.gitApp(func(a *db.App) { a.RepoURL = "https://git.test/acme/dockerfile.git" })
		t.Cleanup(func() {
			out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"ps", "--all", "--quiet", "--filter", "label=" + docker.LabelResource + "=" + e.app.ID}})
			for _, id := range strings.Fields(string(out)) {
				dk.Remove(ctx, id)
			}
			tags, _ := dk.ImageTags(ctx, ImageRepository(e.app.ID))
			for _, tag := range tags {
				dk.RemoveImage(ctx, ImageRepository(e.app.ID)+":"+tag)
			}
			local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", NetworkName(e.app.EnvironmentID)}})
		})
		dep := e.deployWithin(15 * time.Minute)
		if dep.Status != db.DeploySuccess {
			t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
		}
		if body, err := httpGet(e.reload().HostPort); err != nil || !strings.Contains(body, "built from its Dockerfile") {
			t.Fatalf("the app serves %q %v", body, err)
		}
	})

	// stack makes a service from Git out of a project, as the Git form
	// does with Docker Compose chosen.
	stack := func(t *testing.T, e *env, project, file string, typed map[string]string) db.Service {
		t.Helper()
		_, gitEnv := makeRepo(t, project, readProject(t, project))
		e.d.Runners = fixedRunners{local}
		e.d.extraGitEnv = gitEnv
		s, err := e.db.CreateService(ctx, e.team, db.Service{
			EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: project, Template: db.TemplateGit,
			RepoURL: "https://git.test/acme/" + project + ".git", RepoName: "acme/" + project, Branch: "main", ComposePath: file,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(typed) > 0 {
			sealed, err := e.d.SealServiceVariables(typed)
			if err == nil {
				err = e.db.SetServiceVariables(ctx, s.ID, sealed)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		name := ServiceProject(s.ID)
		t.Cleanup(func() {
			local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "--project-name", name, "down", "--volumes", "--remove-orphans", "--rmi", "local", "--timeout", "2"}})
		})
		s, _ = e.db.ServiceByID(ctx, s.ID)
		return s
	}
	// reach gives a service of a stack a domain and deploys, then asks for
	// the page through the loopback port the proxy would use.
	reach := func(t *testing.T, e *env, s db.Service, service string, port int, host, path string) string {
		t.Helper()
		if _, err := e.db.AddEndpoint(ctx, e.team, s.ID, service, port, host, false); err != nil {
			t.Fatal(err)
		}
		if got := e.deployService(s, 10*time.Minute); got.Status != db.AppRunning {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		for _, ep := range mustEndpoints(t, e, s.ID) {
			if ep.Host == host {
				body, err := httpGetPath(ep.HostPort, path)
				if err != nil {
					t.Fatalf("%s on 127.0.0.1:%d: %v", host, ep.HostPort, err)
				}
				return body
			}
		}
		t.Fatalf("no endpoint for %s", host)
		return ""
	}

	t.Run("a small Compose project", func(t *testing.T) {
		e := newEnv(t)
		s := stack(t, e, "compose-small", "compose.yaml", nil)
		got := e.deployService(s, 15*time.Minute)
		if got.Status != db.AppRunning || got.Members != "web" || got.Commit == "" {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		// The file publishes port 80: left out, said, and the port is what
		// the Domains dialog offers.
		if log := e.serviceLog(s); !strings.Contains(log, "Port 80 of the server is the proxy's, so web is not published on it") {
			t.Errorf("the log does not say what became of port 80:\n%s", log)
		}
		if m := got.StackMembers(); len(m) != 1 || !m[0].Built || len(m[0].Ports) != 1 || m[0].Ports[0] != 8080 {
			t.Fatalf("layout: %+v", m)
		}
		if body := reach(t, e, got, "web", 8080, "small.example.test", "/"); !strings.Contains(body, "built by Compose from the repository") {
			t.Fatalf("web serves %q", body)
		}
	})

	t.Run("a large Compose project", func(t *testing.T) {
		e := newEnv(t)
		// Its file insists on a value: without it nothing is built.
		s := stack(t, e, "compose-large", "docker-compose.yml", nil)
		if got := e.deployService(s, 10*time.Minute); got.Status == db.AppRunning || !strings.Contains(got.LastError, "say what the api greets with") {
			t.Fatalf("without GREETING: %s %s", got.Status, got.LastError)
		}
		sealed, err := e.d.SealServiceVariables(map[string]string{"GREETING": "Bonjour"})
		if err == nil {
			err = e.db.SetServiceVariables(ctx, s.ID, sealed)
		}
		if err != nil {
			t.Fatal(err)
		}
		got := e.deployService(s, 20*time.Minute)
		if got.Status != db.AppRunning {
			t.Fatalf("%s %s\n%s", got.Status, got.LastError, e.serviceLog(s))
		}
		if n := len(got.StackMembers()); n != 10 {
			t.Fatalf("%d services in the layout, want 10 (%s)", n, got.Members)
		}
		// The password was generated when the file was read, and the
		// extension field reached the five services that share it.
		vars, err := e.d.ServiceVariables(got)
		if err != nil || len(vars["SERVICE_PASSWORD_DB"]) < 16 {
			t.Fatalf("the generated password: %v", err)
		}
		list, err := dk.StackContainers(ctx, s.ID)
		if err != nil || len(list) != 10 {
			t.Fatalf("containers: %d %v", len(list), err)
		}
		for _, c := range list {
			switch {
			case c.Service == "migrate":
				if c.State != "exited" || !strings.HasPrefix(c.Status, "Exited (0)") {
					t.Errorf("migrate: %s, %s", c.State, c.Status)
				}
			case c.State != "running":
				t.Errorf("%s: %s, %s", c.Service, c.State, c.Status)
			case len(c.Published) != 0:
				t.Errorf("%s is published on the server: %v", c.Service, c.Published)
			}
		}
		log := e.serviceLog(s)
		for _, want := range []string{"Port 80 of the server is the proxy's, so gateway", "Port 443 of the server is the proxy's, so gateway", "Building images"} {
			if !strings.Contains(log, want) {
				t.Errorf("the log does not say %q", want)
			}
		}
		// The gateway through a domain: its built page, and the file of
		// the repository that is mounted into it. The API, built with the
		// argument, through another.
		if body := reach(t, e, got, "gateway", 8080, "large.example.test", "/"); !strings.Contains(body, "the gateway, built from the repository") {
			t.Fatalf("the gateway serves %q", body)
		}
		for _, ep := range mustEndpoints(t, e, s.ID) {
			if body, err := httpGetPath(ep.HostPort, "/extra.html"); err != nil || !strings.Contains(body, "a file of the repository, mounted") {
				t.Fatalf("the mounted file: %q %v", body, err)
			}
		}
		if body := reach(t, e, got, "api", 3000, "api.large.example.test", "/"); !strings.Contains(body, "Bonjour from the api") {
			t.Fatalf("the api serves %q", body)
		}
		// One of the five that share their settings.
		if body := reach(t, e, got, "docs", 8080, "docs.large.example.test", "/"); strings.TrimSpace(body) == "" {
			t.Fatal("docs serves nothing")
		}
		t.Logf("ten services from one repository: two built, %s domains", strconv.Itoa(len(mustEndpoints(t, e, s.ID))))
	})
}
