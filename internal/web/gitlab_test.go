package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// Not written as GitLab writes its tokens ("glpat-" and twenty characters):
// a test value in that shape is taken for a leaked one by secret scanners.
const gitlabToken = "test.T0PSECRET.good"

// fakeGitLab is a GitLab instance that knows one token and two projects.
// It returns the client that trusts it and its address.
func fakeGitLab(t *testing.T) (*source.GitLab, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+gitlabToken {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"401 Unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v4/user":
			io.WriteString(w, `{"username":"ada"}`)
		case "/api/v4/projects":
			io.WriteString(w, `[
				{"path_with_namespace":"acme/shop","default_branch":"main","visibility":"private"},
				{"path_with_namespace":"acme/<b>docs","default_branch":"main","visibility":"private"},
				{"path_with_namespace":"acme/platform/site","default_branch":"trunk","visibility":"public"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &source.GitLab{HTTP: srv.Client()}, srv.URL
}

// The Sources page connects GitLab beside GitHub: one menu, a dialog for
// each, one table. A GitLab source is an address and a token, which GitLab
// is asked about before it is stored and which never comes back.
func TestGitLabSource(t *testing.T) {
	a := newApp(t, false)
	lab, base := fakeGitLab(t)
	a.server.GitLab = lab
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	host := source.GitLabHost(base)

	// With no source, the Add source menu is in the empty state: GitHub
	// and GitLab, each opening its own dialog.
	res, page := a.get("/sources")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"No source yet", `id="new-source"`, `data-open="new-github-app"`, `data-open="new-gitlab"`, `id="new-github-app"`, `id="new-gitlab"`, `action="/sources/gitlab"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the empty Sources page lacks %q", want)
		}
	}
	if strings.Count(page, `id="new-source"`) != 1 || strings.Contains(page, "data-autoopen") || strings.Contains(page, "<table") {
		t.Fatal("the empty page: not one menu, a dialog open, or a table")
	}
	if empty := between(t, "/sources", page, `<div class="empty"`, `<dialog`); !strings.Contains(empty, `id="new-source"`) {
		t.Fatal("the Add source menu is not in the empty state")
	}

	// What is wrong with the form is said in its own dialog, and the token
	// is not sent back.
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"gitlab_name": {""}, "gitlab_base": {base}, "gitlab_token": {gitlabToken}}, "Enter a name"},
		{url.Values{"gitlab_name": {"work"}, "gitlab_base": {"http://gitlab.example.com"}, "gitlab_token": {gitlabToken}}, "It must be https"},
		{url.Values{"gitlab_name": {"work"}, "gitlab_base": {base + "/group"}, "gitlab_token": {gitlabToken}}, "nothing after the host"},
		{url.Values{"gitlab_name": {"work"}, "gitlab_base": {base}, "gitlab_token": {"not a token"}}, "Enter the access token"},
		// Written as a token, but GitLab does not know it.
		{url.Values{"gitlab_name": {"work"}, "gitlab_base": {base}, "gitlab_token": {"test.T0PSECRET.wrong"}}, "did not accept the token"},
	} {
		res, body := a.post("/sources", "/sources/gitlab", c.form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v: status %d, want 422 with %q", c.form, res.StatusCode, c.want)
		}
		if strings.Count(body, "data-autoopen") != 1 || !regexp.MustCompile(`<dialog[^>]*id="new-gitlab"[^>]*data-autoopen`).MatchString(body) {
			t.Errorf("%v: the refused form is not the one dialog that is open", c.form)
		}
		if strings.Contains(body, "T0PSECRET") {
			t.Errorf("%v: the token came back in the page", c.form)
		}
	}
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 0 {
		t.Fatalf("a refused form made a source: %+v", list)
	}

	res, _ = a.post("/sources", "/sources/gitlab", url.Values{"gitlab_name": {"work"}, "gitlab_base": {base + "/"}, "gitlab_token": {" " + gitlabToken + " "}})
	wantRedirect(t, res, "/sources")
	list, _ := a.db.ListGitSources(ctx, team)
	if len(list) != 1 {
		t.Fatalf("sources: %+v", list)
	}
	src := list[0]
	if src.Kind != db.GitSourceGitLab || src.Name != "work" || src.BaseURL != base || src.Slug != "ada" || src.Token == "" || strings.Contains(src.Token, "T0PSECRET") {
		t.Fatalf("stored: %+v", src)
	}
	if plain, err := a.server.Box.OpenString(src.Token); err != nil || plain != gitlabToken {
		t.Fatalf("the sealed token opens to %q, %v", plain, err)
	}

	// The source is a row of the table, and the menu moved to the header.
	_, page = a.get("/sources")
	row := between(t, "/sources", page, "<tbody", "</tbody>")
	for _, want := range []string{">work<", ">GitLab<", "ada on " + host, `action="/sources/gitlab/` + src.ID + `/delete"`, "data-confirm="} {
		if !strings.Contains(row, want) {
			t.Errorf("the source's row lacks %q", want)
		}
	}
	if strings.Contains(row, "Choose repositories") {
		t.Error("a GitLab source offers GitHub's Choose repositories")
	}
	if strings.Contains(page, "T0PSECRET") || strings.Contains(page, "No source yet") || strings.Count(page, `id="new-source"`) != 1 {
		t.Fatal("the page shows the token, still the empty state, or not one Add source menu")
	}
	if head := between(t, "/sources", page, `<header`, `</header>`); !strings.Contains(head, `id="new-source"`) || !strings.Contains(head, "data-menu-end") {
		t.Fatal("the Add source menu is not at the end of the header")
	}

	// Its repository picker lists what the token reads, under the
	// instance's own address. A name that is not a path is left out.
	res, menu := a.get("/sources/gitlab/" + src.ID + "/repos")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(menu, `data-value="`+base+`/acme/shop"`) || !strings.Contains(menu, `data-value="`+base+`/acme/platform/site"`) || !strings.Contains(menu, "trunk") || strings.Contains(menu, "docs") {
		t.Fatalf("the picker: %s", menu)
	}
	if strings.Contains(menu, "T0PSECRET") {
		t.Fatal("the picker shows the token")
	}
	// Each kind's list answers for its own kind only.
	res, _ = a.get("/sources/github/" + src.ID + "/repos")
	wantStatus(t, res, http.StatusNotFound)

	// The Git form offers it by its kind, with its picker, and Add
	// resource has a tile that leads there.
	projectID, env := a.project("Shop")
	newApp := "/projects/" + projectID + "/env/" + env.ID + "/app/new?source=git&access=gitlab"
	_, page = a.get(newApp)
	if !strings.Contains(page, "GitLab: work") || !strings.Contains(page, `hx-get="/sources/gitlab/`+src.ID+`/repos"`) {
		t.Fatal("the Git form does not offer the GitLab source, or not its picker")
	}
	if !regexp.MustCompile(`<input type="hidden"[^>]*name="access"[^>]*value="source:` + src.ID + `"`).MatchString(page) {
		t.Fatal("the Git form reached from the GitLab tile does not start at the GitLab source")
	}
	if _, page = a.get("/projects/" + projectID + "/env/" + env.ID + "/new"); !strings.Contains(page, `href="/projects/`+projectID+`/env/`+env.ID+`/app/new?source=git&amp;access=gitlab"`) {
		t.Fatal("Add resource has no tile for a private repository through GitLab")
	}

	// It reads repositories on its own instance and nowhere else.
	form := gitForm(env, "shop")
	form.Set("access", "source:"+src.ID)
	res, body := a.post(newApp, "/projects/"+projectID+"/env/"+env.ID+"/app", form) // the form's repository is on github.com
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "GitLab work can only read repositories on "+host) {
		t.Fatalf("a repository on another host: %d", res.StatusCode)
	}
	shop := a.newGitApp(projectID, env, "shop", url.Values{"access": {"source:" + src.ID}, "repo": {base + "/acme/shop"}})
	if shop.GitSourceID != src.ID || shop.RepoName != "acme/shop" {
		t.Fatalf("the app: %+v", shop)
	}

	// GitLab reports no pushes through a token, so the app is offered a
	// webhook secret of its own, where one through a GitHub App is not.
	_, page = a.get("/keys/tokens")
	if !regexp.MustCompile(`(?s)id="new-hook-secret".*?` + shop.ID).MatchString(page) {
		t.Fatal("an app through a GitLab source is not offered a webhook secret")
	}
	res, _ = a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {shop.ID}})
	wantRedirect(t, res, "/keys/tokens")
	if _, page = a.get("/keys/tokens"); strings.Contains(page, "a GitHub App reports the pushes") {
		t.Fatal("the Keys page says a GitHub App reports the pushes of an app that has none")
	}

	// A source that something deploys through stays; one that nothing
	// uses goes.
	res, _ = a.post("/sources", "/sources/gitlab/"+src.ID+"/delete", nil)
	wantRedirect(t, res, "/sources")
	if _, page = a.get("/sources"); !strings.Contains(page, "still deploys through work") {
		t.Fatal("removing a source in use was not refused with a reason")
	}
	spare, err := a.db.CreateGitLabSource(ctx, team, "spare", base, "", a.seal(gitlabToken))
	if err != nil {
		t.Fatal(err)
	}
	res, _ = a.post("/sources", "/sources/gitlab/"+spare.ID+"/delete", nil)
	wantRedirect(t, res, "/sources")
	if list, _ = a.db.ListGitSources(ctx, team); len(list) != 1 || list[0].ID != src.ID {
		t.Fatalf("after removing: %+v", list)
	}

	// A Member reads the table and nothing more.
	member := a.newPerson("Member", db.RoleMember)
	_, page = member.get("/sources")
	if !strings.Contains(page, ">work<") || strings.Contains(page, `id="new-source"`) || strings.Contains(page, `id="new-gitlab"`) || strings.Contains(page, "/delete") {
		t.Fatal("a Member's Sources page: no table, or something to change")
	}
	res, _ = member.post("/sources/gitlab", url.Values{"gitlab_name": {"mine"}, "gitlab_base": {base}, "gitlab_token": {gitlabToken}})
	wantStatus(t, res, http.StatusForbidden)
	// The picker is a Member's too: they make apps.
	res, _ = member.get("/sources/gitlab/" + src.ID + "/repos")
	wantStatus(t, res, http.StatusOK)

	// Another team's source is not found.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	theirs, _ := a.db.CreateGitLabSource(ctx, "otherteam", "theirs", base, "eve", a.seal(gitlabToken))
	res, _ = a.get("/sources/gitlab/" + theirs.ID + "/repos")
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post("/sources", "/sources/gitlab/"+theirs.ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	if _, page = a.get("/sources"); strings.Contains(page, "theirs") {
		t.Fatal("another team's source is listed")
	}
}
