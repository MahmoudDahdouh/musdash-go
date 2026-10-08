package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// Domains is a tab of an app and of nothing else. It lists that app's
// addresses, adds one and removes one, and comes back to itself.
func TestAppDomainsTab(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", false, url.Values{"domain": {"shop.example.com"}})
	api := a.newApp(projectID, env, "api", false, url.Values{"domain": {"api.example.com"}})
	tab := a.appPath(web) + "/domains"

	res, page := a.get(tab)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"shop.example.com", `id="add-domain"`, `href="` + tab + `" aria-current="page"`, "data-confirm="} {
		if !strings.Contains(page, want) {
			t.Errorf("the Domains tab is missing %q", want)
		}
	}
	if strings.Contains(page, "api.example.com") {
		t.Fatal("the Domains tab lists another app's domain")
	}
	if strings.Contains(page, "data-autoopen") {
		t.Fatal("the Add domain dialog is open on a page nobody sent a form to")
	}
	// The dialog: the scheme as a menu with HTTPS chosen, the domain, the
	// app's own port, the path, and a button that makes an address for the
	// app's server.
	dialog := between(t, tab, page, `id="add-domain"`, `</dialog>`)
	for _, want := range []string{
		`name="scheme" value="https"`, `data-value="https"`, `data-value="http"`,
		`id="host" name="host"`, `id="port" name="port" type="text" value="80"`, `inputmode="numeric"`,
		`id="path" name="path"`, `name="redirect_www"`, `name="strip_prefix"`, `name="auth_user"`, `name="auth_password" type="password"`,
		`data-generate="host"`, `data-suffix=".127.0.0.1.sslip.io"`, `data-scheme="scheme"`,
	} {
		if !strings.Contains(dialog, want) {
			t.Errorf("the Add domain dialog is missing %s", want)
		}
	}
	// The menu names the scheme and nothing more.
	if strings.Contains(dialog, "://") {
		t.Error("the scheme's menu still writes ://")
	}
	if strings.Contains(dialog, `name="tls"`) {
		t.Error("the dialog still has the HTTPS box beside the scheme")
	}
	// In the order asked for: scheme, domain, port, then the path.
	last := -1
	for _, id := range []string{`id="scheme"`, `id="host"`, `id="port"`, `id="path"`, `id="redirect_www"`, `id="strip_prefix"`, `id="auth_user"`} {
		at := strings.Index(dialog, id)
		if at <= last {
			t.Errorf("%s is missing or out of place in the dialog", id)
		}
		last = at
	}

	// A refused form comes back on the tab, with the dialog open.
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"host": {"not a host"}}, "Enter a domain"},
		{url.Values{"host": {"api.example.com"}}, "already"},
		{url.Values{"host": {"new.example.com"}, "path": {"/a b"}}, "Enter a path such as /api"},
	} {
		res, body := a.post(tab, tab, c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) || !strings.Contains(body, "data-autoopen") {
			t.Errorf("%v: status %d, want 422 with %q in the open dialog", c.form, res.StatusCode, c.want)
		}
		if !strings.Contains(body, `href="`+tab+`" aria-current="page"`) {
			t.Errorf("%v: the refused form did not come back on the Domains tab", c.form)
		}
	}

	// Adding and removing both come back to the tab.
	res, _ = a.post(tab, tab, url.Values{"host": {"new.example.com"}, "path": {"/v1"}, "scheme": {"https"}})
	wantRedirect(t, res, tab)
	doms, _ := a.db.ListDomains(ctx, db.KindApp, web)
	if len(doms) != 2 || doms[1].Host != "new.example.com" || doms[1].Path != "/v1" || !doms[1].TLS {
		t.Fatalf("domains after adding through the tab: %+v", doms)
	}
	if _, page = a.get(tab); !strings.Contains(page, "new.example.com/v1") {
		t.Fatal("the tab does not list the domain that was added")
	}
	res, _ = a.post(tab, tab+"/"+doms[1].ID+"/delete", nil)
	wantRedirect(t, res, tab)
	if doms, _ := a.db.ListDomains(ctx, db.KindApp, web); len(doms) != 1 {
		t.Fatalf("the domain was not removed: %+v", doms)
	}
	// One app cannot remove another's domain through its own tab.
	theirs, _ := a.db.ListDomains(ctx, db.KindApp, api)
	res, _ = a.post(tab, tab+"/"+theirs[0].ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	if left, _ := a.db.ListDomains(ctx, db.KindApp, api); len(left) != 1 {
		t.Fatal("an app's domain was removed through another app")
	}

	// Settings no longer holds the list or the form, and Overview leads
	// to the tab.
	if _, page = a.get(a.appPath(web) + "/settings"); strings.Contains(page, `id="add-domain"`) || strings.Contains(page, `action="`+tab+`"`) {
		t.Error("the app's Settings still hold the Add domain form")
	}
	if _, page = a.get(a.appPath(web)); !strings.Contains(page, `href="`+tab+`"`) || strings.Contains(page, "settings#domains") {
		t.Error("the app's Overview does not lead to the Domains tab")
	}

	// An app with none says so, with the button that adds one.
	bare := a.newApp(projectID, env, "bare", false, url.Values{"domain": {""}})
	if _, page = a.get(a.appPath(bare) + "/domains"); !strings.Contains(page, "No domain yet") || !strings.Contains(page, `data-open="add-domain"`) {
		t.Error("an app without a domain: no empty state, or no button in it")
	}

	// A preview's address is given to it: it has no tab, and is turned
	// away from the page.
	a.db.Exec(`UPDATE apps SET source = 'git' WHERE id = ?`, web)
	parent, _ := a.db.AppByID(ctx, web)
	preview, err := a.db.CreatePreview(ctx, parent, 7, "web-pr-7", "feature")
	if err != nil {
		t.Fatal(err)
	}
	res, _ = a.get(a.appPath(preview.ID) + "/domains")
	wantRedirect(t, res, a.appPath(preview.ID))
	if _, page = a.get(a.appPath(preview.ID)); strings.Contains(page, a.appPath(preview.ID)+"/domains") {
		t.Error("a preview has a Domains tab")
	}

	// A project has no Domains tab, nor a database, nor a service.
	res, _ = a.get("/projects/" + projectID + "/domains")
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post(tab, "/projects/"+projectID+"/domains", url.Values{"app": {web}, "host": {"x.example.com"}})
	wantStatus(t, res, http.StatusNotFound)
	if _, page = a.get("/projects/" + projectID); strings.Contains(page, "/projects/"+projectID+"/domains") {
		t.Error("the project still has a Domains tab")
	}
	mdb := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	res, _ = a.get(a.databasePath(mdb.ID) + "/domains")
	wantStatus(t, res, http.StatusNotFound)

	// Another team's app has no such tab here.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`UPDATE projects SET team_id = 'otherteam' WHERE id = ?`, projectID)
	res, _ = a.get(tab)
	wantStatus(t, res, http.StatusNotFound)
}
