package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// A project's Domains tab lists the addresses of what the project runs,
// in every environment, and adds one to an app of the project only.
func TestProjectDomainsTab(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, production := a.project("Shop")
	staging, err := a.db.CreateEnvironment(ctx, team, projectID, "staging")
	if err != nil {
		t.Fatal(err)
	}
	web := a.newApp(projectID, production, "web", false, url.Values{"domain": {"shop.example.com"}})
	next := a.newApp(projectID, staging, "web-next", false, url.Values{"domain": {"next.example.com"}})
	otherID, otherEnv := a.project("Blog")
	blog := a.newApp(otherID, otherEnv, "blog", false, url.Values{"domain": {"blog.example.com"}})
	tab := "/projects/" + projectID + "/domains"

	res, page := a.get(tab)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"shop.example.com", "next.example.com", ">staging<", `id="add-domain"`, "/apps/" + web + "/settings#domains"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Domains tab is missing %q", want)
		}
	}
	if strings.Contains(page, "blog.example.com") || strings.Contains(page, blog) {
		t.Fatal("the Domains tab lists another project's domain or app")
	}

	// Adding through the tab runs the app's own checks.
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"app": {web}, "host": {"not a host"}}, "Enter a domain"},
		{url.Values{"app": {web}, "host": {"shop.example.com"}}, "already"},
		{url.Values{"app": {web}, "host": {"api.example.com"}, "path": {"/a b"}}, "Enter a path such as /api"},
		// An app of another project, and one that does not exist.
		{url.Values{"app": {blog}, "host": {"api.example.com"}}, "Choose an app of this project."},
		{url.Values{"app": {"nosuchapp"}, "host": {"api.example.com"}}, "Choose an app of this project."},
	} {
		res, body := a.post(tab, tab, c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) || !strings.Contains(body, "data-autoopen") {
			t.Errorf("%v: status %d, want 422 with %q in the open dialog", c.form, res.StatusCode, c.want)
		}
	}
	if doms, _ := a.db.ListDomains(ctx, db.KindApp, blog); len(doms) != 1 {
		t.Fatalf("another project's app got a domain through this project: %+v", doms)
	}
	res, _ = a.post(tab, tab, url.Values{"app": {next}, "host": {"api.example.com"}, "path": {"/v1"}, "tls": {"1"}})
	wantRedirect(t, res, tab)
	doms, _ := a.db.ListDomains(ctx, db.KindApp, next)
	if len(doms) != 2 || doms[1].Host != "api.example.com" || doms[1].Path != "/v1" || !doms[1].TLS {
		t.Fatalf("domains after adding through the tab: %+v", doms)
	}

	// Removing from the tab comes back to the tab.
	res, _ = a.post(tab, "/apps/"+next+"/domains/"+doms[1].ID+"/delete", url.Values{"from": {"project"}})
	wantRedirect(t, res, tab)
	if doms, _ := a.db.ListDomains(ctx, db.KindApp, next); len(doms) != 1 {
		t.Fatalf("the domain was not removed: %+v", doms)
	}

	// A preview cannot be given a domain through its parent's project.
	a.db.Exec(`UPDATE apps SET source = 'git' WHERE id = ?`, web)
	parent, _ := a.db.AppByID(ctx, web)
	preview, err := a.db.CreatePreview(ctx, parent, 7, "web-pr-7", "feature")
	if err != nil {
		t.Fatal(err)
	}
	res, body := a.post(tab, tab, url.Values{"app": {preview.ID}, "host": {"pr.example.com"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose an app of this project.") {
		t.Fatalf("a preview through the project's tab: %d", res.StatusCode)
	}
	if _, page = a.get(tab); strings.Contains(page, "web-pr-7") {
		t.Fatal("a preview is offered or listed on the Domains tab")
	}

	// Another team's project has no such tab here.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	theirs, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	res, _ = a.get("/projects/" + theirs.ID + "/domains")
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post(tab, "/projects/"+theirs.ID+"/domains", url.Values{"app": {web}, "host": {"x.example.com"}})
	wantStatus(t, res, http.StatusNotFound)
}
