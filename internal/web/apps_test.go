package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
)

// project creates a project and returns its id and production environment.
func (a *app) project(name string) (string, db.Environment) {
	a.t.Helper()
	res, _ := a.post("/projects/new", "/projects", url.Values{"name": {name}})
	wantStatus(a.t, res, http.StatusSeeOther)
	id := strings.TrimPrefix(res.Header.Get("Location"), "/projects/")
	envs, err := a.db.ListEnvironments(context.Background(), id)
	if err != nil || len(envs) == 0 {
		a.t.Fatalf("environments: %v", err)
	}
	return id, envs[0]
}

// newApp creates an app through the form and returns its id. With deploy it
// also waits for the first deployment to finish.
func (a *app) newApp(projectID string, env db.Environment, name string, deploy bool, extra url.Values) string {
	a.t.Helper()
	form := url.Values{"env": {env.ID}, "name": {name}, "image": {"nginx:alpine"}, "port": {"80"}}
	if deploy {
		form.Set("deploy", "1")
	}
	for k, v := range extra {
		form[k] = v
	}
	res, body := a.post("/projects/"+projectID+"/apps/new?env="+env.ID, "/projects/"+projectID+"/apps", form)
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("create app: %d\n%s", res.StatusCode, body)
	}
	id := strings.Split(strings.TrimPrefix(res.Header.Get("Location"), "/apps/"), "/")[0]
	if deploy {
		a.waitDeployed(id)
	}
	return id
}

// waitDeployed waits until the app's newest deployment has finished.
func (a *app) waitDeployed(appID string) db.Deployment {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := a.db.ListDeployments(context.Background(), appID, 1)
		if len(list) == 1 && (list[0].Status == db.DeploySuccess || list[0].Status == db.DeployFailed) {
			return list[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatal("deployment did not finish")
	return db.Deployment{}
}

func (a *app) routesFile() string {
	raw, _, _ := a.fake.File(a.cfg.RoutesPath())
	return raw
}

func TestCreateAndDeployApp(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")

	// The form suggests a generated address that needs no DNS.
	_, form := a.get("/projects/" + projectID + "/apps/new?env=" + env.ID)
	m := regexp.MustCompile(`name="domain"[^>]*value="([a-z2-7]{8}\.127\.0\.0\.1\.sslip\.io)"`).FindStringSubmatch(form)
	if m == nil {
		t.Fatal("no generated domain in the form")
	}

	appID := a.newApp(projectID, env, "web", true, url.Values{"domain": {m[1]}})
	dep := a.waitDeployed(appID)
	if dep.Status != db.DeploySuccess {
		t.Fatalf("deployment: %s %s", dep.Status, dep.Error)
	}

	res, body := a.get("/apps/" + appID)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"Running", "nginx:alpine", m[1], "web:80", "Redeploy"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview is missing %q", want)
		}
	}
	// A generated address is served over plain HTTP.
	if !strings.Contains(a.routesFile(), `"host": "`+m[1]+`"`) || strings.Contains(a.routesFile(), `"tls": true`) {
		t.Fatalf("routes: %s", a.routesFile())
	}

	_, list := a.get("/projects/" + projectID + "?env=" + env.ID)
	if !strings.Contains(list, `data-state="running"`) {
		t.Fatal("the project page does not show the app as running")
	}

	// The deployment page and its live log.
	res, page := a.get("/apps/" + appID + "/deployments/" + dep.ID)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "sse-connect") || !strings.Contains(page, "Succeeded") {
		t.Fatal("deployment page is missing the log stream or status")
	}
	res, stream := a.get("/apps/" + appID + "/deployments/" + dep.ID + "/stream")
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream content type %q", ct)
	}
	if !strings.Contains(stream, "Pulling nginx:alpine") || !strings.HasSuffix(stream, "event: done\ndata: \n\n") {
		t.Fatalf("stream:\n%s", stream)
	}
	// A finished deployment's status fragment no longer polls.
	_, frag := a.get("/apps/" + appID + "/deployments/" + dep.ID + "/status")
	if strings.Contains(frag, "hx-trigger") {
		t.Fatal("a finished deployment still polls for status")
	}
}

func TestAppFormValidation(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", false, url.Values{"domain": {"taken.example.com"}})

	cases := []struct {
		field url.Values
		want  string
	}{
		{url.Values{"name": {"Not Valid"}}, appNameRule},
		{url.Values{"name": {"web"}}, "already has an app, database or service called web"},
		{url.Values{"image": {"--privileged"}}, "Enter an image name"},
		{url.Values{"image": {"nginx; rm -rf /"}}, "Enter an image name"},
		{url.Values{"port": {"0"}}, "between 1 and 65535"},
		{url.Values{"port": {"http"}}, "between 1 and 65535"},
		{url.Values{"domain": {"http://x.example.com/path"}}, "without http://"},
		{url.Values{"domain": {"taken.example.com"}}, "already routed"},
		{url.Values{"domain": {"localhost"}}, "Enter a domain such as"},
	}
	for _, c := range cases {
		form := url.Values{"env": {env.ID}, "name": {"api"}, "image": {"nginx"}, "port": {"80"}}
		for k, v := range c.field {
			form[k] = v
		}
		res, body := a.post("/projects/"+projectID+"/apps/new?env="+env.ID, "/projects/"+projectID+"/apps", form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v: status %d, want 422 with %q", c.field, res.StatusCode, c.want)
		}
	}
	apps, _ := a.db.ListApps(context.Background(), env.ID)
	if len(apps) != 1 {
		t.Fatalf("%d apps exist after rejected forms", len(apps))
	}
}

func TestAppSettings(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/settings"
	valid := func() url.Values {
		return url.Values{"name": {"web"}, "image": {"nginx:alpine"}, "port": {"80"}, "health_timeout": {"60"}}
	}

	bad := map[string][2]string{
		"memory_mb":      {"lots", "megabytes"},
		"cpus":           {"-1", "number of cores"},
		"health_path":    {"@evil.example.com/", "starts with a single /"},
		"health_timeout": {"2", "between 5 and 900"},
		"image":          {"-v", "Enter an image name"},
	}
	for field, c := range bad {
		form := valid()
		form.Set(field, c[0])
		res, body := a.post(page, page, form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c[1]) {
			t.Errorf("%s=%q: status %d, want 422 with %q", field, c[0], res.StatusCode, c[1])
		}
	}
	// The second health-path attack: a path that starts with two slashes.
	form := valid()
	form.Set("health_path", "//evil.example.com/")
	if res, _ := a.post(page, page, form); res.StatusCode != http.StatusUnprocessableEntity {
		t.Error("a protocol-relative health path was accepted")
	}

	form = valid()
	form.Set("memory_mb", "256")
	form.Set("cpus", "0.5")
	form.Set("health_path", "/healthz")
	form.Set("port", "8080")
	res, _ := a.post(page, page, form)
	wantRedirect(t, res, page)
	got, _ := a.db.AppByID(context.Background(), appID)
	if got.MemoryMB != 256 || got.CPUs != 0.5 || got.HealthPath != "/healthz" || got.Port != 8080 {
		t.Fatalf("saved: %+v", got)
	}
}

func TestAppEnvironmentIsSealed(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/environment"

	res, body := a.post(page, page, url.Values{"vars": {"GOOD=1\nnot a variable\n"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Line 2") {
		t.Fatal("the error does not name the bad line")
	}

	res, _ = a.post(page, page, url.Values{"vars": {"# database\nDATABASE_URL=postgres://u:hunter2@db/app\nDEBUG=\n"}})
	wantRedirect(t, res, page)
	stored, _ := a.db.ListEnvVars(context.Background(), db.KindApp, appID)
	if len(stored) != 2 {
		t.Fatalf("%d variables stored", len(stored))
	}
	for _, v := range stored {
		if strings.Contains(v.Value, "hunter2") {
			t.Fatal("a value is stored in plain text")
		}
	}
	_, body = a.get(page)
	if !strings.Contains(body, "DATABASE_URL=postgres://u:hunter2@db/app") {
		t.Fatal("the editor does not show the saved value")
	}
}

func TestAppStorage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/storage"

	bad := []struct {
		form url.Values
		want string
	}{
		{url.Values{"kind": {"volume"}, "source": {"data"}, "target": {"relative"}}, "absolute path inside the container"},
		{url.Values{"kind": {"volume"}, "source": {"../etc"}, "target": {"/data"}}, "volume name"},
		{url.Values{"kind": {"bind"}, "source": {"/var/run/docker.sock"}, "target": {"/sock"}}, "cannot be mounted"},
		{url.Values{"kind": {"bind"}, "source": {"/etc"}, "target": {"/host-etc"}}, "cannot be mounted"},
		{url.Values{"kind": {"bind"}, "source": {a.cfg.DataDir}, "target": {"/musdash"}}, "cannot be mounted"},
		{url.Values{"kind": {"bind"}, "source": {"/"}, "target": {"/host"}}, "whole filesystem"},
		{url.Values{"kind": {"tmpfs"}, "source": {"x"}, "target": {"/x"}}, "Choose a storage type"},
	}
	for _, c := range bad {
		res, body := a.post(page, page, c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v: status %d, want 422 with %q", c.form, res.StatusCode, c.want)
		}
	}

	for _, form := range []url.Values{
		{"kind": {"volume"}, "source": {"data"}, "target": {"/data"}},
		{"kind": {"bind"}, "source": {"/srv/uploads"}, "target": {"/uploads"}},
		{"kind": {"file"}, "target": {"/etc/app.conf"}, "content": {"secret_token = abc123"}},
	} {
		res, body := a.post(page, page, form)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%v: %d\n%s", form, res.StatusCode, body)
		}
	}
	res, body := a.post(page, page, url.Values{"kind": {"volume"}, "source": {"other"}, "target": {"/data"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already mounted") {
		t.Error("two mounts on one path were accepted")
	}

	list, _ := a.db.ListStorages(context.Background(), db.KindApp, appID)
	if len(list) != 3 {
		t.Fatalf("%d storages", len(list))
	}
	for _, st := range list {
		if st.Kind == db.StorageFile && (st.Content == "" || strings.Contains(st.Content, "abc123")) {
			t.Fatal("file mount content is not sealed")
		}
	}
	res, _ = a.post(page, page+"/"+list[0].ID+"/delete", nil)
	wantRedirect(t, res, page)
	if list, _ := a.db.ListStorages(context.Background(), db.KindApp, appID); len(list) != 2 {
		t.Fatal("storage was not removed")
	}
}

func TestAppDomains(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	a.fake.PutFile(a.cfg.ProxyPIDPath(), "4242\n")
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	page := "/apps/" + appID + "/settings"

	res, _ := a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"Shop.Example.com"}, "tls": {"1"}, "redirect_www": {"1"}})
	wantRedirect(t, res, page+"#domains")
	// A generated name never gets HTTPS, whatever the box says.
	res, _ = a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"abc.203.0.113.7.sslip.io"}, "tls": {"1"}})
	wantRedirect(t, res, page+"#domains")

	doms, _ := a.db.ListDomains(context.Background(), db.KindApp, appID)
	if len(doms) != 2 || doms[0].Host != "shop.example.com" || !doms[0].TLS || !doms[0].RedirectWWW || doms[1].TLS {
		t.Fatalf("domains: %+v", doms)
	}
	routes := a.routesFile()
	for _, want := range []string{`"host": "shop.example.com"`, `"host": "www.shop.example.com"`, `"redirect_to": "shop.example.com"`, `"host": "abc.203.0.113.7.sslip.io"`} {
		if !strings.Contains(routes, want) {
			t.Errorf("routes are missing %s:\n%s", want, routes)
		}
	}
	reloads := 0
	for _, c := range a.fake.Calls() {
		if c == "kill -HUP 4242" {
			reloads++
		}
	}
	if reloads < 3 { // the deploy, and each added domain
		t.Fatalf("the proxy was reloaded %d times", reloads)
	}

	res, body := a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"shop.example.com"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already routed") {
		t.Error("a duplicate domain was accepted")
	}

	res, _ = a.post(page, "/apps/"+appID+"/domains/"+doms[0].ID+"/delete", nil)
	wantRedirect(t, res, page+"#domains")
	if strings.Contains(a.routesFile(), "shop.example.com") {
		t.Fatal("a removed domain is still routed")
	}
}

func TestStopRedeployAndDeleteApp(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, url.Values{"domain": {"shop.example.com"}})
	ctx := context.Background()

	res, _ := a.post("/apps/"+appID, "/apps/"+appID+"/stop", nil)
	wantRedirect(t, res, "/apps/"+appID)
	if got, _ := a.db.AppByID(ctx, appID); got.Status != db.AppStopped || got.Container != "" {
		t.Fatalf("after stop: %+v", got)
	}
	if strings.Contains(a.routesFile(), "shop.example.com") {
		t.Fatal("a stopped app is still routed")
	}

	res, _ = a.post("/apps/"+appID, "/apps/"+appID+"/deploy", nil)
	wantStatus(t, res, http.StatusSeeOther)
	a.waitDeployed(appID)
	var jobs int
	a.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'deploy'`).Scan(&jobs)
	if jobs != 2 {
		t.Fatalf("%d deploy jobs, want 2 (create and redeploy)", jobs)
	}

	// A project that still holds an app cannot be deleted.
	settings := "/projects/" + projectID + "/settings"
	res, _ = a.post(settings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, settings)
	if _, err := a.db.Project(ctx, firstTeam(t, a), projectID); err != nil {
		t.Fatal("a project with an app in it was deleted")
	}

	appSettings := "/apps/" + appID + "/settings"
	res, _ = a.post(appSettings, "/apps/"+appID+"/delete", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, appSettings)
	res, _ = a.post(appSettings, "/apps/"+appID+"/delete", url.Values{"confirm": {"web"}})
	wantRedirect(t, res, "/projects/"+projectID+"?env="+env.ID)
	res, _ = a.get("/apps/" + appID)
	wantStatus(t, res, http.StatusNotFound)

	res, _ = a.post(settings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, "/")
}

func TestRuntimeLogStreamEscapesOutput(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")

	idle := a.newApp(projectID, env, "idle", false, nil)
	res, _ := a.get("/apps/" + idle + "/logs/stream")
	wantStatus(t, res, http.StatusConflict)
	_, page := a.get("/apps/" + idle + "/logs")
	if !strings.Contains(page, "Nothing is running") {
		t.Fatal("no empty state for an app that is not running")
	}

	appID := a.newApp(projectID, env, "web", true, nil)
	res, stream := a.get("/apps/" + appID + "/logs/stream")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(stream, "data: listening on :80") {
		t.Fatalf("stream:\n%s", stream)
	}
	// Container output is untrusted and the browser inserts it as markup.
	if strings.Contains(stream, "<script>") || !strings.Contains(stream, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("log output was not escaped:\n%s", stream)
	}
}

func TestOtherTeamsAppIsNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: other.ID, Image: "nginx"})
	dom, _ := a.db.AddDomain(ctx, "otherteam", "othersrv", db.Domain{ResourceKind: db.KindApp, ResourceID: other.ID, Host: "secret.example.com", TLS: true})
	base := "/apps/" + other.ID

	for _, path := range []string{"", "/status", "/deployments", "/deployments/" + dep.ID, "/deployments/" + dep.ID + "/status",
		"/deployments/" + dep.ID + "/stream", "/logs", "/logs/stream", "/environment", "/storage", "/settings"} {
		res, body := a.get(base + path)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "secret-app") {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	token := a.csrf("/projects/new")
	for path, form := range map[string]url.Values{
		"/deploy":                        {},
		"/stop":                          {},
		"/environment":                   {"vars": {"X=1"}},
		"/storage":                       {"kind": {"volume"}, "source": {"d"}, "target": {"/d"}},
		"/settings":                      {"name": {"x"}, "image": {"nginx"}, "port": {"80"}, "health_timeout": {"60"}},
		"/domains":                       {"host": {"mine.example.com"}},
		"/domains/" + dom.ID + "/delete": {},
		"/delete":                        {"confirm": {"secret-app"}},
	} {
		form.Set("_csrf", token)
		res, _ := a.postRaw(a.client, base+path, form, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, res.StatusCode)
		}
	}
	// A deployment id from another app does not open under our own app.
	projectID, env := a.project("Mine")
	mine := a.newApp(projectID, env, "web", false, nil)
	res, _ := a.get("/apps/" + mine + "/deployments/" + dep.ID)
	wantStatus(t, res, http.StatusNotFound)
	// Nor can an app be created in another team's environment.
	res, _ = a.post("/projects/"+projectID+"/apps/new?env="+env.ID, "/projects/"+projectID+"/apps",
		url.Values{"env": {envs[0].ID}, "name": {"x"}, "image": {"nginx"}, "port": {"80"}})
	wantStatus(t, res, http.StatusNotFound)

	if got, _ := a.db.AppByID(ctx, other.ID); got.Name != "secret-app" {
		t.Fatal("the other team's app was changed")
	}
	if jobs := 0; a.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&jobs) == nil && jobs != 0 {
		t.Fatalf("%d jobs were queued for another team's app", jobs)
	}
}

func TestServersAndInstanceSettings(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	a.fake.PutFile(a.cfg.ProxyPIDPath(), "4242\n")

	res, body := a.get("/servers")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"localhost", "This machine", "29.8.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("servers page is missing %q", want)
		}
	}
	servers, _ := a.db.ListServers(context.Background(), firstTeam(t, a))
	path := "/servers/" + servers[0].ID
	res, body = a.post("/servers", path, url.Values{"ip": {"not-an-ip"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter an IP address") {
		t.Error("a bad address was accepted")
	}
	res, _ = a.post("/servers", path, url.Values{"ip": {"203.0.113.7"}})
	wantRedirect(t, res, "/servers")
	res, _ = a.post("/servers", "/servers/nope", url.Values{"ip": {"203.0.113.7"}})
	wantStatus(t, res, http.StatusNotFound)

	// New apps are now offered an address on the server's public IP.
	projectID, env := a.project("Shop")
	_, form := a.get("/projects/" + projectID + "/apps/new?env=" + env.ID)
	if !strings.Contains(form, ".203.0.113.7.sslip.io") {
		t.Fatal("the generated domain does not use the server's address")
	}

	res, body = a.post("/settings", "/settings", url.Values{"instance_domain": {"not a domain"}, "acme_email": {"nope"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Enter a domain such as") || !strings.Contains(body, "Enter an email address") {
		t.Fatal("settings errors are missing")
	}
	res, _ = a.post("/settings", "/settings", url.Values{"instance_domain": {"Dash.Example.com"}, "acme_email": {"ops@example.com"}})
	wantRedirect(t, res, "/settings")
	routes := a.routesFile()
	for _, want := range []string{`"email": "ops@example.com"`, `"host": "dash.example.com"`, `"target": "127.0.0.1:8000"`} {
		if !strings.Contains(routes, want) {
			t.Errorf("routes are missing %s:\n%s", want, routes)
		}
	}
	// The dashboard's own domain cannot also be given to an app.
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/settings"
	res, body = a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"dash.example.com"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "dashboard&#39;s own address") {
		t.Errorf("the dashboard domain was given to an app: %d", res.StatusCode)
	}
}

func TestStreamCannotForgeEvents(t *testing.T) {
	rec := httptest.NewRecorder()
	out := startSSE(rec)
	// A carriage return ends a line in the event-stream format; without
	// care this output would close the stream and inject a field.
	out.Write([]byte("progress 10%\rprogress 20%\revent: done\rdata: x\nnext line\n"))
	out.Write([]byte("partial"))
	out.finish()
	body := rec.Body.String()
	// Only a line that begins with "event:" is a field; the same words in
	// the middle of a data line are just text.
	doneFields := 0
	for _, line := range strings.Split(body, "\n") {
		if line == "event: done" {
			doneFields++
		}
	}
	if doneFields != 1 || !strings.HasSuffix(body, "event: done\ndata: \n\n") {
		t.Fatalf("log output forged a done event:\n%q", body)
	}
	if strings.Contains(body, "\r") {
		t.Fatalf("a carriage return reached the stream:\n%q", body)
	}
	for _, want := range []string{"data: progress 10% progress 20% event: done data: x\n", "data: next line\n", "data: partial\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream is missing %q:\n%q", want, body)
		}
	}
}

func TestDomainLimits(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/settings"

	// Valid on its own, but too long once "www." goes in front.
	long := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example"
	res, body := a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {long}, "redirect_www": {"1"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "too long to also carry a www form") {
		t.Fatalf("a name too long for its www form was accepted: %d", res.StatusCode)
	}
	res, _ = a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {long}})
	wantRedirect(t, res, page+"#domains")

	for i := 1; i < maxDomains; i++ {
		res, _ := a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"d" + strconv.Itoa(i) + ".example.com"}})
		wantRedirect(t, res, page+"#domains")
	}
	res, body = a.post(page, "/apps/"+appID+"/domains", url.Values{"host": {"one-too-many.example.com"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "up to 20 domains") {
		t.Fatalf("the domain limit was not enforced: %d", res.StatusCode)
	}
}

func TestCPULimitRejectsNaN(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := "/apps/" + appID + "/settings"
	for _, v := range []string{"NaN", "Inf", "-Inf", "1e400"} {
		res, _ := a.post(page, page, url.Values{"name": {"web"}, "image": {"nginx"}, "port": {"80"}, "health_timeout": {"60"}, "cpus": {v}})
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("cpus=%s: status %d, want 422", v, res.StatusCode)
		}
	}
}

func TestLongFlashIsTruncated(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	setFlash(rec, req, "danger", strings.Repeat("é", 3000))
	cookie := rec.Result().Cookies()[0]
	if len(cookie.Value) > 1200 {
		t.Fatalf("flash cookie is %d bytes", len(cookie.Value))
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	f := takeFlash(httptest.NewRecorder(), req2)
	if f == nil || !strings.HasSuffix(f.Message, "…") || !utf8.ValidString(f.Message) {
		t.Fatalf("flash: %+v", f)
	}
}

// Two apps share a domain by path, one of them behind a password. The
// routes file is what the proxy reads, so the test hands it to the proxy.
func TestAppDomainPathsAndPasswords(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	a.fake.PutFile(a.cfg.ProxyPIDPath(), "4242\n")
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", true, nil)
	api := a.newApp(projectID, env, "api", true, nil)
	webPage, apiPage := "/apps/"+web+"/settings", "/apps/"+api+"/settings"
	const password = "correct horse battery"

	res, _ := a.post(webPage, "/apps/"+web+"/domains", url.Values{"host": {"shop.example.com"}, "tls": {"1"}})
	wantRedirect(t, res, webPage+"#domains")
	// Typed loosely: no leading slash, one at the end.
	res, body := a.post(apiPage, "/apps/"+api+"/domains", url.Values{"host": {"shop.example.com"}, "path": {"api/"}, "strip_prefix": {"1"}, "tls": {"1"},
		"auth_user": {"ada"}, "auth_password": {password}})
	wantRedirect(t, res, apiPage+"#domains")

	doms, _ := a.db.ListDomains(context.Background(), db.KindApp, api)
	if len(doms) != 1 || doms[0].Path != "/api" || !doms[0].StripPrefix || doms[0].AuthUser != "ada" {
		t.Fatalf("stored: %+v", doms)
	}
	if doms[0].AuthHash == "" || strings.Contains(doms[0].AuthHash, password) || bcrypt.CompareHashAndPassword([]byte(doms[0].AuthHash), []byte(password)) != nil {
		t.Fatalf("the password was not stored as its hash: %q", doms[0].AuthHash)
	}

	// The file the proxy reads: private, and one the proxy takes.
	raw, mode, ok := a.fake.File(a.cfg.RoutesPath())
	if !ok || mode.Perm() != 0o600 {
		t.Fatalf("routes file mode %v: it holds password hashes", mode)
	}
	if strings.Contains(raw, password) {
		t.Fatal("the password itself is in the routes file")
	}
	table, err := proxy.Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	site, _ := table.Lookup("shop.example.com", "/")
	guarded, _ := table.Lookup("shop.example.com", "/api/users")
	if site.Target == "" || guarded.Target == "" || site.Target == guarded.Target {
		t.Fatalf("the two apps are not routed apart: %+v %+v", site, guarded)
	}
	if guarded.Path != "/api" || !guarded.StripPrefix || guarded.AuthUser != "ada" || site.AuthUser != "" {
		t.Fatalf("routes: %+v %+v", site, guarded)
	}

	// The pages show that a password is asked for, never the hash.
	for _, page := range []string{apiPage, "/apps/" + api} {
		_, body = a.get(page)
		if !strings.Contains(body, "shop.example.com/api") || !strings.Contains(body, "Password") {
			t.Errorf("%s does not show the path and the password", page)
		}
		if strings.Contains(body, doms[0].AuthHash) || strings.Contains(body, password) {
			t.Errorf("%s shows the password or its hash", page)
		}
	}

	// What is refused, and that nothing of it is stored or echoed.
	bad := map[string]url.Values{
		"same path again":         {"host": {"shop.example.com"}, "path": {"/api"}},
		"a path that climbs":      {"host": {"shop.example.com"}, "path": {"/a/../b"}},
		"a path with a query":     {"host": {"shop.example.com"}, "path": {"/a?b"}},
		"an encoded path":         {"host": {"shop.example.com"}, "path": {"/a%2Fb"}},
		"a password and no user":  {"host": {"x.example.com"}, "auth_password": {password}},
		"a user and no password":  {"host": {"x.example.com"}, "auth_user": {"ada"}},
		"a short password":        {"host": {"x.example.com"}, "auth_user": {"ada"}, "auth_password": {"short"}},
		"a password past bcrypt":  {"host": {"x.example.com"}, "auth_user": {"ada"}, "auth_password": {strings.Repeat("p", 73)}},
		"a user with a colon":     {"host": {"x.example.com"}, "auth_user": {"a:b"}, "auth_password": {password}},
		"a user with a line feed": {"host": {"x.example.com"}, "auth_user": {"a\nb"}, "auth_password": {password}},
	}
	for name, form := range bad {
		res, body := a.post(webPage, "/apps/"+web+"/domains", form)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
		if strings.Contains(body, password) {
			t.Errorf("%s: the password was sent back in the page", name)
		}
	}
	if doms, _ := a.db.ListDomains(context.Background(), db.KindApp, web); len(doms) != 1 {
		t.Fatalf("a refused domain was stored: %+v", doms)
	}

	// A resource that takes a whole host cannot take one that is in use,
	// and the dashboard's own address has no paths to give away.
	res, _ = a.post("/settings", "/settings", url.Values{"instance_domain": {"dash.example.com"}, "acme_email": {"ops@example.com"}})
	wantRedirect(t, res, "/settings")
	res, body = a.post(webPage, "/apps/"+web+"/domains", url.Values{"host": {"dash.example.com"}, "path": {"/app"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "dashboard&#39;s own address") {
		t.Errorf("a path of the dashboard's domain was given to an app: %d", res.StatusCode)
	}
}
