package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// fakeGitHubRepos stands in for api.github.com for an App installed once,
// on the repositories given as the JSON of their list. Of acme/shop it also
// knows the branches and what is in a few folders.
func fakeGitHubRepos(t *testing.T, repos string) *source.GitHub {
	const shop = "/repos/acme/shop"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ref := r.URL.Query().Get("ref")
		switch r.URL.Path {
		case "/app/installations":
			io.WriteString(w, `[{"id":7}]`)
		case "/app/installations/7/access_tokens":
			io.WriteString(w, `{"token":"installation-token"}`)
		case "/installation/repositories":
			io.WriteString(w, `{"repositories":`+repos+`}`)
		case shop + "/installation":
			io.WriteString(w, `{"id":7}`)
		case shop + "/branches":
			// The last name is GitHub's to choose, and is not a branch.
			io.WriteString(w, `[{"name":"main"},{"name":"develop"},{"name":"--upload-pack=<b>"}]`)
		case shop + "/contents":
			if ref == "develop" {
				io.WriteString(w, `[{"name":"package.json","type":"file"},{"name":"<b>.md","type":"file"}]`)
				return
			}
			io.WriteString(w, `[{"name":"Dockerfile","type":"file"},{"name":"package.json","type":"file"},{"name":"site","type":"dir"}]`)
		case shop + "/contents/site":
			io.WriteString(w, `[{"name":"index.html","type":"file"},{"name":"style.css","type":"file"}]`)
		case shop + "/contents/docs":
			io.WriteString(w, `[{"name":"README.md","type":"file"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &source.GitHub{APIBase: srv.URL, HTTP: srv.Client()}
}

// githubSource gives a team a GitHub App whose key signs.
func (a *app) githubSource(team, name string) db.GitSource {
	a.t.Helper()
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		a.t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	src, err := a.db.StartGitSource(ctx, team, name, name+"-state")
	if err != nil {
		a.t.Fatal(err)
	}
	src.AppID, src.Slug, src.PrivateKey = 9, name, a.seal(string(keyPEM))
	if err := a.db.FinishGitSource(ctx, src); err != nil {
		a.t.Fatal(err)
	}
	return src
}

// wantAll fails unless text holds every one of the pieces.
func wantAll(t *testing.T, what, text string, pieces ...string) {
	t.Helper()
	for _, want := range pieces {
		if !strings.Contains(text, want) {
			t.Fatalf("%s lacks %q:\n%s", what, want, text)
		}
	}
}

// Through a source, the repository is one control: a menu that asks for the
// source's repositories only when it is opened and holds the one chosen.
// There is no field to type an address into beside it. A repository's name
// is GitHub's to choose, so it reaches the page as text.
func TestRepositoryPicker(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[
		{"full_name":"acme/shop","default_branch":"main","private":true},
		{"full_name":"acme/My_Docs.site","default_branch":"trunk","private":false},
		{"full_name":"acme/<b>docs","default_branch":"","private":false}]`)
	a.setup()
	src := a.githubSource(firstTeam(t, a), "acme")

	projectID, env := a.project("Shop")
	form := "/projects/" + projectID + "/env/" + env.ID + "/app/new?source=git"
	repos := "/sources/github/" + src.ID + "/repos"
	picker := `<fieldset class="grid min-w-0 gap-4" data-when="access" data-is="source:` + src.ID + `"`
	typed := `<fieldset class="grid min-w-0 gap-4" data-when="access" data-is="public"`

	// Asked for with the App, the form shows its pickers, and the fields to
	// type into are there but neither shown nor sent.
	_, page := a.get(form + "&access=app")
	wantAll(t, "the form", page,
		`hx-get="`+repos+`"`, `hx-trigger="select-load"`, "Choose a repository", "Find a repository",
		`id="repo-`+src.ID+`-value" name="repo" value=""`,
		`id="branch-`+src.ID+`-value" name="branch" value="main"`,
		`hx-get="/sources/github/`+src.ID+`/branches"`, `hx-include="#repo-`+src.ID+`-value"`, "Find a branch",
		picker+`>`, typed+` hidden disabled>`,
		// The folder is looked at when where the code is changes.
		`hx-get="/sources/detect"`, `hx-include="#git-where"`, `id="build-detected"`,
	)
	for _, gone := range []string{"Choose from", "data-fill", "<select"} {
		if strings.Contains(page, gone) {
			t.Fatalf("the form still has %q", gone)
		}
	}
	if strings.Contains(page, `data-value="https://github.com/`) {
		t.Fatal("the repositories were fetched before the menu was opened")
	}
	// With nothing asked for the form starts at a public repository, which
	// is typed.
	_, page = a.get(form)
	wantAll(t, "the form for a public repository", page, picker+` hidden disabled>`, typed+`>`, `id="repo"`, `id="branch"`)

	res, list := a.get(repos)
	wantStatus(t, res, http.StatusOK)
	wantAll(t, "the list", list,
		`role="option"`, `data-value="https://github.com/acme/shop"`, `data-label="acme/shop"`, `data-search="acme/shop"`,
		// Choosing one sets the branch beside it and offers a name.
		`data-sets="branch-`+src.ID+`"`, `data-sets-value="trunk"`, `data-suggest="shop"`, `data-suggest="my-docs-site"`,
		"Private", "acme/&lt;b&gt;docs",
		// One the list does not have is named by typing it.
		`data-typed data-prefix="https://github.com/" data-needs="/"`,
	)
	if strings.Contains(list, "<b>docs") {
		t.Fatalf("a repository's name was written as markup:\n%s", list)
	}
	if strings.Contains(list, "<html") {
		t.Fatalf("the list is a whole page, not the options:\n%s", list)
	}

	// What GitHub answers when it cannot be asked is said in the menu, and
	// a repository can still be named.
	a.server.GitHub = fakeGitHubRepos(t, `"not a list"`)
	res, list = a.get(repos)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(list, `role="alert"`) || strings.Contains(list, `data-value=`) || !strings.Contains(list, "data-typed") {
		t.Fatalf("a failed listing is not reported:\n%s", list)
	}
}

// The name offered for an app is one an app may have.
func TestSuggestedAppName(t *testing.T) {
	for full, want := range map[string]string{
		"acme/shop":                        "shop",
		"group/sub/My_Docs.site":           "my-docs-site",
		"acme/--x--":                       "x",
		"acme/...":                         "",
		"acme/" + strings.Repeat("a-", 30): strings.TrimSuffix(strings.Repeat("a-", 16), "-"),
	} {
		got := pages.SuggestName(full)
		if got != want {
			t.Errorf("SuggestName(%q) = %q, want %q", full, got, want)
		}
		if got != "" && !envNameRE.MatchString(got) {
			t.Errorf("SuggestName(%q) = %q, which no app may be called", full, got)
		}
	}
}

// The branch is chosen from the repository's own, asked for when the menu
// is opened, with the repository the form holds.
func TestBranchPicker(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[]`)
	a.setup()
	src := a.githubSource(firstTeam(t, a), "acme")
	branches := "/sources/github/" + src.ID + "/branches"

	res, list := a.get(branches + "?repo=" + url.QueryEscape("https://github.com/acme/shop"))
	wantStatus(t, res, http.StatusOK)
	wantAll(t, "the list", list, `data-value="main"`, `data-value="develop"`, `data-label="develop"`, `data-typed data-prefix=""`)
	if strings.Contains(list, "upload-pack") || strings.Contains(list, "<html") {
		t.Fatalf("the list holds what is not a branch, or is a page:\n%s", list)
	}

	// No repository yet, one on another host, and one the App is not on.
	_, list = a.get(branches)
	if !strings.Contains(list, "Choose a repository first.") || strings.Contains(list, `role="option"`) {
		t.Fatalf("without a repository:\n%s", list)
	}
	for repo, want := range map[string]string{
		"https://gitlab.com/acme/shop":  "can only read repositories on github.com",
		"https://github.com/acme/other": "not installed on this repository",
		"ext::sh -c id":                 "can only read repositories on github.com",
	} {
		res, list = a.get(branches + "?repo=" + url.QueryEscape(repo))
		wantStatus(t, res, http.StatusOK)
		if !strings.Contains(list, `role="alert"`) || !strings.Contains(list, want) || strings.Contains(list, `data-value=`) {
			t.Errorf("the branches of %s:\n%s", repo, list)
		}
	}

	// A GitHub App is not a GitLab source, and another team's is not found.
	res, _ = a.get("/sources/gitlab/" + src.ID + "/branches")
	wantStatus(t, res, http.StatusNotFound)
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	theirs := a.githubSource("otherteam", "theirs")
	res, _ = a.get("/sources/github/" + theirs.ID + "/branches?repo=" + url.QueryEscape("https://github.com/acme/shop"))
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.get("/sources/detect?access=source:" + theirs.ID + "&branch=main&repo=" + url.QueryEscape("https://github.com/acme/shop"))
	wantStatus(t, res, http.StatusNotFound)
}

// Build with is worked out from the names of the files in the folder the
// app is built from, and the answer says which file decided.
func TestBuildIsWorkedOut(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[]`)
	a.setup()
	src := a.githubSource(firstTeam(t, a), "acme")
	detect := func(access, repo, branch, dir string) string {
		t.Helper()
		res, body := a.get("/sources/detect?" + url.Values{"access": {access}, "repo": {repo}, "branch": {branch}, "base_dir": {dir}}.Encode())
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(body, "<html") {
			t.Fatalf("the answer is a whole page:\n%s", body)
		}
		return body
	}
	const shop = "https://github.com/acme/shop"
	through := "source:" + src.ID

	for _, c := range []struct{ branch, dir, pack, found string }{
		{"main", "", "dockerfile", "Dockerfile"},
		{"develop", "", "railpack", "package.json"},
		{"main", "site", "static", "index.html"},
		{"main", "/site/", "static", "index.html"},
	} {
		wantAll(t, "the answer for "+c.branch+" "+c.dir, detect(through, shop, c.branch, c.dir),
			`data-pick="build_pack"`, `data-value="`+c.pack+`"`, "Found", ">"+c.found+"<")
	}
	// A folder that says nothing, and one that is not there: said, and the
	// menu is left alone.
	for dir, want := range map[string]string{"docs": "Nothing in this folder says how it is built", "nowhere": "no such branch or folder"} {
		if got := detect(through, shop, "main", dir); !strings.Contains(got, want) || strings.Contains(got, "data-pick") {
			t.Errorf("the answer for %s:\n%s", dir, got)
		}
	}
	// Nobody to ask, and what the form itself refuses: nothing is said.
	for _, c := range [][4]string{
		{"public", shop, "main", ""},
		{"key:abc", "git@github.com:acme/shop.git", "main", ""},
		{through, "https://gitlab.com/acme/shop", "main", ""},
		{through, "", "main", ""},
		{through, shop, "--upload-pack=x", ""},
		{through, shop, "main", "../etc"},
	} {
		if got := detect(c[0], c[1], c[2], c[3]); strings.TrimSpace(got) != "" {
			t.Errorf("detect(%v) said:\n%s", c, got)
		}
	}
}

// An app is made from what the pickers send, and the form says when no
// repository was chosen.
func TestCreateAppThroughThePickers(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[]`)
	a.setup()
	src := a.githubSource(firstTeam(t, a), "acme")
	projectID, env := a.project("Shop")

	created := a.newGitApp(projectID, env, "shop", url.Values{
		"access": {"source:" + src.ID}, "repo": {"https://github.com/acme/shop"}, "branch": {"develop"}, "build_pack": {"nixpacks"},
	})
	if created.GitSourceID != src.ID || created.RepoName != "acme/shop" || created.Branch != "develop" || created.BuildPack != "nixpacks" {
		t.Fatalf("the app: %+v", created)
	}

	form := gitForm(env, "other")
	form.Set("access", "source:"+src.ID)
	form.Set("repo", "")
	page := "/projects/" + projectID + "/env/" + env.ID + "/app/new?source=git"
	res, body := a.post(page, "/projects/"+projectID+"/env/"+env.ID+"/app", form)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	// The form comes back with the App's pickers shown and the error under
	// the repository.
	wantAll(t, "the refused form", body, "Choose a repository.", `id="repo-`+src.ID+`-error"`,
		`data-is="source:`+src.ID+`">`, `data-is="public" hidden disabled>`)

	// The app's Source settings show what is stored in the pickers.
	_, settings := a.get(a.appPath(created.ID) + "/settings")
	wantAll(t, "the Source settings", settings,
		`id="repo-`+src.ID+`-value" name="repo" value="https://github.com/acme/shop"`, ">acme/shop<",
		`id="branch-`+src.ID+`-value" name="branch" value="develop"`)
}
