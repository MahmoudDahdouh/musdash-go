package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// One container of a stack can be restarted. Which one is a choice among
// the containers the server lists for the stack: a name from the form
// that is not among them reaches no command.
func TestRestartOneContainerOfAStack(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	var restarts []string
	a.stackServer("front", "3000", func(line string) (string, error, bool) {
		switch {
		case strings.HasPrefix(line, "docker ps --all") && strings.Contains(line, "label=musdash.kind=service"):
			id := line[strings.Index(line, "musdash.resource=")+len("musdash.resource="):]
			id = id[:strings.IndexByte(id, ' ')]
			return "musdash-" + id + "-front-1\trunning\tUp 3 minutes\tmain:1\tfront\t\n" +
				"musdash-" + id + "-db-1\texited\tExited (1) 2 minutes ago\tdb:1\tdb\t\n" +
				"musdash-" + id + "-job-1\texited\tExited (0) 2 minutes ago\tjob:1\tjob\t\n", nil, true
		case strings.HasPrefix(line, "docker restart"):
			restarts = append(restarts, line)
			return "", nil, true
		}
		return "", nil, false
	})
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	base := a.servicePath(s.ID)
	front := "musdash-" + s.ID + "-front-1"

	// Each container has a menu, in the form a restart sends. Restart is in
	// it for what runs or fell over, asked first, and not for a container
	// that did its job and ended.
	_, list := a.get(base + "/containers")
	plain := html.UnescapeString(list)
	wantAll(t, "the containers", plain,
		`action="`+base+`/containers/restart"`, `name="container" value="`+front+`"`, `name="container" value="musdash-`+s.ID+`-db-1"`,
		`aria-label="Actions for front"`, "Restart front?", "Restart db?", "redeploy for that")
	if strings.Contains(plain, "Restart job?") {
		t.Error("a container that finished is offered a restart")
	}

	restart := func(name string) string {
		t.Helper()
		res, _ := a.post(base, base+"/containers/restart", url.Values{"container": {name}})
		wantStatus(t, res, http.StatusSeeOther)
		if got := res.Header.Get("Location"); got != base {
			t.Fatalf("the restart led to %s", got)
		}
		_, page := a.get(base)
		return html.UnescapeString(page)
	}
	if page := restart(front); !strings.Contains(page, "front-1 was restarted.") || len(restarts) != 1 || restarts[0] != "docker restart "+front {
		t.Fatalf("the restart ran %v", restarts)
	}
	// Names that are not the stack's own: another container, an option.
	for _, name := range []string{"musdash-other-web-1", "--all", "", front + " musdash-x"} {
		restarts = nil
		if page := restart(name); !strings.Contains(page, "no such container") || len(restarts) != 0 {
			t.Errorf("a restart of %q ran %v", name, restarts)
		}
	}
	// A stopped stack is started by a deployment, not one container at a
	// time, and its list offers none.
	if err := a.db.SetServiceState(ctx, s.ID, db.AppStopped, ""); err != nil {
		t.Fatal(err)
	}
	if page := restart(front); !strings.Contains(page, "The service is stopped") || len(restarts) != 0 {
		t.Errorf("a stopped stack's container was restarted: %v", restarts)
	}
	if _, list := a.get(base + "/containers"); strings.Contains(list, "data-confirm") {
		t.Error("a stopped stack offers restarts")
	}
	// While it is being deployed, neither.
	if err := a.db.SetServiceState(ctx, s.ID, db.AppDeploying, ""); err != nil {
		t.Fatal(err)
	}
	if page := restart(front); !strings.Contains(page, "busy with a deployment") || len(restarts) != 0 {
		t.Errorf("a container was restarted during a deployment: %v", restarts)
	}
	if err := a.db.SetServiceState(ctx, s.ID, db.AppRunning, ""); err != nil {
		t.Fatal(err)
	}
	// What the menu does not offer is not done for a request that asks
	// anyway: a container that did its job is run again by a deployment.
	if page := restart("musdash-" + s.ID + "-job-1"); !strings.Contains(page, "did its job and ended") || len(restarts) != 0 {
		t.Errorf("a finished container was run again: %v", restarts)
	}

	// Another team's stack is not found, and nothing is run on its server.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, err := a.db.CreateService(ctx, "otherteam", db.Service{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "theirs", Template: db.TemplateCustom, Compose: ownCompose})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/projects/" + p.ID + "/env/" + envs[0].ID + "/service/" + other.ID + "/containers/restart",
		// Under our own project's address too.
		strings.Replace(base, s.ID, other.ID, 1) + "/containers/restart",
	} {
		res, _ := a.post(base, path, url.Values{"container": {"musdash-" + other.ID + "-front-1"}})
		wantStatus(t, res, http.StatusNotFound)
	}
	if len(restarts) != 0 {
		t.Errorf("a restart ran for another team's stack: %v", restarts)
	}
}

// A stack's page says where it comes from and which server it is on.
func TestAStackSaysWhereItIsFrom(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	res, body := a.post("/projects/"+projectID+"/env/"+env.ID+"/service/new?template=git", "/projects/"+projectID+"/env/"+env.ID+"/service", gitServiceForm(env, "shop"))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", res.StatusCode, body)
	}
	base := res.Header.Get("Location")
	_, page := a.get(base)
	header := page[strings.Index(page, `id="service-header"`):]
	header = header[:strings.Index(header, "</header>")]
	if !strings.Contains(header, "acme/stack @ main") || strings.Contains(header, "Your own Compose file") {
		t.Errorf("the header of a stack from Git:\n%s", header)
	}
	wantAll(t, "the Stack card", page, ">Server<", ">localhost<")

	pasted := a.newService(projectID, env, "pasted", nil)
	_, page = a.get(a.servicePath(pasted.ID))
	if !strings.Contains(page, "Your own Compose file") {
		t.Error("a pasted stack does not say what it is")
	}
}

func TestTerminalChoices(t *testing.T) {
	got := pages.TerminalChoices("abc", []string{
		"musdash-abc-worker-2", "musdash-abc-web-1", "musdash-abc-worker-1", "musdash-abc-my-api-1", "proxy-1", "musdash-abc-v2",
	})
	want := []pages.TerminalChoice{
		{Container: "musdash-abc-my-api-1", Label: "my-api"},
		// Named by the file itself: kept whole.
		{Container: "proxy-1", Label: "proxy-1"},
		{Container: "musdash-abc-v2", Label: "v2"},
		{Container: "musdash-abc-web-1", Label: "web"},
		{Container: "musdash-abc-worker-1", Label: "worker-1"},
		{Container: "musdash-abc-worker-2", Label: "worker-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TerminalChoices:\n got %v\nwant %v", got, want)
	}
}

func TestSaid(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		"the health check did not pass":      "The health check did not pass",
		"Pull access denied":                 "Pull access denied",
		"docker exited with status 1":        "Docker exited with status 1",
		"éclair":                             "éclair",
		"8080 is taken":                      "8080 is taken",
		"pull nginx: denied\nsecond line ok": "Pull nginx: denied\nsecond line ok",
	} {
		if got := pages.Said(in); got != want {
			t.Errorf("Said(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only an app built from a repository has a commit to name: the card of a
// Docker image app has no such row.
func TestAnImageAppNamesNoCommit(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	image := a.newApp(projectID, env, "web", false, nil)
	if _, page := a.get(a.appPath(image)); strings.Contains(page, ">Commit<") || !strings.Contains(page, ">Image<") {
		t.Error("the card of a Docker image app names a commit")
	}
	built := a.newGitApp(projectID, env, "built", nil)
	if _, page := a.get(a.appPath(built.ID)); !strings.Contains(page, ">Commit<") {
		t.Error("the card of an app from Git names no commit")
	}
}

// A deployment that waits its turn has no log yet. Its page says that it
// is waiting, where it would show an empty box.
func TestAQueuedStackSaysItIsWaiting(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	s := a.newService(projectID, env, "site", nil)
	base := a.servicePath(s.ID)

	// Over without a log, as one that could not be queued is: nothing to
	// wait for.
	over, _ := a.db.CreateServiceDeployment(ctx, s.ID, db.TriggerManual)
	a.db.FinishServiceDeployment(ctx, over.ID, db.DeployFailed, "it could not be queued")
	if _, stream := a.get(base + "/deployments/" + over.ID + "/stream"); strings.Contains(stream, "Waiting for its turn") || !strings.Contains(stream, "ended before it wrote anything") || !strings.Contains(stream, "event: done") {
		t.Errorf("a deployment that is over says it waits:\n%s", stream)
	}
	// Queued: said at once. The stream ends when the deployment does.
	queued, _ := a.db.CreateServiceDeployment(ctx, s.ID, db.TriggerManual)
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.db.FinishServiceDeployment(ctx, queued.ID, db.DeployFailed, "it never started")
	}()
	if _, stream := a.get(base + "/deployments/" + queued.ID + "/stream"); !strings.Contains(stream, "Waiting for its turn") || !strings.Contains(stream, "event: done") {
		t.Errorf("a queued deployment's log:\n%s", stream)
	}
}
