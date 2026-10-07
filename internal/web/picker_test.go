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
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// fakeGitHubRepos stands in for api.github.com for an App installed once,
// on the repositories given as the JSON of their list.
func fakeGitHubRepos(t *testing.T, repos string) *source.GitHub {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations":
			io.WriteString(w, `[{"id":7}]`)
		case "/app/installations/7/access_tokens":
			io.WriteString(w, `{"token":"installation-token"}`)
		case "/installation/repositories":
			io.WriteString(w, `{"repositories":`+repos+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &source.GitHub{APIBase: srv.URL, HTTP: srv.Client()}
}

// A form offers a GitHub App's repositories in a menu that asks for them
// only when it is opened, and each one it is given fills the repository
// field. A repository's name is GitHub's to choose, so it reaches the page
// as text.
func TestRepositoryPicker(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[
		{"full_name":"acme/shop","default_branch":"main","private":true},
		{"full_name":"acme/<b>docs","default_branch":"trunk","private":false}]`)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	src, err := a.db.StartGitSource(ctx, team, "acme", "state")
	if err != nil {
		t.Fatal(err)
	}
	src.AppID, src.Slug, src.PrivateKey = 9, "acme", a.seal(string(keyPEM))
	if err := a.db.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	projectID, env := a.project("Shop")
	_, page := a.get("/projects/" + projectID + "/env/" + env.ID + "/app/new?source=git")
	repos := "/sources/github/" + src.ID + "/repos"
	for _, want := range []string{
		`hx-get="` + repos + `"`, `hx-trigger="select-load"`, "Choose from acme", "Find a repository",
		// The repository can still be typed: a public one, or an SSH address.
		`id="repo"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the form lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `data-fill="repo"`) {
		t.Fatal("the repositories were fetched before the menu was opened")
	}
	if strings.Contains(page, "<select") {
		t.Fatal("the form has a browser select")
	}

	res, list := a.get(repos)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{
		`role="option"`, `data-fill="repo"`,
		`data-value="https://github.com/acme/shop"`, `data-search="acme/shop"`,
		"Private", "trunk", "acme/&lt;b&gt;docs",
	} {
		if !strings.Contains(list, want) {
			t.Fatalf("the list lacks %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, "<b>docs") {
		t.Fatalf("a repository's name was written as markup:\n%s", list)
	}
	if strings.Contains(list, "<html") {
		t.Fatalf("the list is a whole page, not the options:\n%s", list)
	}

	// What GitHub answers when it cannot be asked is said in the menu.
	a.server.GitHub = fakeGitHubRepos(t, `"not a list"`)
	res, list = a.get(repos)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(list, `role="alert"`) || strings.Contains(list, `role="option"`) {
		t.Fatalf("a failed listing is not reported:\n%s", list)
	}
}
