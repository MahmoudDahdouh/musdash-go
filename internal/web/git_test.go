package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// gitForm is a valid New app form for a public repository.
func gitForm(env db.Environment, name string) url.Values {
	return url.Values{
		"env": {env.ID}, "source": {"git"}, "name": {name}, "port": {"3000"},
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
	page := "/projects/" + projectID + "/apps/new?env=" + env.ID + "&source=git"
	res, body := a.post(page, "/projects/"+projectID+"/apps", form)
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

	_, page := a.get("/projects/" + projectID + "/apps/new?env=" + env.ID + "&source=git")
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
		{url.Values{"build_pack": {"nixpacks"}}, "Choose how the app is built"},
		{url.Values{"base_dir": {"../../etc"}}, "Enter a path inside the repository"},
		{url.Values{"dockerfile_path": {"/etc/passwd"}}, "Enter a path inside the repository"},
		{url.Values{"publish_dir": {"a;b"}}, "Enter a path inside the repository"},
	}
	page := "/projects/" + projectID + "/apps/new?env=" + env.ID + "&source=git"
	for _, c := range cases {
		form := gitForm(env, "web")
		for k, v := range c.field {
			form[k] = v
		}
		res, body := a.post(page, "/projects/"+projectID+"/apps", form)
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
	if !strings.Contains(body, "NPM_TOKEN=s3cret") || !strings.Contains(body, "PORT=3000") {
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

	res, _ := a.post(settings, "/apps/"+app.ID+"/webhook-secret", nil)
	wantRedirect(t, res, settings+"#triggers")
	_, page := a.get(settings)
	m := regexp.MustCompile(`data-copy="([0-9a-f]{48})"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "/webhooks/git/"+app.ID) {
		t.Fatal("the settings page does not show the webhook address and secret")
	}
	key := []byte(m[1])

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
	res, page = a.post(settings, "/apps/"+app.ID+"/deploy-token", nil)
	wantStatus(t, res, http.StatusOK)
	tm := regexp.MustCompile(`mdt_[A-Za-z0-9_-]{40,}`).FindString(page)
	if tm == "" {
		t.Fatal("the new deploy token is not shown")
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

	res, _ = a.post(settings, "/apps/"+app.ID+"/deploy-token", url.Values{"revoke": {"1"}})
	wantRedirect(t, res, settings+"#triggers")
	if res, _ := api(app.ID, tm); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("a revoked token still works")
	}
}

func TestPushBurstQueuesOneDeployment(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings := "/apps/" + app.ID + "/settings"
	a.post(settings, "/apps/"+app.ID+"/webhook-secret", nil)
	_, page := a.get(settings)
	key := []byte(regexp.MustCompile(`data-copy="([0-9a-f]{48})"`).FindStringSubmatch(page)[1])

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
	settings := "/apps/" + app.ID + "/settings"
	a.post(settings, "/apps/"+app.ID+"/webhook-secret", nil)
	_, page := a.get(settings)
	key := []byte(regexp.MustCompile(`data-copy="([0-9a-f]{48})"`).FindStringSubmatch(page)[1])
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
	_, page = a.post(settings, "/apps/"+app.ID+"/deploy-token", nil)
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

	res, body := a.post("/sources", "/sources/keys", url.Values{"key_name": {""}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Enter a name") {
		t.Fatalf("empty name: %d", res.StatusCode)
	}
	res, body = a.post("/sources", "/sources/keys", url.Values{"key_name": {"shop repository"}})
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
	res, _ = a.post("/sources", "/sources/keys/"+keys[0].ID+"/delete", nil)
	wantRedirect(t, res, "/sources")
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

	_, page := a.get("/sources")
	if strings.Contains(page, "theirs") {
		t.Fatal("another team's source or key is listed")
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
