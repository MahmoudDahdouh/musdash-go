package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// gitForm is a valid New app form for a public repository.
func gitForm(env db.Environment, name string) url.Values {
	return url.Values{
		"source": {"git"}, "name": {name}, "port": {"3000"},
		"access": {"public"}, "repo": {"https://github.com/Acme/Shop"}, "branch": {"main"},
		"build_pack": {"dockerfile"}, "auto_deploy": {"1"},
	}
}

// newGitApp creates a Git app through the form, without deploying it.
func (a *app) newGitApp(projectID string, env db.Environment, name string, extra url.Values) db.App {
	a.t.Helper()
	form := gitForm(env, name)
	for k, v := range extra {
		form[k] = v
	}
	page := "/projects/" + projectID + "/e/" + env.ID + "/apps/new?source=git"
	res, body := a.post(page, "/projects/"+projectID+"/e/"+env.ID+"/apps", form)
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("create git app: %d\n%s", res.StatusCode, body)
	}
	id := strings.Split(strings.TrimPrefix(res.Header.Get("Location"), "/apps/"), "/")[0]
	got, err := a.db.AppByID(context.Background(), id)
	if err != nil {
		a.t.Fatal(err)
	}
	return got
}

// hook sends a signed webhook.
func (a *app) hook(path string, secretKey []byte, event, delivery, body string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", source.Sign(secretKey, []byte(body)))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	// A plain client: webhooks carry no cookies.
	return a.do(&http.Client{}, req)
}

func pushBody(repo, ref string) string {
	return `{"ref":"` + ref + `","after":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","repository":{"full_name":"` + repo + `"},"commits":[{"message":"` + strings.Repeat("x", 2000) + `"}]}`
}

func (a *app) deployJobs() int {
	var n int
	a.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'deploy'`).Scan(&n)
	return n
}

func TestCreateGitApp(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")

	_, page := a.get("/projects/" + projectID + "/e/" + env.ID + "/apps/new?source=git")
	for _, want := range []string{"Git repository", `name="repo"`, `name="branch"`, "Nothing: the repository is public"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Git form is missing %q", want)
		}
	}

	got := a.newGitApp(projectID, env, "web", url.Values{"base_dir": {"/apps/web/"}, "dockerfile_path": {"docker/Dockerfile"}})
	if got.Source != db.SourceGit || got.RepoName != "acme/shop" || got.Branch != "main" || got.BuildPack != "dockerfile" ||
		got.BaseDir != "apps/web" || got.DockerfilePath != "docker/Dockerfile" || !got.AutoDeploy || got.Port != 3000 || got.Image != "" {
		t.Fatalf("stored app: %+v", got)
	}
	// A static site always listens on 80, whatever was typed.
	static := a.newGitApp(projectID, env, "site", url.Values{"build_pack": {"static"}, "publish_dir": {"dist"}, "spa_fallback": {"1"}, "port": {"3000"}})
	if static.Port != 80 || !static.SPAFallback || static.PublishDir != "dist" {
		t.Fatalf("static app: %+v", static)
	}
	_, overview := a.get("/apps/" + got.ID)
	if !strings.Contains(overview, "acme/shop @ main") {
		t.Fatal("the app page does not show the repository and branch")
	}
}

func TestGitFormValidation(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	ctx := context.Background()
	team := firstTeam(t, a)
	key, _ := a.db.CreateSSHKey(ctx, team, "k", "ssh-ed25519 AAAA", "sealed")
	// A deploy key and a GitHub App that belong to another team.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	otherKey, _ := a.db.CreateSSHKey(ctx, "otherteam", "theirs", "ssh-ed25519 BBBB", "sealed")

	cases := []struct {
		field url.Values
		want  string
	}{
		{url.Values{"repo": {"ext::sh -c id"}}, "enter a repository address"},
		{url.Values{"repo": {"file:///etc/passwd"}}, "Enter a repository address"},
		{url.Values{"repo": {"--upload-pack=evil"}}, "Enter a repository address"},
		{url.Values{"repo": {"https://user:pw@github.com/a/b"}}, "Leave the user name and password out"},
		{url.Values{"repo": {"git@github.com:acme/shop.git"}}, "An SSH address needs a deploy key"},
		{url.Values{"access": {"key:" + key.ID}}, "A deploy key needs the SSH form"},
		{url.Values{"access": {"key:" + otherKey.ID}, "repo": {"git@github.com:acme/shop.git"}}, "Choose how the repository is read"},
		{url.Values{"access": {"source:nope"}}, "Choose how the repository is read"},
		{url.Values{"branch": {"--force"}}, "Enter a branch name"},
		{url.Values{"build_pack": {"buildpacks"}}, "Choose how the app is built"},
		{url.Values{"base_dir": {"../../etc"}}, "Enter a path inside the repository"},
		{url.Values{"dockerfile_path": {"/etc/passwd"}}, "Enter a path inside the repository"},
		{url.Values{"publish_dir": {"a;b"}}, "Enter a path inside the repository"},
	}
	page := "/projects/" + projectID + "/e/" + env.ID + "/apps/new?source=git"
	for _, c := range cases {
		form := gitForm(env, "web")
		for k, v := range c.field {
			form[k] = v
		}
		res, body := a.post(page, "/projects/"+projectID+"/e/"+env.ID+"/apps", form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(body), strings.ToLower(c.want)) {
			t.Errorf("%v: status %d, want 422 with %q", c.field, res.StatusCode, c.want)
		}
	}
	if apps, _ := a.db.ListApps(ctx, env.ID); len(apps) != 0 {
		t.Fatalf("%d apps exist after rejected forms", len(apps))
	}

	// With a deploy key and the SSH address, it is accepted.
	got := a.newGitApp(projectID, env, "web", url.Values{"access": {"key:" + key.ID}, "repo": {"git@git.example.com:acme/shop.git"}})
	if got.SSHKeyID != key.ID || got.RepoName != "acme/shop" {
		t.Fatalf("stored app: %+v", got)
	}
}

func TestAppSourceAndRuntimeSettings(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	page := "/apps/" + app.ID + "/settings"

	// Source card.
	src := url.Values{"access": {"public"}, "repo": {"https://github.com/acme/other"}, "branch": {"release/2"}, "build_pack": {"static"}, "publish_dir": {"public"}}
	res, _ := a.post(page, "/apps/"+app.ID+"/source", src)
	wantRedirect(t, res, page)
	got, _ := a.db.AppByID(context.Background(), app.ID)
	// Unticked boxes are stored as off.
	if got.RepoName != "acme/other" || got.Branch != "release/2" || got.BuildPack != "static" || got.AutoDeploy || got.SPAFallback {
		t.Fatalf("after saving the source: %+v", got)
	}
	// The app was created listening on 3000; a static site listens on 80.
	if got.Port != 80 {
		t.Fatalf("switching to the static build pack left the port at %d", got.Port)
	}
	src.Set("branch", "bad branch")
	res, body := a.post(page, "/apps/"+app.ID+"/source", src)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter a branch name") {
		t.Fatalf("bad branch accepted: %d", res.StatusCode)
	}

	// An image app has no source to save.
	img := a.newApp(projectID, env, "img", false, nil)
	res, _ = a.post("/apps/"+img+"/settings", "/apps/"+img+"/source", src)
	wantStatus(t, res, http.StatusNotFound)

	// General form: a Git app needs no image; start command and options.
	general := url.Values{"name": {"web"}, "port": {"3000"}, "health_timeout": {"60"}, "start_command": {"node server.js"}, "docker_options": {"--shm-size 128m --init"}}
	res, body = a.post(page, page, general)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("general settings: %d\n%s", res.StatusCode, body)
	}
	got, _ = a.db.AppByID(context.Background(), app.ID)
	if got.StartCommand != "node server.js" || got.DockerOptions != "--shm-size 128m --init" {
		t.Fatalf("stored: %+v", got)
	}
	for option, want := range map[string]string{
		"--privileged":   "--privileged is not allowed",
		"-v /:/host":     "-v is not allowed",
		"--network host": "--network is not allowed",
		"--shm-size":     "needs a value",
	} {
		general.Set("docker_options", option)
		res, body := a.post(page, page, general)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("option %q: status %d, want 422 with %q", option, res.StatusCode, want)
		}
	}
}

func TestBuildTimeVariables(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	page := "/apps/" + app.ID + "/environment"

	for names, want := range map[string]string{
		"DOCKER_HOST=tcp://evil:2375": "DOCKER_HOST cannot be a build-time variable",
		"LD_PRELOAD=/tmp/x.so":        "LD_PRELOAD cannot be a build-time variable",
		"PATH=/tmp":                   "PATH cannot be a build-time variable",
		"not valid":                   "Line 1",
	} {
		res, body := a.post(page, page, url.Values{"vars": {"A=1"}, "build_vars": {names}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("%q: status %d, want 422 with %q", names, res.StatusCode, want)
		}
	}
	res, body := a.post(page, page, url.Values{"vars": {"TOKEN=runtime"}, "build_vars": {"TOKEN=build"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already a runtime variable") {
		t.Errorf("one name as both kinds: %d", res.StatusCode)
	}

	res, _ = a.post(page, page, url.Values{"vars": {"PORT=3000"}, "build_vars": {"NPM_TOKEN=s3cret"}})
	wantRedirect(t, res, page)
	stored, _ := a.db.ListEnvVars(context.Background(), db.KindApp, app.ID)
	if len(stored) != 2 {
		t.Fatalf("%d variables", len(stored))
	}
	for _, v := range stored {
		if v.BuildTime != (v.Key == "NPM_TOKEN") || strings.Contains(v.Value, "s3cret") {
			t.Fatalf("variable %s: build=%v", v.Key, v.BuildTime)
		}
	}
	_, body = a.get(page)
	if !strings.Contains(body, "Given to the build") || !strings.Contains(body, "NPM_TOKEN") || strings.Contains(body, "s3cret") {
		t.Fatal("the tab should list both groups by name and no value")
	}
	if _, body = a.get(page + "/edit"); !strings.Contains(body, "NPM_TOKEN=s3cret") || !strings.Contains(body, "PORT=3000") {
		t.Fatal("the editor does not show both blocks")
	}
}

func TestGitHubAppWebhook(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	team := firstTeam(t, a)

	whSecret := []byte("github-generated-secret")
	sealedSecret := a.seal(string(whSecret))
	src, _ := a.db.StartGitSource(ctx, team, "musdash-test", "st")
	src.AppID, src.Slug, src.WebhookSecret, src.PrivateKey = 777, "musdash-test", sealedSecret, a.seal("pem")
	if err := a.db.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	onMain := a.newGitApp(projectID, env, "web", url.Values{"access": {"source:" + src.ID}})
	a.newGitApp(projectID, env, "staging", url.Values{"access": {"source:" + src.ID}, "branch": {"develop"}})
	a.newGitApp(projectID, env, "manual", url.Values{"access": {"source:" + src.ID}, "auto_deploy": {"0"}})
	// The same repository, but not connected through this App.
	a.newGitApp(projectID, env, "public", nil)
	path := "/webhooks/github/" + src.ID

	// Nothing unsigned or wrongly signed is acted on.
	for name, key := range map[string][]byte{"wrong secret": []byte("guess"), "empty secret": {}} {
		res, _ := a.hook(path, key, "push", "d-bad-"+name, pushBody("Acme/Shop", "refs/heads/main"))
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, res.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, a.url+path, strings.NewReader(pushBody("Acme/Shop", "refs/heads/main")))
	if res, _ := a.do(&http.Client{}, req); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unsigned: status %d", res.StatusCode)
	}
	// An unknown source answers exactly like a bad signature.
	if res, _ := a.hook("/webhooks/github/nosuchsource", whSecret, "push", "d-x", pushBody("Acme/Shop", "refs/heads/main")); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown source: status %d", res.StatusCode)
	}
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("%d deployments queued by rejected webhooks", n)
	}

	// Events that are not a branch push are acknowledged and ignored.
	for name, c := range map[string][2]string{
		"ping":           {"ping", `{"zen":"Design for failure."}`},
		"tag push":       {"push", pushBody("Acme/Shop", "refs/tags/v1")},
		"other branch":   {"push", pushBody("Acme/Shop", "refs/heads/feature")},
		"other repo":     {"push", pushBody("acme/elsewhere", "refs/heads/main")},
		"branch deleted": {"push", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"full_name":"Acme/Shop"}}`},
		"not json":       {"push", `ref=refs/heads/main`},
	} {
		res, body := a.hook(path, whSecret, c[0], "d-"+name, c[1])
		if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":0`) {
			t.Errorf("%s: %d %s", name, res.StatusCode, body)
		}
	}
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("%d deployments queued by ignored events", n)
	}

	// A push to main deploys exactly the one app that tracks it through
	// this App with auto-deploy on.
	res, body := a.hook(path, whSecret, "push", "delivery-1", pushBody("Acme/Shop", "refs/heads/main"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) {
		t.Fatalf("push: %d %s", res.StatusCode, body)
	}
	list, _ := a.db.ListDeployments(ctx, onMain.ID, 5)
	if len(list) != 1 || list[0].Trigger != "push" {
		t.Fatalf("deployments of the tracked app: %+v", list)
	}
	// GitHub redelivers; the same delivery id deploys nothing more.
	res, body = a.hook(path, whSecret, "push", "delivery-1", pushBody("Acme/Shop", "refs/heads/main"))
	if !strings.Contains(body, `"deployments":0`) || !strings.Contains(body, "already handled") {
		t.Fatalf("redelivery: %d %s", res.StatusCode, body)
	}
	if n := a.deployJobs(); n != 1 {
		t.Fatalf("%d deployments queued, want 1", n)
	}
}

func TestManualWebhookAndDeployToken(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings := "/apps/" + app.ID + "/settings"
	hookPath := "/webhooks/git/" + app.ID

	// Before a secret exists, nothing verifies: not even an empty-key MAC.
	if res, _ := a.hook(hookPath, []byte{}, "push", "", pushBody("acme/shop", "refs/heads/main")); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("webhook with no secret configured: %d", res.StatusCode)
	}

	key := []byte(a.webhookSecret(app.ID))
	// The app's own Settings no longer hold it: they point at the Keys page.
	if _, page := a.get(settings); strings.Contains(page, string(key)) || !strings.Contains(page, `href="/keys#deploy-tokens"`) {
		t.Fatal("the settings page shows the secret, or does not lead to the Keys page")
	}

	if res, _ := a.hook(hookPath, key, "push", "m1", pushBody("acme/shop", "refs/heads/other")); res.StatusCode != http.StatusOK || a.deployJobs() != 0 {
		t.Fatal("a push to another branch deployed")
	}
	if res, _ := a.hook(hookPath, key, "push", "m2", pushBody("someone/else", "refs/heads/main")); res.StatusCode != http.StatusOK || a.deployJobs() != 0 {
		t.Fatal("a push to another repository deployed")
	}
	res, body := a.hook(hookPath, key, "push", "m3", pushBody("Acme/Shop", "refs/heads/main"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) || a.deployJobs() != 1 {
		t.Fatalf("push: %d %s", res.StatusCode, body)
	}

	// Deploy token: shown once, stored hashed.
	res, page := a.post("/keys", "/apps/"+app.ID+"/deploy-token", nil)
	wantStatus(t, res, http.StatusOK)
	tm := regexp.MustCompile(`mdt_[A-Za-z0-9_-]{40,}`).FindString(page)
	if tm == "" {
		t.Fatal("the new deploy token is not shown")
	}
	if _, page = a.get("/keys"); strings.Contains(page, tm) || !strings.Contains(page, "Has a token") {
		t.Fatal("the token is shown again, or the Keys page does not know there is one")
	}
	stored, _ := a.db.AppByID(ctx, app.ID)
	if stored.DeployTokenHash != secret.HashToken(tm) || strings.Contains(stored.DeployTokenHash, tm) {
		t.Fatal("the token is not stored as a hash")
	}
	if _, again := a.get(settings); strings.Contains(again, tm) {
		t.Fatal("the token is shown again on a later visit")
	}

	api := func(appID, token string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, a.url+"/api/v1/deploy?uuid="+appID, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return a.do(&http.Client{}, req)
	}
	before := a.deployJobs()
	for name, c := range map[string][2]string{
		"no token":      {app.ID, ""},
		"wrong token":   {app.ID, "mdt_" + strings.Repeat("x", 43)},
		"wrong app":     {"nosuchapp", tm},
		"short token":   {app.ID, "abc"},
		"session token": {app.ID, "not-a-deploy-token-at-all-1234567890"},
	} {
		if res, _ := api(c[0], c[1]); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, res.StatusCode)
		}
	}
	if a.deployJobs() != before {
		t.Fatal("a rejected API call queued a deployment")
	}
	res, body = api(app.ID, tm)
	var out map[string]string
	json.Unmarshal([]byte(body), &out)
	if res.StatusCode != http.StatusAccepted || out["deployment_id"] == "" || out["status"] != "queued" {
		t.Fatalf("api deploy: %d %s", res.StatusCode, body)
	}
	if dep, err := a.db.Deployment(ctx, app.ID, out["deployment_id"]); err != nil || dep.Trigger != "api" {
		t.Fatalf("deployment: %+v %v", dep, err)
	}

	res, _ = a.post("/keys", "/apps/"+app.ID+"/deploy-token", url.Values{"revoke": {"1"}})
	wantRedirect(t, res, "/keys#deploy-tokens")
	if res, _ := api(app.ID, tm); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("a revoked token still works")
	}
}

func TestPushBurstQueuesOneDeployment(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	key := []byte(a.webhookSecret(app.ID))

	// Hold the queue so deployments stay queued.
	a.db.Exec(`INSERT INTO jobs (id, kind, status, lock_key, run_after, created_at) VALUES ('blocker', 'none', 'running', ?, 0, 0)`, "build:"+app.ServerID)
	for i := range 5 {
		a.hook("/webhooks/git/"+app.ID, key, "push", "burst-"+itoa(i), pushBody("acme/shop", "refs/heads/main"))
	}
	if n := a.deployJobs(); n != 1 {
		t.Fatalf("five pushes in a row queued %d deployments, want 1", n)
	}
}

func TestWebhookHardening(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	key := []byte(a.webhookSecret(app.ID))
	hookPath := "/webhooks/git/" + app.ID
	deliveries := func() (n int) {
		a.db.QueryRow(`SELECT count(*) FROM webhook_deliveries`).Scan(&n)
		return n
	}

	// A body over the limit is refused although its signature is right.
	huge := `{"ref":"refs/heads/main","after":"abc","pad":"` + strings.Repeat("x", maxWebhookBytes) + `"}`
	if res, _ := a.hook(hookPath, key, "push", "big-1", huge); res.StatusCode != http.StatusUnauthorized || a.deployJobs() != 0 {
		t.Fatalf("oversized body: %d, %d jobs", res.StatusCode, a.deployJobs())
	}

	// A push that deploys nothing is not remembered.
	a.hook(hookPath, key, "push", "other-branch", pushBody("acme/shop", "refs/heads/other"))
	if deliveries() != 0 {
		t.Fatal("a delivery that deployed nothing was recorded")
	}

	// A form-encoded webhook is signed correctly but unreadable: say why.
	body := "payload=" + url.QueryEscape(pushBody("acme/shop", "refs/heads/main"))
	req, _ := http.NewRequest(http.MethodPost, a.url+hookPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Hub-Signature-256", source.Sign(key, []byte(body)))
	if res, out := a.do(&http.Client{}, req); res.StatusCode != http.StatusOK || !strings.Contains(out, "application/json") || a.deployJobs() != 0 {
		t.Fatalf("form-encoded webhook: %d %s", res.StatusCode, out)
	}

	// A delivery that fails is forgotten, so its redelivery is acted on.
	a.db.Exec(`CREATE TRIGGER refuse_deployments BEFORE INSERT ON deployments BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
	if res, _ := a.hook(hookPath, key, "push", "retry-1", pushBody("acme/shop", "refs/heads/main")); res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a failed enqueue answered %d", res.StatusCode)
	}
	a.db.Exec(`DROP TRIGGER refuse_deployments`)
	if deliveries() != 0 {
		t.Fatal("a failed delivery stayed recorded")
	}
	// Hold the queue so the deployment stays queued.
	a.db.Exec(`INSERT INTO jobs (id, kind, status, lock_key, run_after, created_at) VALUES ('blocker', 'none', 'running', ?, 0, 0)`, "build:"+app.ServerID)
	res, out := a.hook(hookPath, key, "push", "retry-1", pushBody("acme/shop", "refs/heads/main"))
	if res.StatusCode != http.StatusOK || !strings.Contains(out, `"deployments":1`) {
		t.Fatalf("redelivery after a failure: %d %s", res.StatusCode, out)
	}

	// An id that is not a plausible delivery id is never stored.
	a.db.Exec(`UPDATE deployments SET status = 'failed'`)
	for _, id := range []string{strings.Repeat("a", 65), "has space", "semi;colon", strings.Repeat("z", 5000)} {
		a.db.Exec(`UPDATE deployments SET status = 'failed'`)
		if res, _ := a.hook(hookPath, key, "push", id, pushBody("acme/shop", "refs/heads/main")); res.StatusCode != http.StatusOK {
			t.Fatalf("delivery id %.20q: %d", id, res.StatusCode)
		}
	}
	var longest int
	a.db.QueryRow(`SELECT coalesce(max(length(id)), 0) FROM webhook_deliveries`).Scan(&longest)
	if deliveries() != 1 || longest > 64 {
		t.Fatalf("%d deliveries stored, the longest id %d characters", deliveries(), longest)
	}

	// The deploy API hands back the waiting deployment rather than queueing
	// another behind it.
	a.db.Exec(`UPDATE deployments SET status = 'failed'`)
	_, page := a.post("/keys", "/apps/"+app.ID+"/deploy-token", nil)
	token := regexp.MustCompile(`mdt_[A-Za-z0-9_-]{40,}`).FindString(page)
	api := func() map[string]string {
		req, _ := http.NewRequest(http.MethodPost, a.url+"/api/v1/deploy?uuid="+app.ID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, body := a.do(&http.Client{}, req)
		wantStatus(t, res, http.StatusAccepted)
		var out map[string]string
		json.Unmarshal([]byte(body), &out)
		return out
	}
	first, second := api(), api()
	if first["deployment_id"] == "" || first["deployment_id"] != second["deployment_id"] {
		t.Fatalf("two API calls queued two deployments: %v %v", first, second)
	}
	if list, _ := a.db.ListDeployments(ctx, app.ID, 100); len(list) == 0 || list[0].ID != first["deployment_id"] {
		t.Fatal("the deployment the API named is not the newest")
	}
}

func TestWebhookRateLimit(t *testing.T) {
	a := newApp(t, false)
	limited := 0
	for range 130 {
		res, _ := a.hook("/webhooks/github/x", []byte("k"), "push", "", "{}")
		if res.StatusCode == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 10 {
		t.Fatalf("%d of 130 requests were limited, want 10", limited)
	}
}

// fakeGitHubAPI stands in for api.github.com in the manifest flow.
func fakeGitHubAPI(t *testing.T) *source.GitHub {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions" {
			io.WriteString(w, `{"id":4242,"slug":"musdash-shop","name":"musdash shop","html_url":"https://github.com/apps/musdash-shop","client_id":"Iv1.x","client_secret":"client-secret-value","webhook_secret":"webhook-secret-value","pem":"-----BEGIN RSA PRIVATE KEY-----\nprivate-key-value\n-----END RSA PRIVATE KEY-----\n"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	return &source.GitHub{APIBase: srv.URL, HTTP: srv.Client()}
}

func TestGitHubAppManifestFlow(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubAPI(t)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)

	// Validation.
	for field, c := range map[string][2]string{
		"name":         {"x", "3 to 34 letters"},
		"organization": {"not a valid org!", "organisation's GitHub name"},
		"base":         {"javascript:alert(1)", "Enter this dashboard's address"},
	} {
		form := url.Values{"name": {"musdash shop"}, "base": {"https://dash.example.com"}}
		form.Set(field, c[0])
		res, body := a.post("/sources", "/sources/github", form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(htmlUnescape(body), c[1]) {
			t.Errorf("%s=%q: status %d, want 422 with %q", field, c[0], res.StatusCode, c[1])
		}
	}

	res, page := a.post("/sources", "/sources/github", url.Values{"name": {"musdash shop"}, "organization": {"acme"}, "base": {"https://dash.example.com/"}})
	wantStatus(t, res, http.StatusOK)
	// Only this page may post a form to GitHub.
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://github.com") {
		t.Fatalf("hand-off page policy: %q", csp)
	}
	if other, _ := a.get("/sources"); strings.Contains(other.Header.Get("Content-Security-Policy"), "github.com") {
		t.Fatal("another page also allows posting to GitHub")
	}
	action := regexp.MustCompile(`action="(https://github\.com/organizations/acme/settings/apps/new\?state=([^"]+))"`).FindStringSubmatch(page)
	if action == nil {
		t.Fatalf("no form posting to GitHub:\n%s", page)
	}
	state, _ := url.QueryUnescape(action[2])
	raw := regexp.MustCompile(`name="manifest" value="([^"]+)"`).FindStringSubmatch(page)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(htmlUnescape(raw[1])), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	hook := manifest["hook_attributes"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(hook, "https://dash.example.com/webhooks/github/") || manifest["redirect_url"] != "https://dash.example.com/sources/github/callback" || manifest["public"] != false {
		t.Fatalf("manifest: %v", manifest)
	}
	perms := manifest["default_permissions"].(map[string]any)
	if perms["contents"] != "read" || len(perms) != 3 {
		t.Fatalf("permissions: %v", perms)
	}
	sourceID := strings.TrimPrefix(hook, "https://dash.example.com/webhooks/github/")
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 0 {
		t.Fatal("an unfinished App is listed as connected")
	}

	// A callback with the wrong state exchanges nothing.
	res, _ = a.get("/sources/github/callback?code=good-code&state=forged")
	wantRedirect(t, res, "/sources")
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 0 {
		t.Fatal("a forged state completed the flow")
	}
	// GitHub rejects the code.
	res, _ = a.get("/sources/github/callback?code=bad-code&state=" + url.QueryEscape(state))
	wantRedirect(t, res, "/sources")
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 0 {
		t.Fatal("a rejected code completed the flow")
	}

	res, _ = a.get("/sources/github/callback?code=good-code&state=" + url.QueryEscape(state))
	wantRedirect(t, res, "/sources")
	list, _ := a.db.ListGitSources(ctx, team)
	if len(list) != 1 || list[0].ID != sourceID || list[0].AppID != 4242 || list[0].Slug != "musdash-shop" || list[0].State != "" {
		t.Fatalf("stored source: %+v", list)
	}
	// Nothing secret is stored in the clear.
	for name, sealed := range map[string]string{"client secret": list[0].ClientSecret, "private key": list[0].PrivateKey, "webhook secret": list[0].WebhookSecret} {
		if sealed == "" || strings.Contains(sealed, "secret-value") || strings.Contains(sealed, "private-key-value") {
			t.Errorf("%s is not sealed: %q", name, sealed)
		}
	}
	if plain, _ := a.server.Box.OpenString(list[0].WebhookSecret); plain != "webhook-secret-value" {
		t.Fatalf("webhook secret round trip: %q", plain)
	}
	// The state cannot be replayed.
	res, _ = a.get("/sources/github/callback?code=good-code&state=" + url.QueryEscape(state))
	wantRedirect(t, res, "/sources")
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 1 {
		t.Fatal("replaying the callback created a second source")
	}

	_, page = a.get("/sources")
	for _, want := range []string{"musdash shop", "https://github.com/apps/musdash-shop/installations/new"} {
		if !strings.Contains(page, want) {
			t.Errorf("sources page is missing %q", want)
		}
	}
	for _, leak := range []string{"client-secret-value", "webhook-secret-value", "private-key-value"} {
		if strings.Contains(page, leak) {
			t.Fatalf("the sources page shows %q", leak)
		}
	}

	// In use by an app: cannot be removed.
	projectID, env := a.project("Shop")
	a.newGitApp(projectID, env, "web", url.Values{"access": {"source:" + sourceID}})
	res, _ = a.post("/sources", "/sources/github/"+sourceID+"/delete", nil)
	wantRedirect(t, res, "/sources")
	if list, _ := a.db.ListGitSources(ctx, team); len(list) != 1 {
		t.Fatal("a source in use was removed")
	}
}

func TestDeployKeys(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)

	res, body := a.post("/keys", "/sources/keys", url.Values{"key_name": {""}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter a name") || !strings.Contains(body, "data-autoopen") {
		t.Fatalf("empty name: %d", res.StatusCode)
	}
	res, body = a.post("/keys", "/sources/keys", url.Values{"key_name": {"shop repository"}})
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "ssh-ed25519 ") || !strings.Contains(body, "Key created") {
		t.Fatal("the new public key is not shown")
	}
	if strings.Contains(body, "PRIVATE KEY") {
		t.Fatal("the private key is shown")
	}
	keys, _ := a.db.ListSSHKeys(ctx, team)
	if len(keys) != 1 || strings.Contains(keys[0].PrivateKey, "PRIVATE KEY") {
		t.Fatalf("stored keys: %+v", keys)
	}
	if plain, err := a.server.Box.OpenString(keys[0].PrivateKey); err != nil || !strings.Contains(plain, "OPENSSH PRIVATE KEY") {
		t.Fatalf("private key does not open: %v", err)
	}

	projectID, env := a.project("Shop")
	a.newGitApp(projectID, env, "web", url.Values{"access": {"key:" + keys[0].ID}, "repo": {"git@github.com:acme/shop.git"}})
	res, _ = a.post("/keys", "/sources/keys/"+keys[0].ID+"/delete", nil)
	wantRedirect(t, res, "/keys#ssh-keys")
	if keys, _ := a.db.ListSSHKeys(ctx, team); len(keys) != 1 {
		t.Fatal("a key in use was deleted")
	}
}

func TestOtherTeamsSourcesAreNotReachable(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	src, _ := a.db.StartGitSource(ctx, "otherteam", "theirs", "their-state")
	src.AppID, src.Slug, src.PrivateKey = 9, "theirs", a.seal("pem")
	a.db.FinishGitSource(ctx, src)
	pending, _ := a.db.StartGitSource(ctx, "otherteam", "pending", "pending-state")
	key, _ := a.db.CreateSSHKey(ctx, "otherteam", "theirs", "ssh-ed25519 AAAA", a.seal("k"))

	for _, path := range []string{"/sources", "/keys"} {
		if _, page := a.get(path); strings.Contains(page, "theirs") {
			t.Fatalf("another team's source or key is listed on %s", path)
		}
	}
	res, _ := a.get("/sources/github/" + src.ID + "/repos")
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post("/sources", "/sources/github/"+src.ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post("/sources", "/sources/keys/"+key.ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	// Another team's pending flow cannot be completed from this session.
	res, _ = a.get("/sources/github/callback?code=good-code&state=pending-state")
	wantRedirect(t, res, "/sources")
	if got, _ := a.db.GitSourceByID(ctx, pending.ID); got.Ready() {
		t.Fatal("another team's GitHub App flow was completed")
	}

	// Nor can an app be pointed at them.
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings := "/apps/" + app.ID + "/settings"
	res, _ = a.post(settings, "/apps/"+app.ID+"/source", url.Values{"access": {"source:" + src.ID}, "repo": {"https://github.com/acme/shop"}, "branch": {"main"}, "build_pack": {"dockerfile"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if got, _ := a.db.AppByID(ctx, app.ID); got.GitSourceID != "" {
		t.Fatal("an app was connected to another team's GitHub App")
	}
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&#34;", `"`, "&quot;", `"`, "&amp;", "&", "&#39;", "'", "&lt;", "<", "&gt;", ">")
	return r.Replace(s)
}

// prBody is a pull request event as GitHub sends it, cut down to its shape.
func prBody(action, base, head, branch string, number int) string {
	headRepo := `{"full_name":"` + head + `"}`
	if head == "" {
		headRepo = "null"
	}
	return `{"action":"` + action + `","number":` + strconv.Itoa(number) + `,"pull_request":{"number":` + strconv.Itoa(number) + `,
		"head":{"ref":"` + branch + `","sha":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","repo":` + headRepo + `},
		"base":{"ref":"main","repo":{"full_name":"` + base + `"}}},"repository":{"full_name":"` + base + `"}}`
}

func (a *app) previewsOf(appID string) []db.App {
	a.t.Helper()
	list, err := a.db.Previews(context.Background(), appID)
	if err != nil {
		a.t.Fatal(err)
	}
	return list
}

// waitGone waits for an app to be removed by a queued job.
func (a *app) waitGone(appID string) {
	a.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.db.AppByID(context.Background(), appID); errors.Is(err, db.ErrNotFound) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatalf("app %s was not removed", appID)
}

func TestPullRequestsThroughAGitHubApp(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	team := firstTeam(t, a)

	whSecret := []byte("github-generated-secret")
	src, _ := a.db.StartGitSource(ctx, team, "musdash-test", "st")
	src.AppID, src.Slug, src.WebhookSecret, src.PrivateKey = 777, "musdash-test", a.seal(string(whSecret)), a.seal("pem")
	if err := a.db.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	web := a.newGitApp(projectID, env, "web", url.Values{"access": {"source:" + src.ID}})
	// The same repository: one without previews, one that deploys another
	// branch, one that is not connected through this App.
	quiet := a.newGitApp(projectID, env, "quiet", url.Values{"access": {"source:" + src.ID}})
	staging := a.newGitApp(projectID, env, "staging", url.Values{"access": {"source:" + src.ID}, "branch": {"develop"}})
	public := a.newGitApp(projectID, env, "public", nil)
	settings := "/apps/" + web.ID + "/settings"
	for _, app := range []db.App{web, staging, public} {
		page := "/apps/" + app.ID + "/settings"
		res, _ := a.post(page, "/apps/"+app.ID+"/previews", url.Values{"previews": {"1"}})
		wantRedirect(t, res, page+"#previews")
	}
	path := "/webhooks/github/" + src.ID
	opened := prBody("opened", "Acme/Shop", "acme/shop", "feature/login", 12)

	// Nothing unsigned or wrongly signed makes a preview, and an unknown
	// source answers like a bad signature.
	if res, _ := a.hook(path, []byte("guess"), "pull_request", "p-bad", opened); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", res.StatusCode)
	}
	if res, _ := a.hook("/webhooks/github/nosuchsource", whSecret, "pull_request", "p-x", opened); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown source: %d", res.StatusCode)
	}
	// Signed, and not something that gets a preview.
	for name, body := range map[string]string{
		"from a fork":          prBody("opened", "acme/shop", "mallory/shop", "main", 20),
		"from a deleted fork":  prBody("opened", "acme/shop", "", "main", 21),
		"of another repo":      prBody("opened", "acme/elsewhere", "acme/elsewhere", "x", 22),
		"only labelled":        prBody("labeled", "acme/shop", "acme/shop", "x", 23),
		"a branch like a flag": prBody("opened", "acme/shop", "acme/shop", "--upload-pack=x", 24),
		"closed, never opened": prBody("closed", "acme/shop", "acme/shop", "x", 25),
		"not about a PR":       `{"action":"opened","number":26,"issue":{"number":26},"repository":{"full_name":"acme/shop"}}`,
	} {
		res, answer := a.hook(path, whSecret, "pull_request", "p-"+name, body)
		if res.StatusCode != http.StatusOK || !strings.Contains(answer, `"previews":0`) {
			t.Errorf("%s: %d %s", name, res.StatusCode, answer)
		}
	}
	// A pull request body sent as a push is not a push either.
	if res, answer := a.hook(path, whSecret, "push", "p-as-push", opened); res.StatusCode != http.StatusOK || !strings.Contains(answer, `"deployments":0`) {
		t.Errorf("a pull request sent as a push: %d %s", res.StatusCode, answer)
	}
	for _, app := range []db.App{web, quiet, staging, public} {
		if n := len(a.previewsOf(app.ID)); n != 0 {
			t.Fatalf("%s has %d previews after events that give none", app.Name, n)
		}
	}
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("%d deployments were queued", n)
	}

	// Opened: exactly the one app that deploys this branch of this
	// repository through this App, with previews on, gets one.
	res, answer := a.hook(path, whSecret, "pull_request", "p-1", opened)
	if res.StatusCode != http.StatusOK || !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("opened: %d %s", res.StatusCode, answer)
	}
	previews := a.previewsOf(web.ID)
	if len(previews) != 1 || previews[0].PRNumber != 12 || previews[0].Branch != "feature/login" || previews[0].Name != "web-pr-12" {
		t.Fatalf("previews of web: %+v", previews)
	}
	for _, app := range []db.App{quiet, staging, public} {
		if n := len(a.previewsOf(app.ID)); n != 0 {
			t.Errorf("%s got a preview", app.Name)
		}
	}
	child := previews[0]
	if list, _ := a.db.ListDeployments(ctx, child.ID, 5); len(list) != 1 || list[0].Trigger != "pull request" {
		t.Fatalf("deployments of the preview: %+v", list)
	}
	// Redelivered: nothing more.
	if _, answer = a.hook(path, whSecret, "pull_request", "p-1", opened); !strings.Contains(answer, "already handled") {
		t.Fatalf("redelivery: %s", answer)
	}
	// A push to the pull request's branch is a push event too. It deploys
	// nothing: the preview follows the pull request's own events.
	if _, answer = a.hook(path, whSecret, "push", "p-push", pushBody("acme/shop", "refs/heads/feature/login")); !strings.Contains(answer, `"deployments":0`) {
		t.Fatalf("a push to the pull request's branch: %s", answer)
	}
	// New commits: the same preview, deployed again once the first is done.
	a.db.Exec(`UPDATE deployments SET status = 'failed' WHERE app_id = ?`, child.ID)
	if _, answer = a.hook(path, whSecret, "pull_request", "p-2", prBody("synchronize", "acme/shop", "acme/shop", "feature/login", 12)); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("synchronize: %s", answer)
	}
	if n := len(a.previewsOf(web.ID)); n != 1 {
		t.Fatalf("%d previews after new commits", n)
	}
	if list, _ := a.db.ListDeployments(ctx, child.ID, 5); len(list) != 2 {
		t.Fatalf("%d deployments of the preview", len(list))
	}

	// On the pages: with its parent, not among the project's apps.
	_, page := a.get(settings)
	for _, want := range []string{"Pull request previews", "/apps/" + child.ID, "feature/login", "/previews/12/delete"} {
		if !strings.Contains(page, want) {
			t.Errorf("the parent's settings lack %q", want)
		}
	}
	if _, page = a.get("/projects/" + projectID + "/e/" + env.ID); strings.Contains(page, "web-pr-12") {
		t.Error("the project page lists the preview as an app")
	}
	_, page = a.get("/apps/" + child.ID)
	if !strings.Contains(page, "The preview of pull request #12") || !strings.Contains(page, "/apps/"+web.ID) {
		t.Error("the preview's page does not say what it is")
	}
	if strings.Contains(page, "/apps/"+child.ID+"/environment") || strings.Contains(page, "/apps/"+child.ID+"/storage") {
		t.Error("the preview's page offers variables or storage of its own")
	}

	// A preview has no settings of its own to change.
	token := "/apps/" + child.ID
	for _, post := range []string{"/environment", "/settings", "/domains", "/storage", "/source", "/webhook-secret", "/deploy-token", "/tasks", "/build-server"} {
		res, _ := a.post(token, "/apps/"+child.ID+post, url.Values{"vars": {"X=1"}, "host": {"x.example.com"}, "name": {"renamed"}})
		wantRedirect(t, res, "/apps/"+child.ID)
	}
	for _, get := range []string{"/environment", "/environment/values", "/environment/edit", "/storage", "/tasks", "/webhook-secret"} {
		if res, _ := a.get("/apps/" + child.ID + get); res.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s on a preview: %d", get, res.StatusCode)
		}
	}
	after, _ := a.db.AppByID(ctx, child.ID)
	vars, _ := a.db.ListEnvVars(ctx, db.KindApp, child.ID)
	doms, _ := a.db.ListDomains(ctx, db.KindApp, child.ID)
	if after.Name != "web-pr-12" || after.WebhookSecret != "" || after.DeployTokenHash != "" || len(vars) != 0 || len(doms) != 1 {
		t.Fatalf("a preview's settings were changed: %+v, %d variables, %d domains", after, len(vars), len(doms))
	}
	// And previews cannot be switched on for a preview.
	if res, _ := a.post(token, "/apps/"+child.ID+"/previews", url.Values{"previews": {"1"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("previews of a preview: %d", res.StatusCode)
	}

	// Closed: gone.
	if _, answer = a.hook(path, whSecret, "pull_request", "p-3", prBody("closed", "acme/shop", "acme/shop", "feature/login", 12)); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("closed: %s", answer)
	}
	a.waitGone(child.ID)
	if _, err := a.db.AppByID(ctx, web.ID); err != nil {
		t.Fatalf("the parent went with its preview: %v", err)
	}

	// Removed by hand from the parent's page; another team's app is not
	// found, and neither is a pull request that has no preview.
	a.hook(path, whSecret, "pull_request", "p-4", prBody("reopened", "acme/shop", "acme/shop", "feature/login", 12))
	again := a.previewsOf(web.ID)
	if len(again) != 1 {
		t.Fatalf("reopened: %d previews", len(again))
	}
	if res, _ := a.post(settings, "/apps/"+web.ID+"/previews/99/delete", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("removing a preview that does not exist: %d", res.StatusCode)
	}
	if res, _ := a.post(settings, "/apps/"+quiet.ID+"/previews/12/delete", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("removing another app's preview through this one: %d", res.StatusCode)
	}
	res, _ = a.post(settings, "/apps/"+web.ID+"/previews/12/delete", nil)
	wantRedirect(t, res, settings+"#previews")
	a.waitGone(again[0].ID)

	// Switched off: later pull requests get none.
	res, _ = a.post(settings, "/apps/"+web.ID+"/previews", url.Values{})
	wantRedirect(t, res, settings+"#previews")
	if _, answer = a.hook(path, whSecret, "pull_request", "p-5", prBody("opened", "acme/shop", "acme/shop", "another", 13)); !strings.Contains(answer, `"previews":0`) {
		t.Fatalf("with previews off: %s", answer)
	}
}

func TestPreviewSettingsAndManualWebhook(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings := "/apps/" + app.ID + "/settings"

	// The domain previews are served under.
	for name, domain := range map[string]string{
		"not a domain":   "not a domain",
		"with a path":    "preview.example.com/x",
		"generated":      "1.2.3.4.sslip.io",
		"too long a one": strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 50) + ".example",
	} {
		res, _ := a.post(settings, "/apps/"+app.ID+"/previews", url.Values{"previews": {"1"}, "preview_domain": {domain}})
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	if got, _ := a.db.AppByID(ctx, app.ID); got.Previews || got.PreviewDomain != "" {
		t.Fatalf("a refused form was saved: %+v", got)
	}
	// A wildcard as people write it in DNS is taken for its domain.
	res, _ := a.post(settings, "/apps/"+app.ID+"/previews", url.Values{"previews": {"1"}, "preview_domain": {"*.Preview.Example.com"}})
	wantRedirect(t, res, settings+"#previews")
	if got, _ := a.db.AppByID(ctx, app.ID); !got.Previews || got.PreviewDomain != "preview.example.com" {
		t.Fatalf("saved: previews %v, domain %q", got.Previews, got.PreviewDomain)
	}
	// An app deployed from an image has no pull requests.
	image := a.newApp(projectID, env, "img", false, nil)
	if res, _ := a.post("/apps/"+image+"/settings", "/apps/"+image+"/previews", url.Values{"previews": {"1"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("previews for an image app: %d", res.StatusCode)
	}
	if _, page := a.get("/apps/" + image + "/settings"); strings.Contains(page, "Pull request previews") {
		t.Error("an image app's settings offer previews")
	}

	// A webhook added by hand that also sends pull request events.
	key, hookPath := []byte(a.webhookSecret(app.ID)), "/webhooks/git/"+app.ID

	for name, body := range map[string]string{
		"from a fork":     prBody("opened", "acme/shop", "mallory/shop", "main", 30),
		"of another repo": prBody("opened", "someone/else", "someone/else", "x", 31),
	} {
		if res, answer := a.hook(hookPath, key, "pull_request", "m-"+name, body); res.StatusCode != http.StatusOK || !strings.Contains(answer, `"previews":0`) {
			t.Errorf("%s: %d %s", name, res.StatusCode, answer)
		}
	}
	if res, _ := a.hook(hookPath, []byte("guess"), "pull_request", "m-bad", prBody("opened", "acme/shop", "acme/shop", "feature", 32)); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", res.StatusCode)
	}
	if n := len(a.previewsOf(app.ID)); n != 0 {
		t.Fatalf("%d previews after events that give none", n)
	}
	// Whatever the host calls its event header, the body says what it is.
	res, answer := a.hook(hookPath, key, "", "m-1", prBody("opened", "acme/shop", "acme/shop", "feature", 32))
	if res.StatusCode != http.StatusOK || !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("opened: %d %s", res.StatusCode, answer)
	}
	previews := a.previewsOf(app.ID)
	if len(previews) != 1 {
		t.Fatalf("%d previews", len(previews))
	}
	doms, _ := a.db.ListDomains(ctx, db.KindApp, previews[0].ID)
	if len(doms) != 1 || doms[0].Host != "pr-32-web.preview.example.com" || !doms[0].TLS {
		t.Fatalf("the preview's address: %+v", doms)
	}
	// New commits, as Gitea and Forgejo spell them.
	a.db.Exec(`UPDATE deployments SET status = 'failed' WHERE app_id = ?`, previews[0].ID)
	if _, answer = a.hook(hookPath, key, "", "m-1b", prBody("synchronized", "acme/shop", "acme/shop", "feature", 32)); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("synchronized: %s", answer)
	}
	if list, _ := a.db.ListDeployments(ctx, previews[0].ID, 5); len(list) != 2 {
		t.Fatalf("%d deployments of the preview after new commits", len(list))
	}
	// The preview has no webhook of its own: its id is not an address.
	if res, _ := a.hook("/webhooks/git/"+previews[0].ID, key, "push", "m-child", pushBody("acme/shop", "refs/heads/feature")); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a webhook to the preview's id: %d", res.StatusCode)
	}
	if _, answer = a.hook(hookPath, key, "pull_request", "m-2", prBody("closed", "acme/shop", "acme/shop", "feature", 32)); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("closed: %s", answer)
	}
	a.waitGone(previews[0].ID)

	// Deleting an app takes its previews with it.
	a.hook(hookPath, key, "pull_request", "m-3", prBody("opened", "acme/shop", "acme/shop", "feature", 33))
	left := a.previewsOf(app.ID)
	if len(left) != 1 {
		t.Fatalf("%d previews before the delete", len(left))
	}
	// Let its deployment end first: an app is not deleted under one.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := a.db.ListDeployments(ctx, left[0].ID, 1)
		if len(list) == 1 && (list[0].Status == db.DeploySuccess || list[0].Status == db.DeployFailed) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, _ = a.post(settings, "/apps/"+app.ID+"/delete", url.Values{"confirm": {"web"}})
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/projects/") {
		t.Fatalf("delete: %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	for _, id := range []string{app.ID, left[0].ID} {
		if _, err := a.db.AppByID(ctx, id); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("app %s is still there", id)
		}
	}
}
