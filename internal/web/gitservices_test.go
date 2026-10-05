package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

var deployTokenRE = regexp.MustCompile(`mdt_[A-Za-z0-9_-]{20,}`)

func gitServiceForm(env db.Environment, name string) url.Values {
	return url.Values{
		"template": {db.TemplateGit}, "name": {name},
		"access": {"public"}, "repo": {"https://github.com/Acme/Stack"}, "branch": {"main"},
		"compose_path": {"deploy/compose.yaml"}, "auto_deploy": {"1"},
	}
}

func TestServiceFromGitPages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	newPage := "/projects/" + projectID + "/e/" + env.ID + "/services/new"

	// Offered next to the catalogue, with its own form.
	if _, body := a.get("/projects/" + projectID + "/e/" + env.ID + "/new"); !strings.Contains(body, "Compose file in a Git repository") || !strings.Contains(body, "template=git") {
		t.Fatal("the catalogue does not offer a stack from a repository")
	}
	res, form := a.get(newPage + "?template=git")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{`name="repo"`, `name="branch"`, `name="compose_path"`, `value="docker-compose.yml"`, "Deploy automatically when the branch is pushed"} {
		if !strings.Contains(form, want) {
			t.Errorf("the form is missing %s", want)
		}
	}
	if strings.Contains(form, `name="compose"`) {
		t.Error("the form of a stack from a repository asks for the file's text")
	}

	// What reaches git is checked before anything is stored.
	for key, c := range map[string][2]string{
		"repo":         {"https://github.com/acme", "repo-error"},
		"branch":       {"-x", "branch-error"},
		"compose_path": {"../../etc/passwd", "compose_path-error"},
		"access":       {"key:nosuchkey", "access-error"},
	} {
		bad := gitServiceForm(env, "stack")
		bad.Set(key, c[0])
		res, body := a.post(newPage+"?template=git", "/projects/"+projectID+"/e/"+env.ID+"/services", bad)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="`+c[1]+`"`) {
			t.Errorf("%s=%q: %d, error on the field: %v", key, c[0], res.StatusCode, strings.Contains(body, `id="`+c[1]+`"`))
		}
	}
	bad := gitServiceForm(env, "stack")
	bad.Set("repo", "git@github.com:acme/stack.git")
	if _, body := a.post(newPage+"?template=git", "/projects/"+projectID+"/e/"+env.ID+"/services", bad); !strings.Contains(body, "An SSH address needs a deploy key") {
		t.Error("an SSH address without a key was not explained")
	}

	res, _ = a.post(newPage+"?template=git", "/projects/"+projectID+"/e/"+env.ID+"/services", gitServiceForm(env, "stack"))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	id := strings.TrimPrefix(res.Header.Get("Location"), "/services/")
	svc, err := a.db.ServiceByID(ctx, id)
	if err != nil || !svc.FromGit() || svc.RepoName != "acme/stack" || svc.ComposePath != "deploy/compose.yaml" || !svc.AutoDeploy || svc.Compose != "" || svc.Status != db.AppCreated {
		t.Fatalf("%+v %v", svc, err)
	}
	base := "/services/" + id

	// The Compose tab: nothing fetched yet, the source, the ways in.
	res, page := a.get(base + "/compose")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"has not been fetched yet", "Save source", `value="https://github.com/Acme/Stack"`, "/webhooks/git/" + id, "/api/v1/deploy?uuid=" + id, "Create webhook secret", "Create deploy token"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Compose tab is missing %q", want)
		}
	}
	if strings.Contains(page, `<textarea id="compose"`) {
		t.Error("the file of a stack from a repository can be edited on the page")
	}

	// A GitHub App or deploy key of another team cannot be borrowed, on
	// creation or afterwards.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	theirKey, err := a.db.CreateSSHKey(ctx, "otherteam", "theirs", "ssh-ed25519 AAAA", a.seal("private"))
	if err != nil {
		t.Fatal(err)
	}
	borrowed := svc
	borrowed.SSHKeyID, borrowed.RepoURL = theirKey.ID, "git@github.com:acme/stack.git"
	if err := a.db.UpdateServiceSource(ctx, firstTeam(t, a), borrowed); err != db.ErrNotFound {
		t.Fatalf("saving a source with another team's key: %v", err)
	}
	borrowed.Name = "borrowed"
	if _, err := a.db.CreateService(ctx, firstTeam(t, a), borrowed); err != db.ErrNotFound {
		t.Fatalf("creating a service with another team's key: %v", err)
	}
	if got, _ := a.db.ServiceByID(ctx, id); got.SSHKeyID != "" {
		t.Fatal("another team's key was attached to the service")
	}

	// The file's text cannot be put in from the page; variables can.
	res, _ = a.post(base+"/compose", base+"/compose", url.Values{"compose": {"services: {evil: {image: x, privileged: true}}"}, "variables": {"TOKEN=abc"}, "connect_env": {"1"}})
	wantRedirect(t, res, base+"/compose")
	if got, _ := a.db.ServiceByID(ctx, id); got.Compose != "" || !got.ConnectEnv {
		t.Fatalf("after saving the tab: compose %q, connected %v", got.Compose, got.ConnectEnv)
	}

	// Source.
	src := url.Values{"access": {"public"}, "repo": {"https://github.com/acme/other"}, "branch": {"release/2"}, "compose_path": {"compose.yaml"}}
	res, _ = a.post(base+"/compose", base+"/source", src)
	wantRedirect(t, res, base+"/compose")
	if got, _ := a.db.ServiceByID(ctx, id); got.RepoName != "acme/other" || got.Branch != "release/2" || got.ComposePath != "compose.yaml" || got.AutoDeploy {
		t.Fatalf("%+v", got)
	}
	src.Set("compose_path", "/etc/passwd/../x")
	res, body := a.post(base+"/compose", base+"/source", src)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="compose_path-error"`) {
		t.Fatalf("a bad path in the source form: %d", res.StatusCode)
	}
	src.Set("compose_path", "deploy/compose.yaml")
	src.Set("repo", "https://github.com/acme/stack")
	src.Set("branch", "main")
	src.Set("auto_deploy", "1")
	a.post(base+"/compose", base+"/source", src)

	// The push webhook: unsigned until a secret exists, then the branch's
	// pushes deploy.
	hookPath := "/webhooks/git/" + id
	res, _ = a.hook(hookPath, []byte("guess"), "push", "d0", pushBody("acme/stack", "refs/heads/main"))
	wantStatus(t, res, http.StatusUnauthorized)
	res, _ = a.post(base+"/compose", base+"/webhook-secret", url.Values{})
	wantRedirect(t, res, base+"/compose#triggers")
	svc, _ = a.db.ServiceByID(ctx, id)
	key, err := a.server.Box.Open(svc.WebhookSecret)
	if err != nil || len(key) < 20 {
		t.Fatalf("webhook secret: %v", err)
	}
	if _, page = a.get(base + "/compose"); !strings.Contains(page, string(key)) {
		t.Fatal("the secret is not shown to the person who has to enter it at the Git host")
	}
	clones := func() int { return countLines(a.fake.Calls(), "git clone") }
	res, body = a.hook(hookPath, key, "push", "d1", pushBody("acme/stack", "refs/heads/develop"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":0`) {
		t.Fatalf("a push to another branch: %d %s", res.StatusCode, body)
	}
	res, body = a.hook(hookPath, key, "push", "d2", pushBody("acme/stack", "refs/heads/main"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) {
		t.Fatalf("a push to the branch: %d %s", res.StatusCode, body)
	}
	until(t, "the deployment the push started", func() bool { return clones() == 1 })
	a.waitService(id)
	// The same delivery again starts nothing.
	res, body = a.hook(hookPath, key, "push", "d2", pushBody("acme/stack", "refs/heads/main"))
	if !strings.Contains(body, `"deployments":0`) {
		t.Fatalf("a repeated delivery: %s", body)
	}

	// The deploy token: shown once, then usable.
	res, page = a.post(base+"/compose", base+"/deploy-token", url.Values{})
	wantStatus(t, res, http.StatusOK)
	token := deployTokenRE.FindString(page)
	if token == "" {
		t.Fatal("the new token is not shown")
	}
	if _, page = a.get(base + "/compose"); strings.Contains(page, token) || !strings.Contains(page, "Replace deploy token") {
		t.Fatal("the token is shown again, or the page does not know there is one")
	}
	call := func(tok string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, a.url+"/api/v1/deploy?uuid="+id, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		return a.do(&http.Client{}, req)
	}
	res, _ = call("mdt_wrongwrongwrongwrongwrongwrong")
	wantStatus(t, res, http.StatusUnauthorized)
	before := clones()
	res, body = call(token)
	if res.StatusCode != http.StatusAccepted || !strings.Contains(body, id) {
		t.Fatalf("deploy by token: %d %s", res.StatusCode, body)
	}
	until(t, "the deployment the token started", func() bool { return clones() == before+1 })
	a.waitService(id)
	res, _ = a.post(base+"/compose", base+"/deploy-token", url.Values{"revoke": {"1"}})
	wantRedirect(t, res, base+"/compose#triggers")
	res, _ = call(token)
	wantStatus(t, res, http.StatusUnauthorized)

	// A stack that was pasted has no source, secret or token to set.
	pasted := a.newService(projectID, env, "pasted", url.Values{"template": {db.TemplateCustom}, "compose": {"services:\n  web:\n    image: nginx:alpine\n"}})
	for _, path := range []string{"/source", "/webhook-secret", "/deploy-token"} {
		res, _ := a.post("/services/"+pasted.ID+"/compose", "/services/"+pasted.ID+path, src)
		wantStatus(t, res, http.StatusNotFound)
	}
	if _, page = a.get("/services/" + pasted.ID + "/compose"); strings.Contains(page, "Save source") || !strings.Contains(page, `<textarea id="compose"`) {
		t.Fatal("a pasted stack's Compose tab")
	}
}

func countLines(lines []string, part string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, part) {
			n++
		}
	}
	return n
}
