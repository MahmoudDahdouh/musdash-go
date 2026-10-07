package web

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
)

// newDatabase creates a database through the form and waits for it to
// start. It returns the stored row.
func (a *app) newDatabase(projectID string, env db.Environment, engine, name string, extra url.Values) db.Database {
	a.t.Helper()
	tpl, _ := catalog.Database(engine)
	form := url.Values{"engine": {engine}, "name": {name}, "image": {tpl.Image}}
	for k, v := range extra {
		form[k] = v
	}
	page := "/projects/" + projectID + "/env/" + env.ID + "/database/new?engine=" + engine
	res, body := a.post(page, "/projects/"+projectID+"/env/"+env.ID+"/database", form)
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("create database: %d\n%s", res.StatusCode, body)
	}
	return a.waitDatabase(createdID(res, db.KindDatabase))
}

// waitDatabase waits until a database is no longer starting.
func (a *app) waitDatabase(id string) db.Database {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m, err := a.db.DatabaseByID(context.Background(), id)
		if err != nil {
			a.t.Fatal(err)
		}
		if m.Status != db.AppDeploying {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatal("the database did not settle")
	return db.Database{}
}

func TestCreateDatabase(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")

	// The Add resource page offers every engine; each leads to its form.
	res, _ := a.get("/projects/" + projectID + "/env/" + env.ID + "/database/new")
	wantRedirect(t, res, "/projects/"+projectID+"/env/"+env.ID+"/new")
	res, engines := a.get("/projects/" + projectID + "/env/" + env.ID + "/new")
	wantStatus(t, res, http.StatusOK)
	for _, tpl := range catalog.Databases() {
		if !strings.Contains(engines, "engine="+tpl.Engine) || !strings.Contains(engines, tpl.Label) {
			t.Errorf("the engine list is missing %s", tpl.Label)
		}
	}
	_, form := a.get("/projects/" + projectID + "/env/" + env.ID + "/database/new?engine=postgres")
	if !strings.Contains(form, `value="postgres:17-alpine"`) {
		t.Fatal("the form does not offer the default image")
	}
	res, _ = a.get("/projects/" + projectID + "/env/" + env.ID + "/database/new?engine=oracle")
	wantStatus(t, res, http.StatusNotFound)

	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	if m.Status != db.AppRunning || m.Container != deploy.DatabaseContainer(m.ID) || m.PublicPort != 0 {
		t.Fatalf("after create: %+v", m)
	}
	if m.Username != "postgres" || m.DBName != "postgres" || m.Image != "postgres:17-alpine" {
		t.Fatalf("defaults: %+v", m)
	}
	pass, err := a.server.Box.OpenString(m.Password)
	if err != nil || !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(pass) {
		t.Fatalf("generated password %q: %v", pass, err)
	}
	if strings.Contains(m.Password, pass) {
		t.Fatal("the password is stored in the clear")
	}
	for _, c := range a.fake.Calls() {
		if strings.Contains(c, pass) {
			t.Fatalf("the password is on a command line: %s", c)
		}
	}

	// The overview hands out the connection string without drawing the
	// password on the screen.
	res, page := a.get(a.databasePath(m.ID))
	wantStatus(t, res, http.StatusOK)
	page = html.UnescapeString(page)
	wantURL := "postgres://postgres:" + pass + "@maindb:5432/postgres"
	if !strings.Contains(page, `data-copy="`+wantURL+`"`) {
		t.Fatal("the connection string cannot be copied from the overview")
	}
	if !strings.Contains(page, "postgres://postgres:••••••••@maindb:5432/postgres") {
		t.Fatal("the masked connection string is not shown")
	}
	if strings.Contains(regexp.MustCompile(`data-copy="[^"]*"`).ReplaceAllString(page, ""), pass) {
		t.Fatal("the password is drawn on the page outside the copy buttons")
	}
	if !strings.Contains(page, "Only this environment can reach the database") || !strings.Contains(page, "Running") {
		t.Fatal("the overview does not say the database is private and running")
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("a page holding a password may be cached")
	}

	// It is listed with the project, and nothing but its own page holds the
	// password.
	_, list := a.get("/projects/" + projectID + "/env/" + env.ID)
	if !strings.Contains(list, "maindb") || !strings.Contains(list, "PostgreSQL") || !strings.Contains(list, a.databasePath(m.ID)) {
		t.Fatal("the project page does not list the database")
	}
	for _, path := range []string{"/projects/" + projectID + "/env/" + env.ID, a.databasePath(m.ID) + "/settings", a.databasePath(m.ID) + "/logs", a.databasePath(m.ID) + "/status"} {
		if _, body := a.get(path); strings.Contains(body, pass) {
			t.Errorf("%s contains the password", path)
		}
	}
}

func TestDatabaseFormValidation(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", false, nil)
	otherProject, otherEnv := a.project("Other")
	page := "/projects/" + projectID + "/env/" + env.ID + "/database/new?engine=redis"
	create := "/projects/" + projectID + "/env/" + env.ID + "/database"

	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"name": {"Has Space"}}, "Use lowercase letters"},
		{url.Values{"name": {""}}, "Use lowercase letters"},
		{url.Values{"image": {"redis:7 --privileged"}}, "Enter an image name"},
		{url.Values{"image": {"-v"}}, "Enter an image name"},
		// The name is the address on the environment's network, shared with apps.
		{url.Values{"name": {"web"}}, "already has an app, database or service called web"},
	} {
		form := url.Values{"engine": {"redis"}, "name": {"cache"}, "image": {"redis:7-alpine"}}
		for k, v := range c.form {
			form[k] = v
		}
		res, body := a.post(page, create, form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), c.want) {
			t.Errorf("%v: status %d, want 422 with %q", c.form, res.StatusCode, c.want)
		}
	}
	valid := url.Values{"engine": {"redis"}, "name": {"cache"}, "image": {"redis:7-alpine"}}
	unknown := url.Values{}
	for k, v := range valid {
		unknown[k] = v
	}
	unknown.Set("engine", "oracle")
	if res, _ := a.post(page, create, unknown); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown engine: %d, want 404", res.StatusCode)
	}
	// An environment is only reached through its own project.
	if res, _ := a.post(page, "/projects/"+projectID+"/env/"+otherEnv.ID+"/database", valid); res.StatusCode != http.StatusNotFound {
		t.Errorf("environment of another project: %d, want 404", res.StatusCode)
	}
	_ = otherProject
	var n int
	a.db.QueryRow(`SELECT count(*) FROM databases`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d databases were created by rejected forms", n)
	}

	// And the other way round: an app cannot take a database's name.
	a.newDatabase(projectID, env, "redis", "cache", nil)
	res, body := a.post("/projects/"+projectID+"/env/"+env.ID+"/app/new", "/projects/"+projectID+"/env/"+env.ID+"/app",
		url.Values{"name": {"cache"}, "image": {"nginx"}, "port": {"80"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already has an app, database or service called cache") {
		t.Fatalf("an app took a database's name: %d", res.StatusCode)
	}
}

func TestDatabaseSettingsAndPublicPort(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	settings := a.databasePath(m.ID) + "/settings"
	save := func(change url.Values) (*http.Response, string) {
		form := url.Values{"image": {"postgres:17-alpine"}}
		for k, v := range change {
			form[k] = v
		}
		res, body := a.post(settings, settings, form)
		return res, html.UnescapeString(body)
	}

	// Switched on without a number: the first free port is picked. The
	// running database is restarted at once, so what the page says and what
	// the server does never differ.
	res, _ := save(url.Values{"public": {"1"}, "memory_mb": {"512"}, "cpus": {"1.5"}, "image": {"postgres:16-alpine"}})
	wantRedirect(t, res, settings)
	got := a.waitDatabase(m.ID)
	if got.PublicPort != 30000 || got.MemoryMB != 512 || got.CPUs != 1.5 || got.Image != "postgres:16-alpine" || got.Status != db.AppRunning {
		t.Fatalf("stored: %+v", got)
	}
	calls := a.fake.Calls()
	if run := calls[lastIndexOfCall(calls, "docker run")]; !strings.Contains(run, "--publish 0.0.0.0:30000:5432") || !strings.Contains(run, "--memory 512m") || !strings.Contains(run, " postgres:16-alpine") {
		t.Fatalf("the restarted container does not have the new settings:\n%s", run)
	}
	// Saving the same again restarts nothing and keeps the port: connection
	// strings must stay valid.
	restarts := countCalls(a.fake.Calls(), "docker run")
	save(url.Values{"public": {"1"}, "memory_mb": {"512"}, "cpus": {"1.5"}, "image": {"postgres:16-alpine"}})
	if got := a.waitDatabase(m.ID); got.PublicPort != 30000 || countCalls(a.fake.Calls(), "docker run") != restarts {
		t.Fatalf("after saving the same settings: %+v, %d runs", got, countCalls(a.fake.Calls(), "docker run"))
	}
	save(url.Values{"public": {"1"}})
	if got := a.waitDatabase(m.ID); got.PublicPort != 30000 || got.MemoryMB != 0 || got.Image != "postgres:17-alpine" {
		t.Fatalf("after a second change: %+v", got)
	}

	_, page := a.get(a.databasePath(m.ID))
	page = html.UnescapeString(page)
	if !strings.Contains(page, "@SERVER_IP:30000/postgres") || !strings.Contains(page, "Replace SERVER_IP") {
		t.Fatal("the public connection string is not shown")
	}
	a.db.Exec(`UPDATE servers SET ip = '203.0.113.9'`)
	if _, page := a.get(a.databasePath(m.ID)); !strings.Contains(page, "@203.0.113.9:30000/postgres") {
		t.Fatal("the public connection string does not use the server's address")
	}

	// A second database gets the next port, and cannot take the first.
	second := a.newDatabase(projectID, env, "redis", "cache", url.Values{"public": {"1"}})
	if second.PublicPort != 30001 {
		t.Fatalf("second public port %d", second.PublicPort)
	}
	if indexOfCall(a.fake.Calls(), "--publish 0.0.0.0:30001:6379") < 0 {
		t.Fatal("a database created as public does not publish its port")
	}
	for value, want := range map[string]string{
		"30001": "Another database on this server already uses port 30001",
		"80":    "Enter a port from 1024 to 65535",
		"25000": "Enter a port from 1024 to 65535",
		"70000": "Enter a port from 1024 to 65535",
		"abc":   "Enter a port from 1024 to 65535",
		"-5432": "Enter a port from 1024 to 65535",
	} {
		res, body := save(url.Values{"public": {"1"}, "public_port": {value}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("public port %q: status %d, want 422 with %q", value, res.StatusCode, want)
		}
	}
	for field, bad := range map[string]string{"memory_mb": "3", "cpus": "NaN", "image": "postgres --privileged"} {
		if res, _ := save(url.Values{field: {bad}}); res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s=%q: status %d, want 422", field, bad, res.StatusCode)
		}
	}
	if got, _ := a.db.DatabaseByID(ctx, m.ID); got.PublicPort != 30000 || got.Image != "postgres:17-alpine" {
		t.Fatalf("a rejected form changed the database: %+v", got)
	}

	// A chosen port, then off again. Switching it off closes it now, not at
	// some later restart.
	res, _ = save(url.Values{"public": {"1"}, "public_port": {"15432"}})
	wantRedirect(t, res, settings)
	if got := a.waitDatabase(m.ID); got.PublicPort != 15432 {
		t.Fatalf("chosen port: %+v", got)
	}
	res, _ = save(url.Values{"public_port": {"15432"}})
	wantRedirect(t, res, settings)
	if got := a.waitDatabase(m.ID); got.PublicPort != 0 || got.Status != db.AppRunning {
		t.Fatalf("after switching the public port off: %+v", got)
	}
	calls = a.fake.Calls()
	if run := calls[lastIndexOfCall(calls, "docker run")]; strings.Contains(run, "--publish") {
		t.Fatalf("the port is still published after being switched off:\n%s", run)
	}
	if _, page := a.get(a.databasePath(m.ID)); !strings.Contains(page, "Only this environment can reach the database") {
		t.Fatal("the overview does not say the database is private again")
	}

	// A stopped database is not started by a change of settings.
	a.post(settings, a.databasePath(m.ID)+"/stop", nil)
	runs := countCalls(a.fake.Calls(), "docker run")
	save(url.Values{"memory_mb": {"256"}})
	if got, _ := a.db.DatabaseByID(ctx, m.ID); got.Status != db.AppStopped || got.MemoryMB != 256 || countCalls(a.fake.Calls(), "docker run") != runs {
		t.Fatalf("a stopped database after a settings change: %+v", got)
	}
}

func lastIndexOfCall(calls []string, part string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.Contains(calls[i], part) {
			return i
		}
	}
	return -1
}

func countCalls(calls []string, part string) (n int) {
	for _, c := range calls {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

func indexOfCall(calls []string, part string) int {
	for i, c := range calls {
		if strings.Contains(c, part) {
			return i
		}
	}
	return -1
}

func TestStopStartAndDeleteDatabase(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	keep := a.newDatabase(projectID, env, "postgres", "keepdata", nil)
	drop := a.newDatabase(projectID, env, "mariadb", "dropdata", nil)
	base := a.databasePath(keep.ID)

	res, _ := a.post(base, base+"/stop", nil)
	wantRedirect(t, res, base)
	if got, _ := a.db.DatabaseByID(ctx, keep.ID); got.Status != db.AppStopped || got.Container != "" {
		t.Fatalf("after stop: %+v", got)
	}
	_, page := a.get(base)
	if !strings.Contains(page, "Stopped") || !strings.Contains(page, "Start") || strings.Contains(page, "Restart") {
		t.Fatal("a stopped database does not offer Start")
	}
	res, _ = a.get(base + "/logs/stream")
	wantStatus(t, res, http.StatusConflict)

	res, _ = a.post(base, base+"/start", nil)
	wantRedirect(t, res, base)
	if got := a.waitDatabase(keep.ID); got.Status != db.AppRunning {
		t.Fatalf("after start: %+v", got)
	}
	res, stream := a.get(base + "/logs/stream")
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(stream, "<script>") || !strings.Contains(stream, "&lt;script&gt;") {
		t.Fatalf("log output was not escaped:\n%s", stream)
	}

	// Neither the environment nor the project can go while it holds a
	// database.
	projectSettings := "/projects/" + projectID // its Settings are a dialog of its page
	res, _ = a.post(projectSettings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, projectSettings)
	if _, err := a.db.Project(ctx, firstTeam(t, a), projectID); err != nil {
		t.Fatal("a project with a database in it was deleted")
	}

	// Deleting needs the name typed; the data goes only when asked.
	settings := base + "/settings"
	res, _ = a.post(settings, base+"/delete", url.Values{"confirm": {"wrong"}, "delete_data": {"1"}})
	wantRedirect(t, res, settings)
	if _, err := a.db.DatabaseByID(ctx, keep.ID); err != nil {
		t.Fatal("deleted without the name being typed")
	}
	res, _ = a.post(settings, base+"/delete", url.Values{"confirm": {"keepdata"}})
	wantRedirect(t, res, "/projects/"+projectID+"/env/"+env.ID)
	if _, err := a.db.DatabaseByID(ctx, keep.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if indexOfCall(a.fake.Calls(), "docker volume rm") >= 0 {
		t.Fatal("the data volume was deleted without being asked")
	}
	res, _ = a.get(base)
	wantStatus(t, res, http.StatusNotFound)

	dropSettings := a.databasePath(drop.ID) + "/settings"
	res, _ = a.post(dropSettings, a.databasePath(drop.ID)+"/delete", url.Values{"confirm": {"dropdata"}, "delete_data": {"1"}})
	wantRedirect(t, res, "/projects/"+projectID+"/env/"+env.ID)
	if indexOfCall(a.fake.Calls(), "docker volume rm "+deploy.DatabaseVolume(drop.ID)) < 0 {
		t.Fatal("the data volume was kept although its deletion was asked for")
	}

	res, _ = a.post(projectSettings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, "/projects")
}

func TestOtherTeamsDatabaseIsNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, err := a.db.CreateDatabase(ctx, "otherteam", db.Database{
		EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-db", Engine: "postgres", Image: "postgres:17-alpine",
		Username: "postgres", Password: a.seal("their-password"), DBName: "postgres",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := a.databasePath(other.ID)
	for _, path := range []string{"", "/status", "/logs", "/logs/stream", "/settings"} {
		res, body := a.get(base + path)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "secret-db") || strings.Contains(body, "their-password") {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	token := a.csrf("/projects")
	for path, form := range map[string]url.Values{
		"/start":    {},
		"/stop":     {},
		"/settings": {"image": {"postgres:17-alpine"}, "public": {"1"}},
		"/delete":   {"confirm": {"secret-db"}, "delete_data": {"1"}},
	} {
		form.Set("_csrf", token)
		if res, _ := a.postRaw(a.client, base+path, form, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, res.StatusCode)
		}
	}
	// Nor can a database be created in another team's environment.
	projectID, env := a.project("Mine")
	res, _ := a.post("/projects/"+projectID+"/env/"+env.ID+"/database/new?engine=postgres", "/projects/"+projectID+"/env/"+envs[0].ID+"/database",
		url.Values{"engine": {"postgres"}, "name": {"x"}, "image": {"postgres:17-alpine"}})
	wantStatus(t, res, http.StatusNotFound)

	if got, _ := a.db.DatabaseByID(ctx, other.ID); got.PublicPort != 0 || got.Status != db.AppCreated {
		t.Fatalf("the other team's database was changed: %+v", got)
	}
	if n := len(a.fake.Calls()); n != 0 {
		t.Fatalf("%d commands ran for another team's database", n)
	}
}

func TestFailedDatabaseShowsWhy(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	a.db.Exec(`UPDATE databases SET status = 'failed', container = '', last_error = ? WHERE id = ?`,
		`the database exited with status 1 while starting. Its last output: <b>FATAL</b> wrong ownership`, m.ID)
	_, page := a.get(a.databasePath(m.ID))
	if !strings.Contains(page, "The database did not start") || !strings.Contains(page, "wrong ownership") {
		t.Fatal("the overview does not explain the failure")
	}
	if strings.Contains(page, "<b>FATAL</b>") {
		t.Fatal("container output reached the page unescaped")
	}
}
