package source

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeGitLab is a GitLab instance that knows one token.
func fakeGitLab(t *testing.T, projects func(page int) string) (*GitLab, string, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer glpat-good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"401 Unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v4/user":
			io.WriteString(w, `{"id":3,"username":"ada"}`)
		case "/api/v4/projects":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if r.URL.Query().Get("membership") != "true" || r.URL.Query().Get("simple") != "true" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, projects(page))
		case "/api/v4/elsewhere":
			http.Redirect(w, r, "https://example.invalid/api/v4/user", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<html>not a GitLab</html>`)
		}
	}))
	t.Cleanup(srv.Close)
	return &GitLab{HTTP: srv.Client()}, srv.URL, &seen
}

func TestGitLabUserAndRefusals(t *testing.T) {
	ctx := context.Background()
	g, base, seen := fakeGitLab(t, func(int) string { return `[]` })

	user, err := g.User(ctx, base, "glpat-good-token")
	if err != nil || user != "ada" {
		t.Fatalf("user: %q, %v", user, err)
	}
	if len(*seen) != 1 || (*seen)[0] != "/api/v4/user Bearer glpat-good-token" {
		t.Fatalf("the request: %v", *seen)
	}
	// A wrong token is said to be wrong, in words that hold neither the
	// token nor the address.
	_, err = g.User(ctx, base, "glpat-wrong-token")
	if err == nil || !strings.Contains(err.Error(), "did not accept the token") || strings.Contains(err.Error(), "glpat") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("a wrong token: %v", err)
	}
	// A redirect is not followed: the token would go with it.
	var out struct{}
	err = g.call(ctx, base, "glpat-good-token", "/api/v4/elsewhere", &out)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("a redirect: %v", err)
	}
	// An address that is no GitLab says so, without the page it answered.
	err = g.call(ctx, base, "glpat-good-token", "/nothing-here", &out)
	if err == nil || strings.Contains(err.Error(), "html") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("not a GitLab: %v", err)
	}
	// An instance that does not answer: the cause, not the address.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "https://" + l.Addr().String()
	l.Close()
	if _, err := g.User(ctx, closed, "glpat-good-token"); err == nil || !strings.Contains(err.Error(), "did not answer") || strings.Contains(err.Error(), "/api/v4") {
		t.Fatalf("an instance that does not answer: %v", err)
	}
}

func TestGitLabProjects(t *testing.T) {
	ctx := context.Background()
	g, base, _ := fakeGitLab(t, func(page int) string {
		if page > 1 {
			return `[]`
		}
		return `[
			{"path_with_namespace":"acme/shop","default_branch":"main","visibility":"private"},
			{"path_with_namespace":"acme/platform/api","default_branch":"trunk","visibility":"internal"},
			{"path_with_namespace":"acme/site","default_branch":"main","visibility":"public"},
			{"path_with_namespace":"acme/<b>docs","default_branch":"main","visibility":"private"},
			{"path_with_namespace":"../../etc","default_branch":"main","visibility":"private"},
			{"path_with_namespace":"nogroup","default_branch":"main","visibility":"private"},
			{"path_with_namespace":"acme/odd","default_branch":"--upload-pack=x","visibility":"private"}]`
	})
	got, err := g.Projects(ctx, base, "glpat-good-token")
	if err != nil {
		t.Fatal(err)
	}
	want := []RepoInfo{
		{FullName: "acme/shop", DefaultBranch: "main", Private: true},
		{FullName: "acme/platform/api", DefaultBranch: "trunk", Private: true},
		{FullName: "acme/site", DefaultBranch: "main", Private: false},
		// Its branch is not one git would be given: the name stays, the
		// branch does not.
		{FullName: "acme/odd", DefaultBranch: "", Private: true},
	}
	if len(got) != len(want) {
		t.Fatalf("projects: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("project %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	// Full pages are followed, up to what the picker shows.
	full := "[" + strings.TrimSuffix(strings.Repeat(`{"path_with_namespace":"acme/p","default_branch":"main","visibility":"private"},`, 100), ",") + "]"
	pages := 0
	g, base, _ = fakeGitLab(t, func(int) string { pages++; return full })
	if got, err = g.Projects(ctx, base, "glpat-good-token"); err != nil || len(got) != maxRepos || pages != maxRepos/100 {
		t.Fatalf("%d projects from %d pages, %v", len(got), pages, err)
	}
	if _, err = g.Projects(ctx, base, "glpat-wrong-token"); err == nil {
		t.Fatal("a wrong token listed projects")
	}
}

func TestGitLabBaseAndToken(t *testing.T) {
	for raw, want := range map[string]string{
		"https://gitlab.com":              "https://gitlab.com",
		" https://GitLab.Example.com/ ":   "https://gitlab.example.com",
		"https://git.example.com:8443":    "https://git.example.com:8443",
		"http://gitlab.example.com":       "",
		"gitlab.com":                      "",
		"https://gitlab.com/group":        "",
		"https://user:pw@gitlab.com":      "",
		"https://gitlab.com?x=1":          "",
		"https://gitlab.com#x":            "",
		"https://":                        "",
		"https://gitlab.com/../x":         "",
		"https://-oProxyCommand=x":        "",
		"ext::sh -c id":                   "",
		"https://gitlab.com\nHost: other": "",
	} {
		got, ok := GitLabBase(raw)
		if got != want || ok != (want != "") {
			t.Errorf("GitLabBase(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	if GitLabHost("https://git.example.com:8443") != "git.example.com" {
		t.Error("the host of an address with a port")
	}
	for token, want := range map[string]bool{
		"glpat-AbC_123.def-456": true,
		"short":                 false,
		"has space in it":       false,
		"-starts-with-a-dash":   false,
		"line\nbreak-in-token":  false,
		"":                      false,
	} {
		if ValidGitLabToken(token) != want {
			t.Errorf("ValidGitLabToken(%q) is not %v", token, want)
		}
	}
	if h := GitLabAuthHeader("glpat-x"); h != "Authorization: Basic b2F1dGgyOmdscGF0LXg=" {
		t.Errorf("the clone header: %q", h)
	}
}
