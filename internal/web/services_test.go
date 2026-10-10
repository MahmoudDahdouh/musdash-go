package web

import (
	"context"
	"html"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

var projectNameRE = regexp.MustCompile(`--project-name (\S+)`)

// stackServer makes the scripted server answer the sandboxed loads of a
// two-service stack: "main", which has the web address, and "db".
func (a *app) stackServer(mainService string, port string, extra func(line string) (string, error, bool)) {
	previous := a.fake.Handle
	upper := strings.ToUpper(strings.ReplaceAll(mainService, "-", "_"))
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if extra != nil {
			if out, err, handled := extra(line); handled {
				return out, err
			}
		}
		project := ""
		if m := projectNameRE.FindStringSubmatch(line); m != nil {
			project = m[1]
		}
		switch {
		case strings.HasPrefix(line, "id -"):
			return "1000\n", nil
		case strings.Contains(line, "config --format json --no-interpolate"):
			return `{"name":"` + project + `","services":{
				"` + mainService + `":{"image":"main:1","environment":["SERVICE_FQDN_` + upper + `_` + port + `","DB_PASSWORD=${SERVICE_PASSWORD_DB}"]},
				"db":{"image":"db:1","environment":["PASSWORD=${SERVICE_PASSWORD_DB}"]}}}`, nil
		case strings.Contains(line, "config --format json"):
			return `{"name":"` + project + `","networks":{"default":{"name":"` + project + `_default"}},
				"volumes":{"data":{"name":"` + project + `_data"}},
				"services":{
				"` + mainService + `":{"image":"main:1","networks":{"default":null}},
				"db":{"image":"db:1","networks":{"default":null},"volumes":[{"type":"volume","source":"data","target":"/data"}]}}}`, nil
		}
		return previous(line, c)
	}
}

const ownCompose = `services:
  front:
    image: main:1
    environment:
      - SERVICE_FQDN_FRONT_3000
      - DB_PASSWORD=${SERVICE_PASSWORD_DB}
      - API_KEY=${API_KEY}
      - REGION=${REGION:-eu}
  db:
    image: db:1
    environment:
      - PASSWORD=${SERVICE_PASSWORD_DB}
`

// newService creates a service from a person's own Compose file through
// the form and waits for its deployment when one was asked for.
func (a *app) newService(projectID string, env db.Environment, name string, form url.Values) db.Service {
	a.t.Helper()
	full := url.Values{"template": {db.TemplateCustom}, "name": {name}, "compose": {ownCompose}, "variables": {"API_KEY=k-123"}}
	for k, v := range form {
		full[k] = v
	}
	page := "/projects/" + projectID + "/env/" + env.ID + "/service/new?template=" + full.Get("template")
	res, body := a.post(page, "/projects/"+projectID+"/env/"+env.ID+"/service", full)
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("create service: %d\n%s", res.StatusCode, body)
	}
	return a.waitService(createdID(res, db.KindService))
}

func (a *app) waitService(id string) db.Service {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := a.db.ServiceByID(context.Background(), id)
		if err != nil {
			a.t.Fatal(err)
		}
		// A deployment says "running" before it publishes the routes, so
		// the job itself must have ended too: a test that then reads the
		// routes file found it empty now and then.
		var busy int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = ? AND status IN ('queued', 'running')`, deploy.JobService).Scan(&busy); err != nil {
			a.t.Fatal(err)
		}
		if s.Status != db.AppDeploying && busy == 0 {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatal("the service did not settle")
	return db.Service{}
}

func TestServiceCatalogueAndTemplateForm(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	base := "/projects/" + projectID + "/env/" + env.ID + "/service/new"

	// The Add resource page is the catalogue; the address without a
	// template leads to it.
	res, _ := a.get(base)
	wantRedirect(t, res, "/projects/"+projectID+"/env/"+env.ID+"/new")
	res, page := a.get("/projects/" + projectID + "/env/" + env.ID + "/new")
	wantStatus(t, res, http.StatusOK)
	// It draws the first of the catalogue's pages; the rest is asked for as
	// the list is scrolled (TestAddResourceIsFoundAndPaged).
	for _, tpl := range catalog.Services()[:48] {
		if !strings.Contains(page, "template="+tpl.Key+`"`) || !strings.Contains(html.UnescapeString(page), tpl.Name) {
			t.Errorf("the catalogue's first page is missing %s", tpl.Name)
		}
	}
	if !strings.Contains(page, "template=custom") {
		t.Error("the catalogue does not offer a person's own Compose file")
	}

	// The page is narrowed by category: in the menu an option for every
	// category that has something and for no other, and none chosen.
	menu := between(t, "the page", page, `<div class="select" data-select data-select-multi>`, `id="kinds"`)
	if strings.Contains(menu, " checked") || !strings.Contains(menu, `aria-multiselectable="true"`) {
		t.Errorf("a category is chosen before anybody chose, or the list takes one only: %s", menu)
	}
	inUse, count := catalog.CategoriesInUse()
	used := map[string]bool{"database": true} // the engines are databases
	for _, c := range inUse {
		used[c.Key] = true
	}
	for _, c := range catalog.Categories() {
		if has := strings.Contains(menu, `name="category" value="`+c.Key+`"`); has != used[c.Key] {
			t.Errorf("category %s: option %v, in use %v", c.Key, has, used[c.Key])
		}
	}
	if n := strings.Count(menu, `type="checkbox"`); n != len(used) {
		t.Errorf("%d options for %d categories", n, len(used))
	}
	if want := `<span class="min-w-0 flex-1">CMS</span> <span class="menu-count">` + strconv.Itoa(count["cms"]) + `</span>`; !strings.Contains(menu, want) {
		t.Errorf("the CMS option does not say how many: want %s in %s", want, menu)
	}
	// Nothing narrows yet, so there is nothing to clear.
	if strings.Contains(page, "Clear filters") || !strings.Contains(menu, `<span id="kinds-clear"></span>`) {
		t.Errorf("Clear filters is offered with nothing to clear, or has no place by the field")
	}
	// A person's own Compose file is under Apps, with what else is their
	// own. Services are the ready-made ones, and their heading says how
	// many there are in all: the one count on the page.
	apps := between(t, "the page", page, `aria-label="Kinds of app"`, `aria-label="Database engines"`)
	services := page[strings.Index(page, `aria-label="Kinds of service"`):]
	for _, own := range []string{db.TemplateCustom, db.TemplateGit} {
		if link := `/service/new?template=` + own + `"`; !strings.Contains(apps, link) || strings.Contains(services, link) {
			t.Errorf("a person's own Compose file (%s) is not under Apps, or is under Services too", own)
		}
	}
	heading := between(t, "the page", page, `<h2 class="section-title">Services`, `</h2>`)
	if want := `(` + strconv.Itoa(len(catalog.Services())) + `)`; !strings.Contains(heading, want) || strings.Count(page, `<span class="font-normal text-ink-mute">(`) != 1 {
		t.Errorf("the Services heading does not say how many there are, or another heading counts too: %s", heading)
	}
	if n := strings.Count(services, `class="tile offer"`); n != 48 {
		t.Errorf("%d cards under Services on a page of 48", n)
	}
	// The cards refer to icons the page draws once: every reference has
	// its drawing, and each drawing is there once.
	for _, m := range regexp.MustCompile(`<use href="#(icon-[a-z-]+)"`).FindAllStringSubmatch(page, -1) {
		if strings.Count(page, `<symbol id="`+m[1]+`"`) != 1 {
			t.Fatalf("the icon %s is referred to and drawn %d times", m[1], strings.Count(page, `<symbol id="`+m[1]+`"`))
		}
	}
	if n := strings.Count(page, `<path d="M18.5 12L4.99997 12"`); n != 1 {
		t.Errorf("the arrow of Deploy is drawn %d times on a page of cards", n)
	}
	if len(page) > 200<<10 {
		t.Errorf("the page is %d KB: a card's markup has grown, or the page holds more than its first 48", len(page)>>10)
	}
	if !strings.Contains(page, `<p class="offer-tags"><span>`) {
		t.Error("a card does not name its categories")
	}

	// A template that needs a value from the person asks for it, and one
	// that must reach apps is connected to the environment by default.
	_, form := a.get(base + "?template=cloudflared")
	if !strings.Contains(form, `name="var_TUNNEL_TOKEN"`) || !regexp.MustCompile(`name="connect_env"[^>]*checked`).MatchString(form) {
		t.Fatal("the cloudflared form does not ask for the token or is not connected by default")
	}
	_, form = a.get(base + "?template=wordpress")
	if strings.Contains(form, `name="var_`) || regexp.MustCompile(`name="connect_env"[^>]*checked`).MatchString(form) || strings.Contains(form, `name="compose"`) {
		t.Fatal("the WordPress form asks for something it should not")
	}
	_, form = a.get(base + "?template=custom")
	if !strings.Contains(form, `name="compose"`) || !strings.Contains(form, `name="variables"`) {
		t.Fatal("the form for a person's own file has no place for the file")
	}
	res, _ = a.get(base + "?template=../databases")
	wantStatus(t, res, http.StatusNotFound)

	// The token is required.
	res, body := a.post(base+"?template=cloudflared", "/projects/"+projectID+"/env/"+env.ID+"/service",
		url.Values{"template": {"cloudflared"}, "name": {"tunnel"}, "connect_env": {"1"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter a value on one line") {
		t.Fatalf("cloudflared without its token: %d", res.StatusCode)
	}
}

func TestCreateServiceFromTheCatalogue(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("wordpress", "80", nil)

	page := "/projects/" + projectID + "/env/" + env.ID + "/service/new?template=wordpress"
	res, body := a.post(page, "/projects/"+projectID+"/env/"+env.ID+"/service",
		url.Values{"template": {"wordpress"}, "name": {"blog"}, "deploy": {"1"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", res.StatusCode, body)
	}
	s := a.waitService(createdID(res, db.KindService))
	if s.Status != db.AppRunning || s.Template != "wordpress" || s.ConnectEnv || s.Members != "db,wordpress" {
		t.Fatalf("after create: %+v", s)
	}
	tpl, _ := catalog.Service("wordpress")
	if s.Compose != tpl.Compose {
		t.Fatal("the service does not hold the template's file")
	}

	// Its values were generated, are stored sealed, and never reach a
	// command line.
	vars, err := a.server.Deploy.ServiceVariables(s)
	if err != nil || len(vars["SERVICE_PASSWORD_WORDPRESS"]) != 32 || len(vars["SERVICE_PASSWORD_ROOT"]) != 32 || len(vars["SERVICE_USER_WORDPRESS"]) != 16 {
		t.Fatalf("generated: %v %v", vars, err)
	}
	for _, c := range a.fake.Calls() {
		for name, value := range vars {
			if strings.Contains(c, value) {
				t.Fatalf("%s is on a command line: %s", name, c)
			}
		}
	}
	if strings.Contains(s.Variables, vars["SERVICE_PASSWORD_ROOT"]) {
		t.Fatal("the generated values are stored in the clear")
	}

	// The endpoint got an address that needs no DNS, served over HTTP.
	endpoints, _ := a.db.ListEndpoints(ctx, s.ID)
	if len(endpoints) != 1 || endpoints[0].Name != "WORDPRESS" || endpoints[0].ComposeService != "wordpress" || endpoints[0].Port != 80 ||
		!strings.HasSuffix(endpoints[0].Host, ".127.0.0.1.sslip.io") || endpoints[0].TLS || endpoints[0].HostPort == 0 {
		t.Fatalf("endpoints: %+v", endpoints)
	}
	if !strings.Contains(a.routesFile(), `"host": "`+endpoints[0].Host+`"`) {
		t.Fatalf("the endpoint is not routed: %s", a.routesFile())
	}
	envFile, _, _ := a.fake.File(a.cfg.AppDir(s.ID) + "/sandbox.env")
	if !strings.Contains(envFile, "SERVICE_FQDN_WORDPRESS_80="+endpoints[0].Host+"\n") {
		t.Fatalf("the service was not told its address:\n%s", envFile)
	}

	// The overview: address, the question about its containers, generated
	// values behind bullets.
	res, overview := a.get(a.servicePath(s.ID))
	wantStatus(t, res, http.StatusOK)
	overview = html.UnescapeString(overview)
	for _, want := range []string{"Running", "WordPress", endpoints[0].Host, "to wordpress:80", `hx-get="` + a.servicePath(s.ID) + `/containers"`, "Redeploy", "Stop",
		`data-copy="` + vars["SERVICE_PASSWORD_ROOT"] + `"`, "SERVICE_USER_WORDPRESS", a.servicePath(s.ID) + "/deploy-log/stream"} {
		if !strings.Contains(overview, want) {
			t.Errorf("the overview is missing %q", want)
		}
	}
	if strings.Contains(regexp.MustCompile(`data-copy="[^"]*"`).ReplaceAllString(overview, ""), vars["SERVICE_PASSWORD_ROOT"]) {
		t.Fatal("a generated password is drawn on the page outside its copy button")
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("a page holding passwords may be cached")
	}
	// Nowhere else.
	for _, path := range []string{"/projects/" + projectID + "/env/" + env.ID, a.servicePath(s.ID) + "/settings", a.servicePath(s.ID) + "/logs", a.servicePath(s.ID) + "/compose", a.servicePath(s.ID) + "/status"} {
		res, body := a.get(path)
		wantStatus(t, res, http.StatusOK)
		for name, value := range vars {
			if strings.Contains(body, value) {
				t.Errorf("%s shows %s", path, name)
			}
		}
	}
	_, list := a.get("/projects/" + projectID + "/env/" + env.ID)
	if !strings.Contains(list, a.servicePath(s.ID)) || !strings.Contains(list, "blog") {
		t.Fatal("the project page does not list the service")
	}

	// The deployment's output, and the containers' own.
	res, stream := a.get(a.servicePath(s.ID) + "/deploy-log/stream")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(stream, "Reading the Compose file") || !strings.Contains(stream, "Deployed.") || !strings.Contains(stream, "event: done") {
		t.Fatalf("deploy log stream:\n%s", stream)
	}
	a.stackServer("wordpress", "80", func(line string) (string, error, bool) {
		if strings.Contains(line, " logs --follow") {
			return "wordpress-1  | <b>ready</b>\n", nil, true
		}
		return "", nil, false
	})
	res, stream = a.get(a.servicePath(s.ID) + "/logs/stream")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(stream, "wordpress-1  | &lt;b&gt;ready&lt;/b&gt;") || strings.Contains(stream, "<b>") {
		t.Fatalf("container log stream:\n%s", stream)
	}
}

func TestServiceFormValidation(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", false, nil)
	_, otherEnv := a.project("Other")
	page := "/projects/" + projectID + "/env/" + env.ID + "/service/new?template=custom"
	create := "/projects/" + projectID + "/env/" + env.ID + "/service"
	valid := url.Values{"template": {db.TemplateCustom}, "name": {"site"}, "compose": {ownCompose}}

	for _, c := range []struct {
		change url.Values
		want   string
	}{
		{url.Values{"name": {"Has Space"}}, "Use lowercase letters"},
		{url.Values{"name": {"web"}}, "already has an app, database or service called web"},
		{url.Values{"compose": {"  \n"}}, "Paste a Compose file"},
		{url.Values{"compose": {"services:\n  a:\n    image: x # " + strings.Repeat("x", maxComposeBytes)}}, "larger than 128 KB"},
		{url.Values{"variables": {"not a variable"}}, "write each variable as NAME=value"},
		{url.Values{"variables": {"SERVICE_PASSWORD_DB=mine"}}, "SERVICE_PASSWORD_DB is filled in by musdash"},
		{url.Values{"variables": {"SERVICE_FQDN_FRONT=evil.example.com"}}, "SERVICE_FQDN_FRONT is filled in by musdash"},
	} {
		form := url.Values{}
		for k, v := range valid {
			form[k] = v
		}
		for k, v := range c.change {
			form[k] = v
		}
		res, body := a.post(page, create, form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), c.want) {
			t.Errorf("%.60v: status %d, want 422 with %q", c.change, res.StatusCode, c.want)
		}
	}
	unknown := url.Values{}
	for k, v := range valid {
		unknown[k] = v
	}
	unknown.Set("template", "nonsense")
	if res, _ := a.post(page, create, unknown); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown template: %d, want 404", res.StatusCode)
	}
	// An environment is only reached through its own project.
	if res, _ := a.post(page, "/projects/"+projectID+"/env/"+otherEnv.ID+"/service", valid); res.StatusCode != http.StatusNotFound {
		t.Errorf("environment of another project: %d, want 404", res.StatusCode)
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM services`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d services were created by rejected forms", n)
	}

	// Created without deploying: it says what is still missing.
	s := a.newService(projectID, env, "site", url.Values{"variables": {""}})
	if s.Status != db.AppCreated {
		t.Fatalf("%+v", s)
	}
	_, overview := a.get(a.servicePath(s.ID))
	if !strings.Contains(overview, "The Compose file reads API_KEY, which has no value yet") || strings.Contains(overview, "REGION,") {
		t.Fatal("the overview does not name the variable that still needs a value")
	}
	// And an app cannot take its name.
	res, body := a.post("/projects/"+projectID+"/env/"+env.ID+"/app/new", "/projects/"+projectID+"/env/"+env.ID+"/app",
		url.Values{"name": {"site"}, "image": {"nginx"}, "port": {"80"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already has an app, database or service called site") {
		t.Fatalf("an app took a service's name: %d", res.StatusCode)
	}
}

func TestEditServiceComposeAndVariables(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("front", "3000", nil)
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	if s.Status != db.AppRunning {
		t.Fatalf("%+v", s)
	}
	before, _ := a.server.Deploy.ServiceVariables(s)
	composePage := a.servicePath(s.ID) + "/compose"

	// The page shows the file. The person's own variables are sent when
	// asked for, and the generated ones not at all.
	_, page := a.get(composePage)
	page = html.UnescapeString(page)
	if !strings.Contains(page, "SERVICE_FQDN_FRONT_3000") || strings.Contains(page, "k-123") || strings.Contains(page, before["SERVICE_PASSWORD_DB"]) {
		t.Fatal("the Compose page should show the file and no variable's value")
	}
	if !strings.Contains(page, `name="variables_kept"`) || !strings.Contains(page, `hx-get="`+composePage+`/variables"`) {
		t.Fatal("the Compose page has no way to ask for the variables")
	}
	_, field := a.get(composePage + "/variables")
	if !strings.Contains(field, "API_KEY=k-123") || strings.Contains(field, `name="variables_kept"`) || strings.Contains(field, before["SERVICE_PASSWORD_DB"]) {
		t.Fatalf("asking for the variables does not answer with the entered ones only:\n%s", field)
	}
	// A form sent from the page as it first was changes no variable.
	res, _ := a.post(composePage, composePage, url.Values{"compose": {ownCompose}, "variables_kept": {"1"}})
	wantRedirect(t, res, composePage)
	if kept, _ := a.server.Deploy.ServiceVariables(s); kept["API_KEY"] != "k-123" || kept["SERVICE_PASSWORD_DB"] != before["SERVICE_PASSWORD_DB"] {
		t.Fatalf("a form without the variables changed them: %v", kept)
	}

	// A change: one more generated value, a changed entered one, connected.
	changed := strings.Replace(ownCompose, "- REGION=${REGION:-eu}", "- REGION=${REGION:-eu}\n      - SESSION=${SERVICE_BASE64_SESSION}", 1)
	res, _ = a.post(composePage, composePage, url.Values{"compose": {strings.ReplaceAll(changed, "\n", "\r\n")}, "variables": {"API_KEY=k-456\nREGION=us"}, "connect_env": {"1"}})
	wantRedirect(t, res, composePage)
	got, _ := a.db.ServiceByID(ctx, s.ID)
	after, _ := a.server.Deploy.ServiceVariables(got)
	if got.Compose != changed || !got.ConnectEnv {
		t.Fatalf("stored file or connection: %+v", got)
	}
	if after["SERVICE_PASSWORD_DB"] != before["SERVICE_PASSWORD_DB"] {
		t.Fatal("saving the file changed a value that was generated before")
	}
	if after["API_KEY"] != "k-456" || after["REGION"] != "us" || after["SERVICE_BASE64_SESSION"] == "" {
		t.Fatalf("variables after the save: %v", after)
	}
	// Saving alone deploys nothing.
	if got.Status != db.AppRunning {
		t.Fatalf("status %s after a plain save", got.Status)
	}
	// An entered variable that is removed is gone.
	a.post(composePage, composePage, url.Values{"compose": {changed}, "variables": {"API_KEY=k-456"}})
	got, _ = a.db.ServiceByID(ctx, s.ID)
	if after, _ = a.server.Deploy.ServiceVariables(got); after["REGION"] != "" || after["API_KEY"] != "k-456" {
		t.Fatalf("after removing a variable: %v", after)
	}

	// Save and deploy.
	ups := countCalls(a.fake.Calls(), " up --detach")
	res, _ = a.post(composePage, composePage, url.Values{"compose": {changed}, "variables": {"API_KEY=k-789"}, "deploy": {"1"}})
	wantRedirect(t, res, a.servicePath(s.ID))
	if got = a.waitService(s.ID); got.Status != db.AppRunning || countCalls(a.fake.Calls(), " up --detach") != ups+1 {
		t.Fatalf("after save and deploy: %+v", got)
	}
	envFile, _, _ := a.fake.File(a.cfg.AppDir(s.ID) + "/sandbox.env")
	if !strings.Contains(envFile, "API_KEY=k-789\n") {
		t.Fatalf("the deployment did not use the saved variables:\n%s", envFile)
	}

	for field, c := range map[string][2]string{
		"compose":   {"", "Paste a Compose file"},
		"variables": {"SERVICE_USER_X=me", "filled in by musdash"},
	} {
		form := url.Values{"compose": {changed}, "variables": {"API_KEY=k"}}
		form.Set(field, c[0])
		res, body := a.post(composePage, composePage, form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), c[1]) {
			t.Errorf("%s=%q: status %d, want 422 with %q", field, c[0], res.StatusCode, c[1])
		}
	}
}

func TestServiceEndpointDomain(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("front", "3000", nil)
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	other := a.newService(projectID, env, "second", url.Values{})
	appID := a.newApp(projectID, env, "web", false, url.Values{"domain": {"taken.example.com"}})
	_ = appID
	endpoints, _ := a.db.ListEndpoints(ctx, s.ID)
	otherEndpoints, _ := a.db.ListEndpoints(ctx, other.ID)
	settings := a.servicePath(s.ID) + "/domains"
	// The form sends what the handler reads: the scheme of each endpoint's
	// dialog under the one name, its button under an id of its own.
	if _, page := a.get(settings); !strings.Contains(page, `id="scheme-`+endpoints[0].ID+`"`) || strings.Contains(page, `name="scheme-`) {
		t.Fatal("the scheme of an endpoint is not sent as scheme")
	}
	save := func(endpointID string, form url.Values) (*http.Response, string) {
		res, body := a.post(settings, a.servicePath(s.ID)+"/domains/"+endpointID, form)
		return res, html.UnescapeString(body)
	}

	_, page := a.get(settings)
	if !strings.Contains(page, endpoints[0].Host) || !strings.Contains(page, "Domain for front:3000") {
		t.Fatal("the Domains tab does not show the endpoint's domain")
	}
	// The file's own endpoint is changed here and not removed.
	if !strings.Contains(page, "From the file") || strings.Contains(page, "/domains/"+endpoints[0].ID+"/delete") {
		t.Fatal("an endpoint the file names is not marked as the file's, or is offered for removal")
	}

	res, _ := save(endpoints[0].ID, url.Values{"host": {"Shop.Example.com"}, "scheme": {"https"}})
	wantRedirect(t, res, settings)
	got, _ := a.db.ListEndpoints(ctx, s.ID)
	if got[0].Host != "shop.example.com" || !got[0].TLS {
		t.Fatalf("after saving a domain: %+v", got[0])
	}
	// Routed at once, with a certificate; the old address is gone.
	routes := a.routesFile()
	if !strings.Contains(routes, `"host": "shop.example.com"`) || !strings.Contains(routes, `"tls": true`) || strings.Contains(routes, endpoints[0].Host) {
		t.Fatalf("routes: %s", routes)
	}
	var domains int
	a.db.QueryRow(`SELECT count(*) FROM domains WHERE resource_kind = 'service' AND resource_id = ?`, endpoints[0].ID).Scan(&domains)
	if domains != 1 {
		t.Fatalf("the endpoint has %d domains, want exactly 1", domains)
	}
	// The next deployment tells the service its new address.
	a.post(settings, a.servicePath(s.ID)+"/deploy", nil)
	a.waitService(s.ID)
	envFile, _, _ := a.fake.File(a.cfg.AppDir(s.ID) + "/sandbox.env")
	if !strings.Contains(envFile, "SERVICE_FQDN_FRONT_3000=shop.example.com\n") {
		t.Fatalf("variables after the change:\n%s", envFile)
	}
	// Only the scheme changed: the same name is not "taken" by itself.
	res, _ = save(endpoints[0].ID, url.Values{"host": {"shop.example.com"}, "scheme": {"http"}})
	wantRedirect(t, res, settings)
	if got, _ := a.db.ListEndpoints(ctx, s.ID); got[0].TLS {
		t.Fatal("HTTPS was not switched off")
	}

	for name, c := range map[string]struct {
		form url.Values
		want string
	}{
		"not a domain":          {url.Values{"host": {"http://x/y"}}, "Enter a domain such as"},
		"taken by an app":       {url.Values{"host": {"taken.example.com"}}, "already routed"},
		"taken by an endpoint":  {url.Values{"host": {otherEndpoints[0].Host}}, "already routed"},
		"generated, with HTTPS": {url.Values{"host": {"abcd1234.127.0.0.1.sslip.io"}, "scheme": {"https"}}, "served over plain HTTP"},
		"a scheme of its own":   {url.Values{"host": {"new.example.com"}, "scheme": {"ftp"}}, "Choose https or http"},
	} {
		res, body := save(endpoints[0].ID, c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%s: status %d, want 422 with %q", name, res.StatusCode, c.want)
		}
	}
	// An endpoint of another service cannot be changed through this one.
	if res, _ := save(otherEndpoints[0].ID, url.Values{"host": {"mine.example.com"}}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another service's endpoint: %d", res.StatusCode)
	}
	if got, _ := a.db.ListEndpoints(ctx, other.ID); got[0].Host != otherEndpoints[0].Host {
		t.Fatal("another service's endpoint was changed")
	}
}

func TestServiceFailureStopAndDelete(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")

	// A file that asks for too much: refused, and the page says why without
	// letting the text act as markup.
	a.stackServer("front", "3000", func(line string) (string, error, bool) {
		if strings.Contains(line, "config --format json") && !strings.Contains(line, "--no-interpolate") {
			return `{"name":"x","services":{"front":{"image":"<img src=x>","privileged":true},"db":{"image":"db:1","pid":"host"}}}`, nil, true
		}
		return "", nil, false
	})
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	if s.Status != db.AppFailed {
		t.Fatalf("%+v", s)
	}
	_, overview := a.get(a.servicePath(s.ID))
	plain := html.UnescapeString(overview)
	if !strings.Contains(plain, "The last deployment failed") || !strings.Contains(plain, `service front: "privileged" is not allowed`) || !strings.Contains(plain, `service db: "pid" is not allowed`) {
		t.Fatal("the overview does not explain the refusal")
	}
	if countCalls(a.fake.Calls(), "docker compose") != 0 {
		t.Fatal("Compose ran on the server for a refused file")
	}

	// Compose's own complaint about a broken file.
	a.stackServer("front", "3000", func(line string) (string, error, bool) {
		if strings.Contains(line, "config --format json") {
			return "", runnertest.Exit("docker", 15, "validating stdin: services.front Additional property imagee is not allowed <script>"), true
		}
		return "", nil, false
	})
	a.post(a.servicePath(s.ID), a.servicePath(s.ID)+"/deploy", nil)
	s = a.waitService(s.ID)
	_, overview = a.get(a.servicePath(s.ID))
	if !strings.Contains(html.UnescapeString(overview), "Additional property imagee is not allowed") || strings.Contains(overview, "<script>") {
		t.Fatal("Compose's message is not shown, or is shown unescaped")
	}

	// Then a good deployment, stop, and delete.
	a.stackServer("front", "3000", nil)
	a.post(a.servicePath(s.ID), a.servicePath(s.ID)+"/deploy", nil)
	if s = a.waitService(s.ID); s.Status != db.AppRunning {
		t.Fatalf("%+v", s)
	}
	endpoints, _ := a.db.ListEndpoints(ctx, s.ID)

	res, _ := a.post(a.servicePath(s.ID), a.servicePath(s.ID)+"/stop", nil)
	wantRedirect(t, res, a.servicePath(s.ID))
	if got, _ := a.db.ServiceByID(ctx, s.ID); got.Status != db.AppStopped {
		t.Fatalf("after stop: %+v", got)
	}
	if strings.Contains(a.routesFile(), endpoints[0].Host) {
		t.Fatal("a stopped service is still routed")
	}
	_, page := a.get(a.servicePath(s.ID))
	if !strings.Contains(page, "Stopped") || strings.Contains(page, "Redeploy") {
		t.Fatal("a stopped service does not offer Deploy")
	}
	res, _ = a.get(a.servicePath(s.ID) + "/logs/stream")
	wantStatus(t, res, http.StatusConflict)

	// The project cannot go while it holds a service.
	projectSettings := "/projects/" + projectID + "/settings"
	res, _ = a.post(projectSettings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, projectSettings)

	settings := a.servicePath(s.ID) + "/settings"
	res, _ = a.post(settings, a.servicePath(s.ID)+"/delete", url.Values{"confirm": {"wrong"}, "delete_data": {"1"}})
	wantRedirect(t, res, settings)
	if _, err := a.db.ServiceByID(ctx, s.ID); err != nil {
		t.Fatal("deleted without the name being typed")
	}
	res, _ = a.post(settings, a.servicePath(s.ID)+"/delete", url.Values{"confirm": {"site"}, "delete_data": {"1"}})
	wantRedirect(t, res, "/projects/"+projectID+"/env/"+env.ID)
	down := a.fake.Calls()[lastIndexOfCall(a.fake.Calls(), " down ")]
	if !strings.Contains(down, "--project-name "+deploy.ServiceProject(s.ID)) || !strings.HasSuffix(down, "--volumes") {
		t.Fatalf("down: %s", down)
	}
	res, _ = a.get(a.servicePath(s.ID))
	wantStatus(t, res, http.StatusNotFound)

	res, _ = a.post(projectSettings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, "/projects")
}

func TestOtherTeamsServiceIsNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	sealed, _ := a.server.Deploy.SealServiceVariables(map[string]string{"SERVICE_PASSWORD_DB": "their-generated-password", "API_KEY": "their-key"})
	other, err := a.db.CreateService(ctx, "otherteam", db.Service{
		EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-stack", Template: db.TemplateCustom, Compose: ownCompose, Variables: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SyncEndpoints(ctx, other.ID, []string{"FRONT"}, func(string) (string, bool) { return "theirs.example.com", true }); err != nil {
		t.Fatal(err)
	}
	theirs, _ := a.db.ListEndpoints(ctx, other.ID)
	base := a.servicePath(other.ID)

	for _, path := range []string{"", "/status", "/deploy-log/stream", "/logs", "/logs/stream", "/compose", "/domains", "/domains/" + theirs[0].ID + "/dns", "/settings"} {
		res, body := a.get(base + path)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "secret-stack") || strings.Contains(body, "their-") {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	token := a.csrf("/projects")
	for path, form := range map[string]url.Values{
		"/deploy":                              {},
		"/stop":                                {},
		"/compose":                             {"compose": {"services:\n  a:\n    image: x\n"}},
		"/domains":                             {"service": {"front"}, "port": {"3000"}, "host": {"grab.example.com"}},
		"/domains/" + theirs[0].ID:             {"host": {"mine.example.com"}},
		"/domains/" + theirs[0].ID + "/delete": {},
		"/delete":                              {"confirm": {"secret-stack"}, "delete_data": {"1"}},
	} {
		form.Set("_csrf", token)
		if res, _ := a.postRaw(a.client, base+path, form, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, res.StatusCode)
		}
	}
	// Their endpoint cannot be reached through a service of ours either.
	projectID, env := a.project("Mine")
	mine := a.newService(projectID, env, "site", url.Values{})
	res, _ := a.post(a.servicePath(mine.ID)+"/domains", a.servicePath(mine.ID)+"/domains/"+theirs[0].ID, url.Values{"host": {"mine.example.com"}})
	wantStatus(t, res, http.StatusNotFound)
	// One they added themselves is the kind that can be removed at all.
	added, err := a.db.AddEndpoint(ctx, "otherteam", other.ID, "front", 3000, "added.theirs.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{theirs[0].ID, added.ID} {
		res, _ = a.post(a.servicePath(mine.ID)+"/domains", a.servicePath(mine.ID)+"/domains/"+id+"/delete", nil)
		wantStatus(t, res, http.StatusNotFound)
	}
	if list, _ := a.db.ListEndpoints(ctx, other.ID); len(list) != 2 {
		t.Fatalf("their endpoints after the attempts: %+v", list)
	}
	if res, _ := a.get(a.servicePath(mine.ID) + "/domains/" + theirs[0].ID + "/dns"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("their domain's DNS through a service of ours: %d", res.StatusCode)
	}
	// Nor can a service be created in their environment.
	res, _ = a.post("/projects/"+projectID+"/env/"+env.ID+"/service/new?template=custom", "/projects/"+projectID+"/env/"+envs[0].ID+"/service",
		url.Values{"template": {db.TemplateCustom}, "name": {"x"}, "compose": {ownCompose}})
	wantStatus(t, res, http.StatusNotFound)

	got, _ := a.db.ServiceByID(ctx, other.ID)
	after, _ := a.db.ListEndpoints(ctx, other.ID)
	if got.Compose != ownCompose || got.Status != db.AppCreated || after[0].Host != "theirs.example.com" || len(after) != 2 {
		t.Fatalf("the other team's service was changed: %+v %+v", got, after)
	}
	if n := countCalls(a.fake.Calls(), "docker"); n != 0 {
		t.Fatalf("%d docker commands ran for another team's service", n)
	}
}

// TestDomainsOfAStack: a domain is given to any service of a stack from its
// Domains tab, with no magic variable in the file, and taken away again.
func TestDomainsOfAStack(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("front", "3000", nil)
	// Not deployed yet: nothing has read the file, so the service is typed.
	s := a.newService(projectID, env, "site", url.Values{})
	tab := a.servicePath(s.ID) + "/domains"
	add := func(form url.Values) (*http.Response, string) {
		res, body := a.post(tab, tab, form)
		return res, html.UnescapeString(body)
	}
	_, page := a.get(tab)
	if !strings.Contains(page, `id="service" name="service"`) || strings.Contains(page, `aria-controls="service-list"`) {
		t.Fatal("before a deployment the service is not a text field")
	}
	if _, overview := a.get(a.servicePath(s.ID)); !strings.Contains(overview, tab) {
		t.Fatal("the overview does not lead to the Domains tab")
	}
	res, _ := add(url.Values{"service": {"db"}, "port": {"5432"}, "scheme": {"http"}, "host": {"Early.Example.com"}})
	wantRedirect(t, res, tab)
	if routes := a.routesFile(); strings.Contains(routes, "early.example.com") {
		t.Fatalf("a stack that was never started is routed: %s", routes)
	}

	a.post(tab, a.servicePath(s.ID)+"/deploy", nil)
	s = a.waitService(s.ID)
	if s.Status != db.AppRunning {
		t.Fatalf("status %s: %s", s.Status, s.LastError)
	}
	// Deployed: the file's services are offered, each with its ports, and
	// the domain from before is served.
	_, page = a.get(tab)
	page = html.UnescapeString(page)
	for _, want := range []string{`aria-controls="service-list"`, `data-value="front"`, `data-value="db"`, "no port named", "early.example.com", "db:5432", "From the file"} {
		if !strings.Contains(page, want) {
			t.Errorf("the tab of a deployed stack is missing %q", want)
		}
	}
	if !strings.Contains(a.routesFile(), `"host": "early.example.com"`) {
		t.Fatalf("the added domain is not routed after the deployment: %s", a.routesFile())
	}

	// A second domain for a published target is served at once; one for a
	// port nothing publishes yet says to redeploy.
	res, _ = add(url.Values{"service": {"db"}, "port": {"5432"}, "scheme": {"https"}, "host": {"again.example.com"}})
	wantRedirect(t, res, tab)
	if !strings.Contains(a.routesFile(), `"host": "again.example.com"`) {
		t.Fatal("a second domain for a published port was not routed at once")
	}
	res, _ = add(url.Values{"service": {"front"}, "port": {"9000"}, "scheme": {"https"}, "host": {"admin.example.com"}})
	wantRedirect(t, res, tab)
	_, page = a.get(tab)
	page = html.UnescapeString(page)
	if !strings.Contains(page, "Redeploy the service to serve it: front:9000 is not published to the proxy yet.") || !strings.Contains(page, "Redeploy to serve") {
		t.Fatal("a domain for an unpublished port does not say to redeploy")
	}
	if strings.Contains(a.routesFile(), "admin.example.com") {
		t.Fatal("a domain whose port is not published is routed")
	}

	for name, c := range map[string]struct {
		form url.Values
		want string
	}{
		"a service the file does not have": {url.Values{"service": {"api"}, "port": {"80"}, "host": {"x.example.com"}}, "The Compose file has no service called api. It has db, front."},
		"a name that is not one":           {url.Values{"service": {"-x; rm"}, "port": {"80"}, "host": {"x.example.com"}}, "Enter the service's name"},
		"no port":                          {url.Values{"service": {"front"}, "port": {""}, "host": {"x.example.com"}}, "Enter 1 to 65535."},
		"a port out of range":              {url.Values{"service": {"front"}, "port": {"70000"}, "host": {"x.example.com"}}, "Enter 1 to 65535."},
		"not a domain":                     {url.Values{"service": {"front"}, "port": {"80"}, "host": {"http://x/y"}}, "Enter a domain such as"},
		"taken":                            {url.Values{"service": {"front"}, "port": {"80"}, "host": {"early.example.com"}}, "already routed"},
		"generated, with https":            {url.Values{"service": {"front"}, "port": {"80"}, "scheme": {"https"}, "host": {"abcd1234.127.0.0.1.sslip.io"}}, "served over plain HTTP"},
	} {
		res, body := add(c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%s: status %d, want 422 with %q", name, res.StatusCode, c.want)
		}
		// The dialog comes back open with what was typed.
		if !strings.Contains(body, "data-autoopen") {
			t.Errorf("%s: the dialog is not open again", name)
		}
	}

	// Check DNS reads the stored name of the service's own endpoint.
	var asked []string
	a.server.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		asked = append(asked, host)
		return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
	}
	list, _ := a.db.ListEndpoints(ctx, s.ID)
	var early, file db.Endpoint
	for _, e := range list {
		switch {
		case e.Host == "early.example.com":
			early = e
		case !e.Manual:
			file = e
		}
	}
	res, body := a.get(tab + "/" + early.ID + "/dns")
	if res.StatusCode != http.StatusOK || len(asked) != 1 || asked[0] != "early.example.com" || strings.Contains(body, "<html") {
		t.Fatalf("Check DNS: %d, asked %v", res.StatusCode, asked)
	}

	// Removing: only what a person added, and the route goes with it.
	res, _ = a.post(tab, tab+"/"+file.ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post(tab, tab+"/"+early.ID+"/delete", nil)
	wantRedirect(t, res, tab)
	if routes := a.routesFile(); strings.Contains(routes, "early.example.com") || !strings.Contains(routes, "again.example.com") {
		t.Fatalf("after removing a domain: %s", routes)
	}

	// The file is saved with another text: the list of services is no
	// longer known, and the domains a person added are still there.
	res, _ = a.post(a.servicePath(s.ID)+"/compose", a.servicePath(s.ID)+"/compose", url.Values{"compose": {ownCompose + "# changed\n"}, "variables_kept": {"1"}})
	wantRedirect(t, res, a.servicePath(s.ID)+"/compose")
	_, page = a.get(tab)
	if !strings.Contains(page, `id="service" name="service"`) || !strings.Contains(page, "again.example.com") || !strings.Contains(page, "admin.example.com") {
		t.Fatal("after the file changed: the service is not typed again, or an added domain is gone")
	}
}

// TestStackContainersAndLogsByService: the Overview asks the server about
// the stack's containers and shows each with its state, a service of the
// file that has none is named, and the logs can be narrowed to one of the
// stack's own services and to nothing else.
func TestStackContainersAndLogsByService(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	var logs []string
	a.stackServer("front", "3000", func(line string) (string, error, bool) {
		switch {
		case strings.HasPrefix(line, "docker ps --all") && strings.Contains(line, "label=musdash.kind=service"):
			id := line[strings.Index(line, "musdash.resource=")+len("musdash.resource="):]
			id = id[:strings.IndexByte(id, ' ')]
			return "musdash-" + id + "-front-1\trunning\tUp 3 minutes (unhealthy)\tmain:1\tfront\t0.0.0.0:8088->3000/tcp, 127.0.0.1:20001->3000/tcp\n" +
				"musdash-" + id + "-job-1\texited\tExited (0) 2 minutes ago\tjob:1\tjob\t\n" +
				"musdash-" + id + "-odd-1\trunning\t<script>alert(1)</script>\tx\t<i>odd</i>\t\n", nil, true
		case strings.Contains(line, " logs --follow"):
			logs = append(logs, line)
			return "front-1  | ready\n", nil, true
		}
		return "", nil, false
	})
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	base := a.servicePath(s.ID)

	_, overview := a.get(base)
	if !strings.Contains(overview, `hx-get="`+base+`/containers"`) {
		t.Fatal("the overview does not ask for the stack's containers")
	}
	res, body := a.get(base + "/containers")
	wantStatus(t, res, http.StatusOK)
	plain := html.UnescapeString(body)
	for _, want := range []string{
		"Unhealthy", "Up 3 minutes (unhealthy)", "8088 → 3000", // the web container, and the port outside
		"Finished", "Exited (0) 2 minutes ago", // one that did its job
		"No container", // db is in the file and has none
		base + "/logs?service=front", base + "/terminal?container=musdash-" + s.ID + "-front-1",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("the containers are missing %q", want)
		}
	}
	// The loopback port is the proxy's, and what a server says is text.
	if strings.Contains(plain, "20001") || strings.Contains(body, "<script>") || strings.Contains(body, "<i>odd</i>") {
		t.Fatalf("the containers fragment shows what it should not:\n%s", body)
	}
	if strings.Contains(body, "<html") {
		t.Fatal("the containers answer is a page, not a fragment")
	}
	// It asks again when the stack's state changes, not on a timer: each
	// answer is a command on the server.
	if !strings.Contains(body, `hx-trigger="stack-changed from:body"`) || strings.Contains(body, "every ") {
		t.Fatalf("when the fragment asks again:\n%s", body)
	}
	// The header says so when it has something new to show, and only then.
	res, header := a.get(base + "/status")
	if res.Header.Get("HX-Trigger") != "stack-changed" {
		t.Fatalf("a header with something new: HX-Trigger %q", res.Header.Get("HX-Trigger"))
	}
	seen := regexp.MustCompile(`status\?was=running&(?:amp;)?seen=([0-9a-f]+)`).FindStringSubmatch(header)
	if seen == nil {
		t.Fatalf("the header does not ask for itself again:\n%s", header)
	}
	if res, _ := a.get(base + "/status?was=running&seen=" + seen[1]); res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Trigger") != "" {
		t.Fatalf("an unchanged header: %d, HX-Trigger %q", res.StatusCode, res.Header.Get("HX-Trigger"))
	}

	// Logs: all of them, one service's, and a name the stack does not have.
	_, page := a.get(base + "/logs")
	if !strings.Contains(page, base+"/logs?service=db") || !strings.Contains(page, "All services") {
		t.Fatal("the Logs tab does not offer the stack's services")
	}
	_, page = a.get(base + "/logs?service=db")
	if !strings.Contains(page, `sse-connect="`+base+`/logs/stream?service=db"`) {
		t.Fatal("the Logs tab of one service does not stream that service")
	}
	for query, want := range map[string]string{
		"":                       "--tail 200",
		"?service=db":            "--tail 200 -- db",
		"?service=nope":          "--tail 200",
		"?service=--volumes":     "--tail 200",
		"?service=db%20--follow": "--tail 200",
	} {
		logs = nil
		res, _ := a.get(base + "/logs/stream" + query)
		wantStatus(t, res, http.StatusOK)
		if len(logs) != 1 || !strings.HasSuffix(logs[0], want) {
			t.Errorf("logs%s ran %v, want one command ending in %q", query, logs, want)
		}
	}
}

// TestWhatAComposeTextReads: the forms list the variables a Compose text
// reads as it is typed, by what each needs, without loading the file.
func TestWhatAComposeTextReads(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	form := "/projects/" + projectID + "/env/" + env.ID + "/service/new?template=custom"
	reads := "/projects/" + projectID + "/env/" + env.ID + "/service/reads"
	_, page := a.get(form)
	if !strings.Contains(page, `hx-post="`+reads+`"`) || !strings.Contains(page, `id="compose-reads"`) || strings.Contains(page, "What the file reads") {
		t.Fatal("the new-service form does not ask what its text reads, or lists something before there is a text")
	}
	ask := func(path string, form url.Values) string {
		res, body := a.post(path, path, form)
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(body, "<html") {
			t.Fatal("the answer is a page, not a fragment")
		}
		return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " "))
	}
	in := func(text, label string, names ...string) bool {
		i := strings.Index(text, label)
		if i < 0 {
			return false
		}
		rest := text[i:]
		if j := strings.Index(rest[len(label):], "  Has a "); j >= 0 && label == "Needs a value" {
			rest = rest[:len(label)+j]
		}
		for _, n := range names {
			if !strings.Contains(rest, n) {
				return false
			}
		}
		return true
	}
	got := ask(reads, url.Values{"compose": {ownCompose}})
	if !in(got, "Needs a value", "API_KEY") || !in(got, "Has a default", "REGION") || !in(got, "Generated", "SERVICE_PASSWORD_DB") || !in(got, "Gets a domain", "front") {
		t.Fatalf("what the text reads:\n%s", got)
	}
	// A value typed under Variables is no longer missing; an empty one is.
	got = ask(reads, url.Values{"compose": {ownCompose}, "variables": {"API_KEY=k-1"}})
	if strings.Contains(got, "Needs a value") || !in(got, "Has a value", "API_KEY") {
		t.Fatalf("with the value given:\n%s", got)
	}
	if got = ask(reads, url.Values{"compose": {ownCompose}, "variables": {"API_KEY="}}); !in(got, "Needs a value", "API_KEY") {
		t.Fatalf("with an empty value:\n%s", got)
	}
	if got = ask(reads, url.Values{"compose": {"services:\n  a:\n    image: x\n"}}); !strings.Contains(got, "No variables") {
		t.Fatalf("a text with no variables:\n%s", got)
	}
	// A name in the text is text on the page.
	res, body := a.post(reads, reads, url.Values{"compose": {"x: ${A<b>}\ny: ${OK}"}})
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(body, "<b>") {
		t.Fatal("a name from the text reached the page as markup")
	}
	if got = ask(reads, url.Values{"compose": {""}}); strings.TrimSpace(got) != "" {
		t.Fatalf("no text: %q", got)
	}

	// The Compose tab counts what is stored as given, without showing it.
	a.stackServer("front", "3000", nil)
	s := a.newService(projectID, env, "site", url.Values{})
	tab := a.servicePath(s.ID) + "/compose"
	_, page = a.get(tab)
	plain := html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(page, " "))
	if !in(plain, "Has a value", "API_KEY") || strings.Contains(plain, "Needs a value") || strings.Contains(page, "k-123") {
		t.Fatal("the Compose tab does not count the stored value as given, or shows it")
	}
	got = ask(tab+"/reads", url.Values{"compose": {ownCompose + "      - NEW=${NEW_ONE}\n"}, "variables_kept": {"1"}})
	if !in(got, "Needs a value", "NEW_ONE") || !in(got, "Has a value", "API_KEY") || strings.Contains(got, "k-123") {
		t.Fatalf("the Compose tab's answer:\n%s", got)
	}
}
