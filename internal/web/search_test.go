package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// find asks the search what it has for a text.
func (a *app) find(text string) string {
	a.t.Helper()
	res, body := a.get("/search?q=" + url.QueryEscape(text))
	wantStatus(a.t, res, http.StatusOK)
	return body
}

// optionRE is one row of the search's answer: where it leads and all it says.
var optionRE = regexp.MustCompile(`(?s)<a class="menu-item search-hit" role="option"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)

// found is where the rows of an answer lead, in their order.
func found(body string) []string {
	var out []string
	for _, m := range optionRE.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// owned is the rows among hrefs that are the team's own things and pages:
// without what the catalogue offers to deploy, which is listed after them.
func owned(hrefs []string) []string {
	var out []string
	for _, h := range hrefs {
		if !strings.HasPrefix(h, "/deploy/") {
			out = append(out, h)
		}
	}
	return out
}

// rowTo is the row of an answer that leads to href.
func rowTo(t *testing.T, body, href string) string {
	t.Helper()
	for _, m := range optionRE.FindAllStringSubmatch(body, -1) {
		if m[1] == href {
			return m[2]
		}
	}
	t.Fatalf("no row leads to %s:\n%s", href, body)
	return ""
}

// The bar of every signed-in page ends with the search and then the
// person, and the page has the one dialog the search is.
func TestSearchIsInTheBar(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	for _, page := range []string{"/", "/projects", "/projects/" + projectID + "/env/" + env.ID, "/account", "/nosuchpage"} {
		_, body := a.get(page)
		bar := between(t, page, body, `<div class="topbar">`, `<main class="page"`)
		end := between(t, page, bar, `<div class="topbar-end">`, `id="usermenu-list"`)
		button := strings.Index(end, "data-search-open")
		if button < 0 || button > strings.Index(end, `id="usermenu"`) {
			t.Errorf("%s: the search button is not in the bar before the person's menu", page)
		}
		if !regexp.MustCompile(`<button type="button" class="search-button"[^>]*aria-label="Search"[^>]*aria-keyshortcuts="/"`).MatchString(end) {
			t.Errorf("%s: the search button does not say what it is", page)
		}
		if n := strings.Count(body, `<dialog class="modal search" id="search"`); n != 1 {
			t.Errorf("%s: %d search dialogs", page, n)
		}
		dialog := between(t, page, body, `<dialog class="modal search" id="search"`, `</dialog>`)
		for _, want := range []string{
			`role="combobox"`, `name="q"`, `maxlength="100"`, `autocomplete="off"`, "data-search-field",
			`hx-get="/search"`, `hx-trigger="input delay:150ms, search-load"`, `hx-target="#search-results"`, `hx-sync="this:replace"`,
			`aria-controls="search-results"`, `id="search-results" role="listbox"`, `id="search-status"`, `role="status"`, "data-close",
		} {
			if !strings.Contains(dialog, want) {
				t.Errorf("%s: the search dialog lacks %s", page, want)
			}
		}
		// Results are asked for, never sent with the page.
		if strings.Contains(dialog, `role="option"`) {
			t.Errorf("%s: the page was sent with search results in it", page)
		}
	}
	// Signed out there is nothing to search.
	out := newApp(t, false)
	_, body := out.get("/setup")
	if strings.Contains(body, "data-search-open") || strings.Contains(body, `id="search"`) {
		t.Error("the setup page has the search")
	}
}

func TestSearchFindsWhatTheTeamHas(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", true, nil)
	a.newApp(projectID, env, "web-admin", false, nil)
	maindb := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	a.stackServer("front", "3000", nil)
	site := a.newService(projectID, env, "site", nil)
	servers, _ := a.db.ListServers(ctx, team)
	if _, err := a.db.AddDomain(ctx, team, servers[0].ID, db.Domain{ResourceKind: db.KindApp, ResourceID: web, Host: "shop.example.com", TLS: true}); err != nil {
		t.Fatal(err)
	}
	endpoints, err := a.db.ListEndpoints(ctx, site.ID)
	if err != nil || len(endpoints) == 0 {
		t.Fatalf("the service's endpoints: %v, %v", endpoints, err)
	}
	if err := a.db.SetEndpointDomain(ctx, site.ID, endpoints[0].ID, "press.example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetTags(ctx, team, db.KindApp, web, []string{"frontend"}); err != nil {
		t.Fatal(err)
	}
	base := "/projects/" + projectID

	// Each kind leads to its page, best match first.
	body := a.find("web")
	if got := strings.Join(found(body), " "); !strings.HasPrefix(got, a.appPath(web)+" "+a.appPath(a.appID(env, "web-admin"))) {
		t.Errorf("web: %s", got)
	}
	// A row says what it is, where it is, one fact of it and its state,
	// and marks what was typed in its name.
	row := rowTo(t, body, a.appPath(web))
	for _, want := range []string{"<mark>web</mark>", "App", "Shop", env.Name, "nginx:alpine", "Running", `class="icon`, "pill"} {
		if !strings.Contains(row, want) {
			t.Errorf("the app's row lacks %q:\n%s", want, row)
		}
	}
	if !regexp.MustCompile(`<mark>web</mark>-admin`).MatchString(rowTo(t, body, a.appPath(a.appID(env, "web-admin")))) {
		t.Error("the match is not marked where it is in the name")
	}
	for text, href := range map[string]string{
		"shop":      base,
		"Shop prod": base + "/env/" + env.ID,
		"maindb":    a.databasePath(maindb.ID),
		"postgres":  a.databasePath(maindb.ID),
		"site":      a.servicePath(site.ID),
		"shop.exam": a.appPath(web) + "/domains",
		"press.ex":  a.servicePath(site.ID),
		"localhost": "/servers/" + servers[0].ID,
		"frontend":  "/tags/frontend",
		web:         a.appPath(web),
	} {
		if got := found(a.find(text)); len(got) == 0 || got[0] != href {
			t.Errorf("%q leads to %v, want %s first", text, got, href)
		}
	}
	// A server's row leads to its page, which must be there to lead to.
	if res, _ := a.get("/servers/" + servers[0].ID); res.StatusCode != http.StatusOK {
		t.Errorf("the page a server's row leads to: %d", res.StatusCode)
	}
	if row := rowTo(t, a.find("shop.exam"), a.appPath(web)+"/domains"); !strings.Contains(row, "Domain of web") || !strings.Contains(row, "<mark>shop.exam</mark>ple.com") {
		t.Errorf("a domain's row does not say what it points at:\n%s", row)
	}
	if row := rowTo(t, a.find("maindb"), a.databasePath(maindb.ID)); !strings.Contains(row, "Database") || !strings.Contains(row, "postgres") {
		t.Errorf("the database's row:\n%s", row)
	}

	// A count is said for a reader who does not see the list, in the
	// region the page has, by its content alone.
	// (Part of an image's name, which nothing of the catalogue is called.)
	if body := a.find("nginx:alp"); !strings.Contains(body, `<p id="search-status" hx-swap-oob="innerHTML">2 results</p>`) {
		t.Errorf("no count of results:\n%s", body)
	}
	if body := a.find("maindb"); !strings.Contains(body, `hx-swap-oob="innerHTML">1 result</p>`) {
		t.Errorf("one result is not counted as one:\n%s", body)
	}

	// Stored variables are not searched: not their names, not their values.
	res, _ := a.post(a.appPath(web)+"/environment/edit", a.appPath(web)+"/environment", url.Values{"vars": {"PAYMENT_TOKEN=s3cr3tvalue"}})
	wantStatus(t, res, http.StatusSeeOther)
	for _, text := range []string{"PAYMENT_TOKEN", "s3cr3tvalue", "API_KEY", "k-123"} {
		if got := found(a.find(text)); len(got) != 0 {
			t.Errorf("%q finds %v", text, got)
		}
	}
}

// appID is the id of an environment's app of that name.
func (a *app) appID(env db.Environment, name string) string {
	a.t.Helper()
	apps, err := a.db.ListApps(context.Background(), env.ID)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, x := range apps {
		if x.Name == name {
			return x.ID
		}
	}
	a.t.Fatalf("no app %s", name)
	return ""
}

func TestSearchListsPages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	member := a.newPerson("Member", db.RoleMember)

	// Nothing typed: the pages a person can go to, and no count to say.
	for _, text := range []string{"", "   "} {
		body := a.find(text)
		if got := strings.Join(found(body), " "); got != "/ /projects /tags /servers /sources /team /keys /notifications /settings /account" {
			t.Errorf("%q: pages are %s", text, got)
		}
		if !strings.Contains(body, "Go to") || !strings.Contains(body, `<p id="search-status" hx-swap-oob="innerHTML"></p>`) {
			t.Errorf("%q: no heading, or something to announce:\n%s", text, body)
		}
	}
	// A Member is not offered what an Admin's role opens.
	_, body := member.get("/search?q=")
	if got := strings.Join(found(body), " "); got != "/ /projects /tags /servers /sources /team /keys /account" {
		t.Errorf("a Member's pages are %s", got)
	}
	for _, text := range []string{"settings", "notifications", "slack"} {
		if _, body = member.get("/search?q=" + text); len(found(body)) != 0 {
			t.Errorf("a Member finds %s:\n%s", text, body)
		}
	}

	// A page is found by its name and by the words it is known by.
	for text, href := range map[string]string{
		"serv":         "/servers",
		"settings":     "/settings",
		"KEYS":         "/keys",
		"ssh":          "/keys",
		"members":      "/team",
		"github":       "/sources",
		"password":     "/account",
		"keys tokens":  "/keys",
		"api tokens":   "/keys",
		"two-step":     "/account",
		"labels":       "/tags",
		"applications": "/projects",
		"slack":        "/notifications",
		"alerts":       "/notifications",
	} {
		got := found(a.find(text))
		if len(got) == 0 || got[0] != href {
			t.Errorf("%q leads to %v, want %s first", text, got, href)
		}
	}
	// With something typed a page's row says that it is one.
	if row := rowTo(t, a.find("serv"), "/servers"); !strings.Contains(row, "<mark>Serv</mark>ers") || !strings.Contains(row, "Page") {
		t.Errorf("the page's row:\n%s", row)
	}
	// The gallery is a page only where it is one.
	if got := found(a.find("components")); len(got) != 0 {
		t.Errorf("the gallery is offered outside development: %v", got)
	}
	dev := newApp(t, true)
	dev.setup()
	if got := found(dev.find("components")); len(got) != 1 || got[0] != "/_ui" {
		t.Errorf("the gallery in development: %v", got)
	}
}

func TestSearchAnswersWhateverIsTyped(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, _ := a.project("Shop")

	// Nothing found: said in the list, and to who does not see it.
	body := a.find("nosuchthing")
	if len(found(body)) != 0 || !strings.Contains(body, "Nothing matches “nosuchthing”.") || !strings.Contains(body, `hx-swap-oob="innerHTML">No results</p>`) {
		t.Errorf("nothing found:\n%s", body)
	}
	// What was typed comes back as text.
	body = a.find(`<script>alert(1)</script> "x" & y`)
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("the text is not escaped:\n%s", body)
	}
	// A wildcard is a character.
	if got := found(a.find("%")); len(got) != 0 {
		t.Errorf("%% finds %v", got)
	}
	// Text of any length gets an answer: cut, not refused.
	long := strings.Repeat("shop ", 400)
	if got := owned(found(a.find(long))); len(got) != 1 || got[0] != "/projects/"+projectID {
		t.Errorf("a long text finds %v", got)
	}
	if body = a.find(strings.Repeat("x", 5000)); strings.Contains(body, strings.Repeat("x", 101)) {
		t.Error("an over-long text comes back whole")
	}

	// More than a list holds: the first 20, and a line that says so.
	for i := 0; i < 25; i++ {
		if _, err := a.db.CreateProject(context.Background(), firstTeam(t, a), "bulk-"+strconv.Itoa(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	body = a.find("bulk")
	if n := len(found(body)); n != 20 || !strings.Contains(body, "Showing the first 20.") || !strings.Contains(body, `hx-swap-oob="innerHTML">The first 20 results</p>`) {
		t.Errorf("%d rows of 25:\n%s", n, body)
	}
	if body = a.find("bulk-1"); strings.Contains(body, "Showing the first") {
		t.Error("a list that holds everything says there is more")
	}

	// The answer is a part of a page, for the dialog to put in its list.
	if strings.Contains(body, "<html") || strings.Contains(body, "<dialog") {
		t.Error("the answer is a whole page")
	}
}

func TestSearchIsTheTeamsOwnAndASession(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", false, nil)

	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at, status) VALUES ('theirserver', 'otherteam', 'secret-server', 'ssh', 1, 'unreachable')`); err != nil {
		t.Fatal(err)
	}
	theirs, err := a.db.CreateProject(ctx, "otherteam", "Secret project", "")
	if err != nil {
		t.Fatal(err)
	}
	envs, _ := a.db.ListEnvironments(ctx, theirs.ID)
	their, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-web", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.AddDomain(ctx, "otherteam", "theirserver", db.Domain{ResourceKind: db.KindApp, ResourceID: their.ID, Host: "secret.example.com", TLS: true}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"secret", "web", "nginx", "example.com", their.ID, theirs.ID, "theirserver", envs[0].Name} {
		body := a.find(text)
		// What was typed is said back when nothing matches; that is not
		// a row of theirs.
		for _, secret := range []string{"Secret project", "secret-web", "secret-server", "secret.example.com", their.ID, theirs.ID} {
			if secret != text && strings.Contains(body, secret) {
				t.Errorf("%q shows %q of another team", text, secret)
			}
		}
		for _, href := range found(body) {
			if strings.Contains(href, their.ID) || strings.Contains(href, theirs.ID) || strings.Contains(href, "theirserver") {
				t.Errorf("%q leads to %s of another team", text, href)
			}
		}
	}
	if got := found(a.find(their.ID)); len(got) != 0 {
		t.Errorf("another team's id finds %v", got)
	}

	// Signed out, the search is the sign-in page: for a browser a
	// redirect, for the dialog's request a header that moves the page.
	out := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/search?q=web", nil)
	res, body := a.do(out, req)
	wantRedirect(t, res, "/login")
	if strings.Contains(body, "/apps/") {
		t.Error("a signed-out search answered")
	}
	req, _ = http.NewRequest(http.MethodGet, a.url+"/search?q=web", nil)
	req.Header.Set("HX-Request", "true")
	if res, _ = a.do(out, req); res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") != "/login" {
		t.Errorf("htmx signed out: %d HX-Redirect=%q", res.StatusCode, res.Header.Get("HX-Redirect"))
	}
}

func TestSearchWords(t *testing.T) {
	for text, want := range map[string]string{
		"":                       "",
		"   ":                    "",
		" Shop   web ":           "Shop|web",
		"a b c d e f g":          "a|b|c|d|e",
		"tab\tand\nline":         "tab|and|line",
		strings.Repeat("é", 150): strings.Repeat("é", 100),
	} {
		if got := strings.Join(searchWords(text), "|"); got != want {
			t.Errorf("searchWords(%q) = %q, want %q", text, got, want)
		}
	}
}

// The search also finds what the catalogue has to deploy, by its name: a
// few rows after the team's own, each marked as something to deploy and
// leading to the page that asks where it goes.
func TestSearchOffersWhatCanBeDeployed(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	mdb := a.newDatabase(projectID, env, "postgres", "maindb", nil)

	body := a.find("n8n")
	got := found(body)
	if len(got) == 0 || got[0] != "/deploy/service/n8n" {
		t.Fatalf("n8n leads to %v, want the template first", got)
	}
	row := rowTo(t, body, "/deploy/service/n8n")
	for _, want := range []string{"<mark>n8n</mark>", "Service template", `<span class="badge badge-brand">Deploy</span>`, `class="logo"`} {
		if !strings.Contains(row, want) {
			t.Errorf("the template's row lacks %q:\n%s", want, row)
		}
	}
	if strings.Contains(row, "pill") {
		t.Errorf("a template's row has a state, which only something that runs has:\n%s", row)
	}
	if !strings.Contains(body, "Deploy something new") {
		t.Error("what can be deployed is not under a heading of its own")
	}

	// What the team has comes first, however well a template's name fits:
	// the database found by its engine, then the engine itself.
	got = found(a.find("postgres"))
	if len(got) < 2 || got[0] != a.databasePath(mdb.ID) || got[1] != "/deploy/database/postgres" {
		t.Errorf("postgres leads to %v, want the team's database and then the engine", got)
	}
	// Never more than a few, of a catalogue of hundreds, and the best
	// fits of those: a name that is the text, then one that starts with it.
	offers := found(a.find("a"))
	if n := len(offers) - len(owned(offers)); n != searchOffers {
		t.Errorf("%d rows of the catalogue for one letter, want %d", n, searchOffers)
	}
	if got = found(a.find("redis")); len(got) == 0 || got[0] != "/deploy/database/redis" {
		t.Errorf("redis leads to %v, want the engine first", got)
	}
	// Only a name finds a template: not what it says of itself.
	if got = found(a.find("workflow")); slices.Contains(got, "/deploy/service/n8n") {
		t.Errorf("a word of n8n's description finds it: %v", got)
	}
	// With nothing typed the list is the pages, and nothing to deploy.
	if got = found(a.find("")); len(got) != len(owned(got)) {
		t.Errorf("the catalogue is listed before anything is typed: %v", got)
	}
	// Searched from inside an environment, the row carries that place as
	// the first choice of the page it leads to. It is a hint: an address
	// that is no environment's is not handed on.
	req, _ := http.NewRequest(http.MethodGet, a.url+"/search?q=n8n", nil)
	req.Header.Set("HX-Current-URL", a.url+"/projects/"+projectID+"/env/"+env.ID+"/app/abc")
	_, body = a.do(a.client, req)
	if got = found(body); len(got) == 0 || html.UnescapeString(got[0]) != "/deploy/service/n8n?env="+env.ID+"&project="+projectID {
		t.Errorf("searched from an environment, n8n leads to %v", got)
	}
	req.Header.Set("HX-Current-URL", "https://elsewhere.example/projects/../../x?<script>")
	if _, body = a.do(a.client, req); found(body)[0] != "/deploy/service/n8n" {
		t.Errorf("a made-up address is handed on: %v", found(body))
	}
	// A Member finds them too: adding to a project is a Member's to do.
	member := a.newPerson("Member", db.RoleMember)
	if _, body = member.get("/search?q=n8n"); !strings.Contains(body, `href="/deploy/service/n8n"`) {
		t.Error("a Member is not offered the template")
	}
}

// The page a found template leads to asks for the project and then the
// environment, and leads to the form that makes the thing there. It makes
// nothing itself, and goes nowhere that is not the team's.
func TestDeployAsksWhereItGoes(t *testing.T) {
	a := newApp(t, false)
	a.setup()

	// No project yet: the page says so and offers to make one.
	res, body := a.get("/deploy/service/n8n")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No projects yet") || !strings.Contains(body, `data-open="new-project"`) || strings.Contains(body, `name="go"`) {
		t.Error("with no project the page does not offer to make one, or asks to continue")
	}

	shop, shopEnv := a.project("Shop")
	blog, blogEnv := a.project("Blog")
	res, _ = a.post("/projects/"+blog, "/projects/"+blog+"/environments", url.Values{"name": {"staging"}})
	wantStatus(t, res, http.StatusSeeOther)
	blogEnvs, _ := a.db.ListEnvironments(context.Background(), blog)
	if len(blogEnvs) != 2 {
		t.Fatalf("Blog has %d environments", len(blogEnvs))
	}
	// Something deployed in Shop, after Blog was last touched, makes Shop
	// the likeliest answer.
	if _, err := a.db.Exec(`UPDATE projects SET created_at = created_at - 100 WHERE id = ?`, blog); err != nil {
		t.Fatal(err)
	}
	a.newApp(shop, shopEnv, "web", true, nil)

	res, body = a.get("/deploy/service/n8n")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"Deploy n8n", "Service template", `name="project" value="` + shop + `"`, `name="env" value="` + shopEnv.ID + `"`, `data-value="` + blog + `"`,
		`hx-get="/deploy/environments"`, `hx-trigger="change from:#deploy-project"`, `name="go" value="1"`, `action="/deploy/service/n8n"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The place it was found from is the first choice.
	_, body = a.get("/deploy/service/n8n?project=" + blog + "&env=" + blogEnvs[1].ID)
	if !strings.Contains(body, `name="project" value="`+blog+`"`) || !strings.Contains(body, `name="env" value="`+blogEnvs[1].ID+`"`) {
		t.Error("the project and environment in the address are not the ones chosen")
	}
	// Another project's environments are asked for when it is chosen.
	res, body = a.get("/deploy/environments?project=" + blog)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, `data-value="`+blogEnv.ID+`"`) || !strings.Contains(body, `data-value="`+blogEnvs[1].ID+`"`) || strings.Contains(body, shopEnv.ID) || strings.Contains(body, "<html") {
		t.Errorf("the environments of Blog: %s", body)
	}

	// Continue leads to the form its card on Add resource leads to.
	res, _ = a.get("/deploy/service/n8n?project=" + blog + "&env=" + blogEnvs[1].ID + "&go=1")
	wantRedirect(t, res, "/projects/"+blog+"/env/"+blogEnvs[1].ID+"/service/new?template=n8n")
	if res, body = a.get(res.Header.Get("Location")); res.StatusCode != http.StatusOK || !strings.Contains(body, "n8n") {
		t.Errorf("the form Continue leads to: %d", res.StatusCode)
	}
	res, _ = a.get("/deploy/database/postgres?project=" + shop + "&env=" + shopEnv.ID + "&go=1")
	wantRedirect(t, res, "/projects/"+shop+"/env/"+shopEnv.ID+"/database/new?engine=postgres")
	if res, _ = a.get(res.Header.Get("Location")); res.StatusCode != http.StatusOK {
		t.Errorf("the database form Continue leads to: %d", res.StatusCode)
	}

	// An environment of another project is not gone to, nor one that does
	// not exist: the page comes back and says so.
	for _, q := range []string{"project=" + shop + "&env=" + blogEnv.ID, "project=" + shop + "&env=nosuchenv123", "project=" + shop, "env=" + shopEnv.ID, "project=nosuchproj12&env=" + shopEnv.ID} {
		res, body = a.get("/deploy/service/n8n?go=1&" + q)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose a project and one of its environments.") {
			t.Errorf("%s: status %d, want the page again with what is wrong", q, res.StatusCode)
		}
	}
	// What is no template has no page: a made-up key, a kind that is
	// neither, and the two keys that are a form's and not the catalogue's.
	for _, path := range []string{"/deploy/service/nosuchtemplate", "/deploy/app/n8n", "/deploy/service/" + db.TemplateCustom, "/deploy/service/" + db.TemplateGit, "/deploy/database/nosuchengine"} {
		if res, _ = a.get(path); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, res.StatusCode)
		}
	}

	// Another team's project is none of this team's: not listed, not gone
	// to, and its environments are not told.
	ctx := context.Background()
	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	theirs, err := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	if err != nil {
		t.Fatal(err)
	}
	theirEnvs, _ := a.db.ListEnvironments(ctx, theirs.ID)
	if _, body = a.get("/deploy/service/n8n?project=" + theirs.ID + "&env=" + theirEnvs[0].ID); strings.Contains(body, theirs.ID) || strings.Contains(body, "Theirs") {
		t.Error("another team's project is on the page")
	}
	if res, _ = a.get("/deploy/service/n8n?go=1&project=" + theirs.ID + "&env=" + theirEnvs[0].ID); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("another team's project: status %d", res.StatusCode)
	}
	if _, body = a.get("/deploy/environments?project=" + theirs.ID); strings.Contains(body, theirEnvs[0].ID) || !strings.Contains(body, "The project is gone.") {
		t.Errorf("another team's environments: %s", body)
	}
	// Signed out, there is no page.
	if res, _ = a.do(a.newClient(), mustGet(a.url+"/deploy/service/n8n")); res.StatusCode != http.StatusSeeOther {
		t.Errorf("signed out: status %d", res.StatusCode)
	}
}
