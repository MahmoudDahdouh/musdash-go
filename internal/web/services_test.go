package web

import (
	"context"
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
	page := "/projects/" + projectID + "/e/" + env.ID + "/services/new?template=" + full.Get("template")
	res, body := a.post(page, "/projects/"+projectID+"/e/"+env.ID+"/services", full)
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("create service: %d\n%s", res.StatusCode, body)
	}
	return a.waitService(strings.TrimPrefix(res.Header.Get("Location"), "/services/"))
}

func (a *app) waitService(id string) db.Service {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := a.db.ServiceByID(context.Background(), id)
		if err != nil {
			a.t.Fatal(err)
		}
		if s.Status != db.AppDeploying {
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
	base := "/projects/" + projectID + "/e/" + env.ID + "/services/new"

	// The Add resource page is the catalogue; the address without a
	// template leads to it.
	res, _ := a.get(base)
	wantRedirect(t, res, "/projects/"+projectID+"/e/"+env.ID+"/new")
	res, page := a.get("/projects/" + projectID + "/e/" + env.ID + "/new")
	wantStatus(t, res, http.StatusOK)
	for _, tpl := range catalog.Services() {
		if !strings.Contains(page, "template="+tpl.Key) || !strings.Contains(html.UnescapeString(page), tpl.Name) {
			t.Errorf("the catalogue is missing %s", tpl.Name)
		}
	}
	if !strings.Contains(page, "template=custom") {
		t.Error("the catalogue does not offer a person's own Compose file")
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
	res, body := a.post(base+"?template=cloudflared", "/projects/"+projectID+"/e/"+env.ID+"/services",
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

	page := "/projects/" + projectID + "/e/" + env.ID + "/services/new?template=wordpress"
	res, body := a.post(page, "/projects/"+projectID+"/e/"+env.ID+"/services",
		url.Values{"template": {"wordpress"}, "name": {"blog"}, "deploy": {"1"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", res.StatusCode, body)
	}
	s := a.waitService(strings.TrimPrefix(res.Header.Get("Location"), "/services/"))
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

	// The overview: address, containers, generated values behind bullets.
	res, overview := a.get("/services/" + s.ID)
	wantStatus(t, res, http.StatusOK)
	overview = html.UnescapeString(overview)
	for _, want := range []string{"Running", "WordPress", endpoints[0].Host, "to wordpress:80", "db, wordpress", "Redeploy", "Stop",
		`data-copy="` + vars["SERVICE_PASSWORD_ROOT"] + `"`, "SERVICE_USER_WORDPRESS", "/services/" + s.ID + "/deploy-log/stream"} {
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
	for _, path := range []string{"/projects/" + projectID + "/e/" + env.ID, "/services/" + s.ID + "/settings", "/services/" + s.ID + "/logs", "/services/" + s.ID + "/compose", "/services/" + s.ID + "/status"} {
		res, body := a.get(path)
		wantStatus(t, res, http.StatusOK)
		for name, value := range vars {
			if strings.Contains(body, value) {
				t.Errorf("%s shows %s", path, name)
			}
		}
	}
	_, list := a.get("/projects/" + projectID + "/e/" + env.ID)
	if !strings.Contains(list, "/services/"+s.ID) || !strings.Contains(list, "blog") {
		t.Fatal("the project page does not list the service")
	}

	// The deployment's output, and the containers' own.
	res, stream := a.get("/services/" + s.ID + "/deploy-log/stream")
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
	res, stream = a.get("/services/" + s.ID + "/logs/stream")
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
	page := "/projects/" + projectID + "/e/" + env.ID + "/services/new?template=custom"
	create := "/projects/" + projectID + "/e/" + env.ID + "/services"
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
	if res, _ := a.post(page, "/projects/"+projectID+"/e/"+otherEnv.ID+"/services", valid); res.StatusCode != http.StatusNotFound {
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
	_, overview := a.get("/services/" + s.ID)
	if !strings.Contains(overview, "The Compose file reads API_KEY, which has no value yet") || strings.Contains(overview, "REGION,") {
		t.Fatal("the overview does not name the variable that still needs a value")
	}
	// And an app cannot take its name.
	res, body := a.post("/projects/"+projectID+"/e/"+env.ID+"/apps/new", "/projects/"+projectID+"/e/"+env.ID+"/apps",
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
	composePage := "/services/" + s.ID + "/compose"

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
	if !strings.Contains(field, "API_KEY=k-123") || strings.Contains(field, "variables_kept") || strings.Contains(field, before["SERVICE_PASSWORD_DB"]) {
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
	wantRedirect(t, res, "/services/"+s.ID)
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
	settings := "/services/" + s.ID + "/settings"
	// The form sends what the handler reads: the HTTPS box is "tls", and
	// the box's own id is not a second name for it.
	if _, page := a.get(settings); !strings.Contains(page, `id="tls-`+endpoints[0].ID+`" name="tls"`) || strings.Contains(page, `name="tls-`) {
		t.Fatal("the HTTPS box of an endpoint is not sent as tls")
	}
	save := func(endpointID string, form url.Values) (*http.Response, string) {
		res, body := a.post(settings, "/services/"+s.ID+"/endpoints/"+endpointID, form)
		return res, html.UnescapeString(body)
	}

	_, page := a.get(settings)
	if !strings.Contains(page, endpoints[0].Host) || !strings.Contains(page, "Domain for front:3000") {
		t.Fatal("the settings page does not show the endpoint's domain")
	}

	res, _ := save(endpoints[0].ID, url.Values{"host": {"Shop.Example.com"}, "tls": {"1"}})
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
	a.post(settings, "/services/"+s.ID+"/deploy", nil)
	a.waitService(s.ID)
	envFile, _, _ := a.fake.File(a.cfg.AppDir(s.ID) + "/sandbox.env")
	if !strings.Contains(envFile, "SERVICE_FQDN_FRONT_3000=shop.example.com\n") {
		t.Fatalf("variables after the change:\n%s", envFile)
	}
	// Only the HTTPS box changed: the same name is not "taken" by itself.
	res, _ = save(endpoints[0].ID, url.Values{"host": {"shop.example.com"}})
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
		"generated, with HTTPS": {url.Values{"host": {"abcd1234.127.0.0.1.sslip.io"}, "tls": {"1"}}, "served over plain HTTP"},
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
	_, overview := a.get("/services/" + s.ID)
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
	a.post("/services/"+s.ID, "/services/"+s.ID+"/deploy", nil)
	s = a.waitService(s.ID)
	_, overview = a.get("/services/" + s.ID)
	if !strings.Contains(html.UnescapeString(overview), "Additional property imagee is not allowed") || strings.Contains(overview, "<script>") {
		t.Fatal("Compose's message is not shown, or is shown unescaped")
	}

	// Then a good deployment, stop, and delete.
	a.stackServer("front", "3000", nil)
	a.post("/services/"+s.ID, "/services/"+s.ID+"/deploy", nil)
	if s = a.waitService(s.ID); s.Status != db.AppRunning {
		t.Fatalf("%+v", s)
	}
	endpoints, _ := a.db.ListEndpoints(ctx, s.ID)

	res, _ := a.post("/services/"+s.ID, "/services/"+s.ID+"/stop", nil)
	wantRedirect(t, res, "/services/"+s.ID)
	if got, _ := a.db.ServiceByID(ctx, s.ID); got.Status != db.AppStopped {
		t.Fatalf("after stop: %+v", got)
	}
	if strings.Contains(a.routesFile(), endpoints[0].Host) {
		t.Fatal("a stopped service is still routed")
	}
	_, page := a.get("/services/" + s.ID)
	if !strings.Contains(page, "Stopped") || strings.Contains(page, "Redeploy") {
		t.Fatal("a stopped service does not offer Deploy")
	}
	res, _ = a.get("/services/" + s.ID + "/logs/stream")
	wantStatus(t, res, http.StatusConflict)

	// The project cannot go while it holds a service.
	projectSettings := "/projects/" + projectID + "/settings"
	res, _ = a.post(projectSettings, "/projects/"+projectID+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, projectSettings)

	settings := "/services/" + s.ID + "/settings"
	res, _ = a.post(settings, "/services/"+s.ID+"/delete", url.Values{"confirm": {"wrong"}, "delete_data": {"1"}})
	wantRedirect(t, res, settings)
	if _, err := a.db.ServiceByID(ctx, s.ID); err != nil {
		t.Fatal("deleted without the name being typed")
	}
	res, _ = a.post(settings, "/services/"+s.ID+"/delete", url.Values{"confirm": {"site"}, "delete_data": {"1"}})
	wantRedirect(t, res, "/projects/"+projectID+"/e/"+env.ID)
	down := a.fake.Calls()[lastIndexOfCall(a.fake.Calls(), " down ")]
	if !strings.Contains(down, "--project-name "+deploy.ServiceProject(s.ID)) || !strings.HasSuffix(down, "--volumes") {
		t.Fatalf("down: %s", down)
	}
	res, _ = a.get("/services/" + s.ID)
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
	base := "/services/" + other.ID

	for _, path := range []string{"", "/status", "/deploy-log/stream", "/logs", "/logs/stream", "/compose", "/settings"} {
		res, body := a.get(base + path)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "secret-stack") || strings.Contains(body, "their-") {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	token := a.csrf("/projects")
	for path, form := range map[string]url.Values{
		"/deploy":                    {},
		"/stop":                      {},
		"/compose":                   {"compose": {"services:\n  a:\n    image: x\n"}},
		"/endpoints/" + theirs[0].ID: {"host": {"mine.example.com"}},
		"/delete":                    {"confirm": {"secret-stack"}, "delete_data": {"1"}},
	} {
		form.Set("_csrf", token)
		if res, _ := a.postRaw(a.client, base+path, form, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, res.StatusCode)
		}
	}
	// Their endpoint cannot be reached through a service of ours either.
	projectID, env := a.project("Mine")
	mine := a.newService(projectID, env, "site", url.Values{})
	res, _ := a.post("/services/"+mine.ID+"/settings", "/services/"+mine.ID+"/endpoints/"+theirs[0].ID, url.Values{"host": {"mine.example.com"}})
	wantStatus(t, res, http.StatusNotFound)
	// Nor can a service be created in their environment.
	res, _ = a.post("/projects/"+projectID+"/e/"+env.ID+"/services/new?template=custom", "/projects/"+projectID+"/e/"+envs[0].ID+"/services",
		url.Values{"template": {db.TemplateCustom}, "name": {"x"}, "compose": {ownCompose}})
	wantStatus(t, res, http.StatusNotFound)

	got, _ := a.db.ServiceByID(ctx, other.ID)
	after, _ := a.db.ListEndpoints(ctx, other.ID)
	if got.Compose != ownCompose || got.Status != db.AppCreated || after[0].Host != "theirs.example.com" {
		t.Fatalf("the other team's service was changed: %+v %+v", got, after)
	}
	if n := countCalls(a.fake.Calls(), "docker"); n != 0 {
		t.Fatalf("%d docker commands ran for another team's service", n)
	}
}
