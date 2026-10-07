package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

var newTokenRE = regexp.MustCompile(`id="new-token-value">(msd_[A-Za-z0-9_-]+)<`)

// operator is what a token had that the old "deploy" ability gave: it
// reads, starts and stops, and deploys.
var operator = []string{db.AbilityRead, db.AbilityWrite, db.AbilityDeploy}

// tokenForm is the New API token dialog, filled in: the boxes of the
// given permissions checked.
func tokenForm(name, expires string, abilities ...string) url.Values {
	form := url.Values{"token_name": {name}, "token_expires": {expires}, "token_password": {testPassword}}
	for _, p := range pages.TokenPerms {
		if slices.Contains(abilities, p.Ability) {
			form.Set(p.Field, "1")
		}
	}
	return form
}

// newToken makes an API token for the owner through the Keys page.
func (a *app) newToken(abilities ...string) string {
	a.t.Helper()
	res, body := a.post("/keys/tokens", "/account/tokens", tokenForm("ci", "never", abilities...))
	wantStatus(a.t, res, http.StatusOK)
	m := newTokenRE.FindStringSubmatch(body)
	if m == nil {
		a.t.Fatalf("no token on the page:\n%s", body)
	}
	// The page that shows the token still has one New token dialog, and
	// nothing else of its id: the button would open that instead.
	if strings.Count(body, `id="new-token"`) != 1 {
		a.t.Fatalf("%d elements with the id of the New token dialog", strings.Count(body, `id="new-token"`))
	}
	return m[1]
}

func (p person) newToken(abilities ...string) string {
	p.a.t.Helper()
	res, body := p.post("/account/tokens", tokenForm("ci", "30", abilities...))
	wantStatus(p.a.t, res, http.StatusOK)
	m := newTokenRE.FindStringSubmatch(body)
	if m == nil {
		p.a.t.Fatalf("no token on the page:\n%s", body)
	}
	return m[1]
}

// call makes an API request as a script would: no cookies, only the
// Authorization header (left out when token is "").
func (a *app) call(method, path, token string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(method, a.url+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return a.do(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, req)
}

// decode reads a JSON answer.
func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return v
}

func TestAPITokensOnTheAccountPage(t *testing.T) {
	var logs strings.Builder
	a := newAppWithLog(t, true, &logs)
	a.setup()
	ctx := context.Background()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	team := firstTeam(t, a)

	without := func(form url.Values, field string) url.Values {
		form.Del(field)
		return form
	}
	for name, form := range map[string]url.Values{
		"no name":       tokenForm("", "never", db.AbilityRead),
		"no permission": tokenForm("x", "never"),
		"bad lifetime":  tokenForm("x", "3650", db.AbilityRead),
		// A session alone does not make a token.
		"no password":    without(tokenForm("x", "never", db.AbilityRead), "token_password"),
		"wrong password": {"token_name": {"x"}, "perm_read": {"1"}, "token_expires": {"never"}, "token_password": {"not my password"}},
	} {
		res, body := a.post("/keys/tokens", "/account/tokens", form)
		if res.StatusCode != http.StatusUnprocessableEntity || newTokenRE.MatchString(body) {
			t.Errorf("%s: status %d", name, res.StatusCode)
		}
	}
	if list, _ := a.db.ListAPITokens(ctx, owner.ID, team); len(list) != 0 {
		t.Fatalf("a refused form made a token: %+v", list)
	}

	token := a.newToken(operator...)
	list, _ := a.db.ListAPITokens(ctx, owner.ID, team)
	if len(list) != 1 || list[0].Name != "ci" || list[0].Abilities != "read,write,deploy" || list[0].ExpiresAt != 0 {
		t.Fatalf("stored: %+v", list)
	}
	// Stored as its hash, shown once, never logged.
	var stored string
	a.db.QueryRow(`SELECT token_hash FROM api_tokens`).Scan(&stored)
	if stored != secret.HashToken(token) || strings.Contains(stored, token) {
		t.Fatal("the token must be stored as its hash")
	}
	_, page := a.get("/keys/tokens")
	if strings.Contains(page, token) || !strings.Contains(page, `<span class="badge">Deploy</span>`) {
		t.Fatal("the Keys page should list the token without showing it")
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("the token was written to the log")
	}
	if res, _ := a.call(http.MethodGet, "/api/v1/me", token); res.StatusCode != http.StatusOK {
		t.Fatalf("the new token does not work: %d", res.StatusCode)
	}

	// Somebody else cannot revoke it, or see it.
	mem := a.newPerson("Member", db.RoleMember)
	res, _ := mem.post("/account/tokens/"+list[0].ID+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	if _, page := mem.get("/keys/tokens"); strings.Contains(page, list[0].ID) {
		t.Fatal("a Member's Keys page shows the Owner's token")
	}
	// Revoked, it stops working at once.
	res, _ = a.post("/keys/tokens", "/account/tokens/"+list[0].ID+"/delete", nil)
	wantRedirect(t, res, "/keys/tokens")
	if res, _ := a.call(http.MethodGet, "/api/v1/me", token); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", res.StatusCode)
	}

	// No more than a person may have.
	for i := 0; i < db.MaxAPITokens; i++ {
		a.newToken(db.AbilityRead)
	}
	res, _ = a.post("/keys/tokens", "/account/tokens", tokenForm("one more", "never", db.AbilityRead))
	wantStatus(t, res, http.StatusUnprocessableEntity)
}

func TestAPIRefusesWhatIsNotALiveToken(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	good := a.newToken(operator...)
	expired := a.newToken(db.AbilityRead)
	a.db.Exec(`UPDATE api_tokens SET expires_at = ? WHERE token_hash = ?`, time.Now().Unix()-1, secret.HashToken(expired))
	mem := a.newPerson("Member", db.RoleMember)
	removed := mem.newToken(db.AbilityRead)
	if res, _ := a.call(http.MethodGet, "/api/v1/me", removed); res.StatusCode != http.StatusOK {
		t.Fatal("a Member's token should work while they are a member")
	}
	res, _ := a.post("/team", "/team/members/"+mem.user.ID+"/delete", url.Values{"confirm": {"member@example.com"}})
	wantRedirect(t, res, "/team")

	// Every one of these is refused the same way.
	var first string
	for name, token := range map[string]string{
		"none":             "",
		"not a token":      "hello",
		"right shape":      apiTokenPrefix + secret.RandomToken(32),
		"too short":        apiTokenPrefix + "abc",
		"expired":          expired,
		"a removed person": removed,
		"a prefix of one":  good[:len(good)-1],
		"with more after":  good + "x",
	} {
		res, body := a.call(http.MethodGet, "/api/v1/apps", token)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, res.StatusCode)
		}
		if first == "" {
			first = body
		}
		if body != first {
			t.Errorf("%s is refused differently:\n%s\n%s", name, body, first)
		}
	}
	// Other ways of sending the token do not count.
	req, _ := http.NewRequest(http.MethodGet, a.url+"/api/v1/apps?token="+good, nil)
	req.Header.Set("Authorization", "Basic "+good)
	if res, _ := a.do(http.DefaultClient, req); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a token outside the Bearer header was accepted: %d", res.StatusCode)
	}

	// A session is not a token: the signed-in browser gets nothing from
	// the API, with or without its CSRF token, and changes nothing.
	projectID, env := a.project("Shop")
	id := a.newApp(projectID, env, "web", false, nil)
	res, _ = a.get("/api/v1/apps")
	wantStatus(t, res, http.StatusUnauthorized)
	res, _ = a.post("/account", "/api/v1/apps/"+id+"/deploy", nil)
	wantStatus(t, res, http.StatusUnauthorized)
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("a session deployed through the API: %d jobs", n)
	}
	// And a token is not a session: pages send it to sign in.
	for _, path := range []string{"/", a.appPath(id), "/team", "/settings"} {
		res, _ = a.call(http.MethodGet, path, good)
		wantRedirect(t, res, "/login")
	}
	req, _ = http.NewRequest(http.MethodPost, a.url+a.appPath(id)+"/deploy", nil)
	req.Header.Set("Authorization", "Bearer "+good)
	if res, _ := a.do(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, req); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("a token was accepted by a page: %d", res.StatusCode)
	}
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("a token deployed through a page: %d jobs", n)
	}
	_ = ctx

	// An unknown route under /api/ answers in JSON.
	res, body := a.call(http.MethodGet, "/api/v1/nosuchthing", good)
	wantStatus(t, res, http.StatusNotFound)
	if decode[map[string]string](t, body)["error"] == "" {
		t.Fatalf("not a JSON error: %s", body)
	}
}

// The phase's done-when: the API can trigger a deploy.
func TestAPIReadsAndOperates(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	database := a.newDatabase(projectID, env, "postgres", "main", nil)
	a.waitDatabase(database.ID)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "blog", nil)
	res, _ := a.post(a.appPath(appID)+"/settings", a.appPath(appID)+"/tags", url.Values{"tags": {"nightly"}})
	wantRedirect(t, res, a.appPath(appID)+"/settings#tags")
	read, deploy := a.newToken(db.AbilityRead), a.newToken(operator...)

	// Reading.
	res, body := a.call(http.MethodGet, "/api/v1/me", read)
	wantStatus(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); ct != "application/json" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %q %q", ct, res.Header.Get("Cache-Control"))
	}
	me := decode[map[string]any](t, body)
	if me["role"] != db.RoleOwner || me["user"].(map[string]any)["email"] != testEmail || fmt.Sprint(me["token"].(map[string]any)["abilities"]) != "[read]" {
		t.Fatalf("me: %v", me)
	}
	_, body = a.call(http.MethodGet, "/api/v1/projects", read)
	projects := decode[[]apiProject](t, body)
	if len(projects) != 1 || projects[0].ID != projectID || len(projects[0].Environments) != 1 || projects[0].Environments[0].ID != env.ID {
		t.Fatalf("projects: %+v", projects)
	}
	_, body = a.call(http.MethodGet, "/api/v1/servers", read)
	if servers := decode[[]apiServer](t, body); len(servers) != 1 || servers[0].Kind != db.ServerLocal {
		t.Fatalf("servers: %+v", servers)
	}
	_, body = a.call(http.MethodGet, "/api/v1/apps", read)
	if apps := decode[[]apiApp](t, body); len(apps) != 1 || apps[0].ID != appID || apps[0].Status != db.AppRunning {
		t.Fatalf("apps: %+v", apps)
	}
	_, body = a.call(http.MethodGet, "/api/v1/apps?tag=nightly", read)
	if apps := decode[[]apiApp](t, body); len(apps) != 1 {
		t.Fatalf("apps by tag: %+v", apps)
	}
	_, body = a.call(http.MethodGet, "/api/v1/apps?tag=other", read)
	if apps := decode[[]apiApp](t, body); len(apps) != 0 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("apps by a tag nothing has: %s", body)
	}
	_, body = a.call(http.MethodGet, "/api/v1/apps/"+appID, read)
	one := decode[apiApp](t, body)
	if one.Name != "web" || len(one.Tags) != 1 || one.Tags[0] != "nightly" || one.Image != "nginx:alpine" {
		t.Fatalf("app: %+v", one)
	}
	_, body = a.call(http.MethodGet, "/api/v1/tags", read)
	if tags := decode[[]apiTag](t, body); len(tags) != 1 || tags[0].Tag != "nightly" || tags[0].Apps != 1 {
		t.Fatalf("tags: %+v", tags)
	}
	_, body = a.call(http.MethodGet, "/api/v1/databases", read)
	if list := decode[[]apiDatabase](t, body); len(list) != 1 || list[0].ID != database.ID || list[0].Engine != "postgres" {
		t.Fatalf("databases: %+v", list)
	}
	res, _ = a.call(http.MethodGet, "/api/v1/databases/"+database.ID, read)
	wantStatus(t, res, http.StatusOK)
	_, body = a.call(http.MethodGet, "/api/v1/services", read)
	if list := decode[[]apiService](t, body); len(list) != 1 || list[0].ID != svc.ID {
		t.Fatalf("services: %+v", list)
	}
	res, _ = a.call(http.MethodGet, "/api/v1/services/"+svc.ID, read)
	wantStatus(t, res, http.StatusOK)
	_, body = a.call(http.MethodGet, "/api/v1/apps/"+appID+"/deployments?limit=5", read)
	deployments := decode[[]apiDeployment](t, body)
	if len(deployments) != 1 || deployments[0].Status != db.DeploySuccess {
		t.Fatalf("deployments: %+v", deployments)
	}
	res, _ = a.call(http.MethodGet, "/api/v1/apps/"+appID+"/deployments?limit=0", read)
	wantStatus(t, res, http.StatusBadRequest)

	// A token that may only read starts and stops nothing.
	operate := []string{
		"/api/v1/apps/" + appID + "/deploy", "/api/v1/apps/" + appID + "/stop",
		"/api/v1/databases/" + database.ID + "/start", "/api/v1/databases/" + database.ID + "/stop",
		"/api/v1/services/" + svc.ID + "/deploy", "/api/v1/services/" + svc.ID + "/stop",
		"/api/v1/deploy?uuid=" + appID, "/api/v1/deploy?tag=nightly",
	}
	jobs := a.deployJobs()
	for _, path := range operate {
		res, _ := a.call(http.MethodPost, path, read)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s with a read token: %d", path, res.StatusCode)
		}
	}
	if a.deployJobs() != jobs {
		t.Fatal("a read token queued a deployment")
	}
	if app, _ := a.db.AppByID(context.Background(), appID); app.Status != db.AppRunning {
		t.Fatalf("a read token stopped the app: %s", app.Status)
	}

	// A token that may deploy: the app is deployed, and the deployment
	// can be followed.
	res, body = a.call(http.MethodPost, "/api/v1/apps/"+appID+"/deploy", deploy)
	wantStatus(t, res, http.StatusAccepted)
	queued := decode[apiQueued](t, body)
	if queued.Kind != "app" || queued.ID != appID || queued.DeploymentID == "" || queued.DeploymentID == deployments[0].ID {
		t.Fatalf("queued: %+v", queued)
	}
	if dep := a.waitDeployed(appID); dep.ID != queued.DeploymentID || dep.Trigger != "api" || dep.Status != db.DeploySuccess {
		t.Fatalf("the deployment the API started: %+v", dep)
	}
	_, body = a.call(http.MethodGet, "/api/v1/deployments/"+queued.DeploymentID, read)
	if dep := decode[apiDeployment](t, body); dep.Status != db.DeploySuccess || dep.AppID != appID {
		t.Fatalf("deployment: %+v", dep)
	}

	// Stopping and starting.
	res, _ = a.call(http.MethodPost, "/api/v1/apps/"+appID+"/stop", deploy)
	wantStatus(t, res, http.StatusOK)
	if app, _ := a.db.AppByID(context.Background(), appID); app.Status != db.AppStopped {
		t.Fatalf("after stop: %s", app.Status)
	}
	res, _ = a.call(http.MethodPost, "/api/v1/databases/"+database.ID+"/stop", deploy)
	wantStatus(t, res, http.StatusOK)
	res, _ = a.call(http.MethodPost, "/api/v1/databases/"+database.ID+"/start", deploy)
	wantStatus(t, res, http.StatusAccepted)
	if got := a.waitDatabase(database.ID); got.Status != db.AppRunning {
		t.Fatalf("database after start: %s", got.Status)
	}
	res, _ = a.call(http.MethodPost, "/api/v1/services/"+svc.ID+"/deploy", deploy)
	wantStatus(t, res, http.StatusAccepted)
	if got := a.waitService(svc.ID); got.Status != db.AppRunning {
		t.Fatalf("service after deploy: %s: %s", got.Status, got.LastError)
	}
	res, _ = a.call(http.MethodPost, "/api/v1/services/"+svc.ID+"/stop", deploy)
	wantStatus(t, res, http.StatusOK)

	// Unknown ids, and methods a route does not have.
	for _, path := range []string{"/api/v1/apps/nosuchid", "/api/v1/databases/nosuchid", "/api/v1/services/nosuchid", "/api/v1/deployments/nosuchid", "/api/v1/apps/nosuchid/deployments"} {
		res, _ := a.call(http.MethodGet, path, read)
		wantStatus(t, res, http.StatusNotFound)
	}
	res, _ = a.call(http.MethodPost, "/api/v1/apps/nosuchid/deploy", deploy)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.call(http.MethodDelete, "/api/v1/apps/"+appID, deploy)
	if res.StatusCode == http.StatusOK {
		t.Fatal("DELETE was accepted")
	}
}

func TestAPIDeployEndpoint(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", false, nil)
	api := a.newApp(projectID, env, "api", false, nil)
	worker := a.newApp(projectID, env, "worker", false, nil)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "blog", nil)
	a.db.SetTags(ctx, team, db.KindApp, api, []string{"nightly"})
	a.db.SetTags(ctx, team, db.KindService, svc.ID, []string{"nightly"})
	token := a.newToken(operator...)

	// What a call may not be.
	for name, query := range map[string]string{
		"nothing named": "",
		"a bad tag":     "tag=not/a/tag",
		"too many ids":  "uuid=" + strings.Repeat("x,", maxDeployIDs) + "y",
	} {
		res, _ := a.call(http.MethodPost, "/api/v1/deploy?"+query, token)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	// One unknown id, and nothing is deployed.
	res, _ := a.call(http.MethodPost, "/api/v1/deploy?uuid="+web+",nosuchid", token)
	wantStatus(t, res, http.StatusNotFound)
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("%d deployments queued by a refused call", n)
	}

	// By id and by tag together; a resource named both ways is deployed once.
	res, body := a.call(http.MethodPost, "/api/v1/deploy?uuid="+web+","+api+"&tag=nightly", token)
	wantStatus(t, res, http.StatusAccepted)
	answer := decode[map[string][]apiQueued](t, body)["deployments"]
	kinds := map[string]string{}
	for _, q := range answer {
		kinds[q.ID] = q.Kind
	}
	if len(answer) != 3 || kinds[web] != "app" || kinds[api] != "app" || kinds[svc.ID] != "service" {
		t.Fatalf("answer: %+v", answer)
	}
	for _, id := range []string{web, api} {
		if dep := a.waitDeployed(id); dep.Status != db.DeploySuccess || dep.Trigger != "api" {
			t.Fatalf("%s: %+v", id, dep)
		}
	}
	if got := a.waitService(svc.ID); got.Status != db.AppRunning {
		t.Fatalf("the service: %s: %s", got.Status, got.LastError)
	}
	if list, _ := a.db.ListDeployments(ctx, worker, 5); len(list) != 0 {
		t.Fatal("an app that was not named was deployed")
	}

	// Another team's app, by id or by tag, is not there.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, host, created_at) VALUES ('theirserver', 'otherteam', 'theirs', 'ssh', '203.0.113.9', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	theirs, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	a.db.SetTags(ctx, "otherteam", db.KindApp, theirs.ID, []string{"theirtag"})
	res, _ = a.call(http.MethodPost, "/api/v1/deploy?uuid="+theirs.ID, token)
	wantStatus(t, res, http.StatusNotFound)
	res, body = a.call(http.MethodPost, "/api/v1/deploy?tag=theirtag", token)
	wantStatus(t, res, http.StatusAccepted)
	if got := decode[map[string][]apiQueued](t, body)["deployments"]; len(got) != 0 {
		t.Fatalf("another team's tag deployed something: %+v", got)
	}
	for _, path := range []string{"/api/v1/apps/" + theirs.ID, "/api/v1/apps/" + theirs.ID + "/deployments"} {
		res, body := a.call(http.MethodGet, path, token)
		wantStatus(t, res, http.StatusNotFound)
		if strings.Contains(body, "secret-app") {
			t.Fatal("another team's app leaked")
		}
	}
	_, body = a.call(http.MethodGet, "/api/v1/apps", token)
	if strings.Contains(body, "secret-app") || strings.Contains(body, theirs.ID) {
		t.Fatal("another team's app is listed")
	}
	if list, _ := a.db.ListDeployments(ctx, theirs.ID, 5); len(list) != 0 {
		t.Fatal("another team's app was deployed")
	}
}

func TestAPINeverReturnsSecrets(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	gitApp := a.newGitApp(projectID, env, "web", nil)
	res, _ := a.post(a.appPath(gitApp.ID)+"/environment", a.appPath(gitApp.ID)+"/environment", url.Values{"vars": {"SECRET=plain-env-value-1"}, "build_vars": {"BUILD_SECRET=plain-env-value-2"}})
	wantRedirect(t, res, a.appPath(gitApp.ID)+"/environment")
	res, _ = a.post(a.appPath(gitApp.ID)+"/settings", a.appPath(gitApp.ID)+"/webhook-secret", nil)
	res, _ = a.post(a.appPath(gitApp.ID)+"/settings", a.appPath(gitApp.ID)+"/deploy-token", nil)
	database := a.newDatabase(projectID, env, "postgres", "main", nil)
	a.waitDatabase(database.ID)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "blog", url.Values{"variables": {"API_KEY=plain-service-value"}})
	a.db.ReplaceSharedVars(ctx, team, db.ScopeTeam, team, []db.EnvVar{{Key: "T", Value: a.seal("plain-shared-value")}})
	token := a.newToken(db.AbilityRead)

	// Everything in the database that must stay there.
	var forbidden []string
	add := func(query string, args ...any) {
		rows, err := a.db.Query(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			if len(v) >= 8 {
				forbidden = append(forbidden, v)
			}
		}
	}
	add(`SELECT password_hash FROM users`)
	add(`SELECT token_hash FROM api_tokens`)
	add(`SELECT token_hash FROM sessions`)
	add(`SELECT csrf_token FROM sessions`)
	add(`SELECT value FROM env_vars`)
	add(`SELECT value FROM shared_vars`)
	add(`SELECT webhook_secret FROM apps`)
	add(`SELECT deploy_token_hash FROM apps`)
	add(`SELECT password FROM databases`)
	add(`SELECT variables FROM services`)
	if len(forbidden) < 9 {
		t.Fatalf("the test's own fixtures are thin: %d values", len(forbidden))
	}
	password, _ := a.server.Box.OpenString(a.waitDatabase(database.ID).Password)
	forbidden = append(forbidden, "plain-env-value-1", "plain-env-value-2", "plain-service-value", "plain-shared-value", password, testPassword)

	// A deployment that failed, so its free-text error is read too. It
	// fails on a shared variable that does not exist, with others set.
	res, _ = a.post(a.appPath(gitApp.ID)+"/environment", a.appPath(gitApp.ID)+"/environment", url.Values{"vars": {"SECRET=plain-env-value-1\nA={{team.T}}\nB={{team.NOPE}}"}, "build_vars": {"BUILD_SECRET=plain-env-value-2"}})
	wantRedirect(t, res, a.appPath(gitApp.ID)+"/environment")
	res, _ = a.post(a.appPath(gitApp.ID), a.appPath(gitApp.ID)+"/deploy", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy: %d", res.StatusCode)
	}
	failed := a.waitDeployed(gitApp.ID)
	if failed.Status != db.DeployFailed || failed.Error == "" {
		t.Fatalf("the fixture deployment should have failed: %+v", failed)
	}
	paths := []string{
		"/api/v1/me", "/api/v1/servers", "/api/v1/projects", "/api/v1/tags", "/api/v1/apps", "/api/v1/apps/" + gitApp.ID,
		"/api/v1/apps/" + gitApp.ID + "/deployments", "/api/v1/apps/" + gitApp.ID + "/envs", "/api/v1/deployments/" + failed.ID,
		"/api/v1/databases", "/api/v1/databases/" + database.ID, "/api/v1/services", "/api/v1/services/" + svc.ID,
	}
	// The walk must cover every GET route of the API.
	gets := 0
	for _, route := range a.server.routes {
		if strings.HasPrefix(route.pattern, "GET /api/v1/") {
			gets++
		}
	}
	if gets != len(paths) {
		t.Fatalf("the API has %d GET routes and this test reads %d: add the new one here", gets, len(paths))
	}
	for _, path := range paths {
		res, body := a.call(http.MethodGet, path, token)
		wantStatus(t, res, http.StatusOK)
		for _, v := range forbidden {
			if strings.Contains(body, v) {
				t.Errorf("%s returns a value that must stay in the database: %q", path, v)
			}
		}
		// A field, not a value: a variable may well be called SECRET, and
		// its name is what the envs route is there to list.
		if m := secretFieldRE.FindString(strings.ToLower(body)); m != "" {
			t.Errorf("%s has a field named like a secret: %s", path, m)
		}
	}

	// A token that reads sensitive data is the one exception, and it is
	// given exactly two things: an app's variables with their values, and a
	// database's password.
	sensitive := a.newToken(db.AbilitySensitive)
	_, body := a.call(http.MethodGet, "/api/v1/apps/"+gitApp.ID+"/envs", sensitive)
	envs := map[string]apiEnv{}
	for _, e := range decode[[]apiEnv](t, body) {
		envs[e.Key] = e
	}
	if e := envs["SECRET"]; e.Value == nil || *e.Value != "plain-env-value-1" || e.Build {
		t.Fatalf("SECRET for a sensitive token: %s", body)
	}
	if e := envs["BUILD_SECRET"]; e.Value == nil || *e.Value != "plain-env-value-2" || !e.Build {
		t.Fatalf("BUILD_SECRET for a sensitive token: %s", body)
	}
	// What names a shared variable is given as it is stored: the name.
	if e := envs["A"]; e.Value == nil || *e.Value != "{{team.T}}" || strings.Contains(body, "plain-shared-value") {
		t.Fatalf("a shared variable's value reached the API: %s", body)
	}
	_, body = a.call(http.MethodGet, "/api/v1/databases/"+database.ID, sensitive)
	if got := decode[apiDatabase](t, body); got.Password != password {
		t.Fatalf("the database's password for a sensitive token: %s", body)
	}
	// The same routes for a token that only reads: the names, no value.
	_, body = a.call(http.MethodGet, "/api/v1/apps/"+gitApp.ID+"/envs", token)
	plain := decode[[]apiEnv](t, body)
	if len(plain) != len(envs) || strings.Contains(body, `"value"`) {
		t.Fatalf("envs for a read token: %s", body)
	}
	for _, e := range plain {
		if e.Value != nil || e.Key == "" {
			t.Fatalf("envs for a read token: %s", body)
		}
	}
	// Everywhere else a sensitive token gets what a read token gets.
	for _, path := range paths {
		if strings.HasSuffix(path, "/envs") || path == "/api/v1/databases/"+database.ID {
			continue
		}
		res, body := a.call(http.MethodGet, path, sensitive)
		wantStatus(t, res, http.StatusOK)
		for _, v := range forbidden {
			if strings.Contains(body, v) {
				t.Errorf("%s returns to a sensitive token what must stay in the database: %q", path, v)
			}
		}
	}
}

var secretFieldRE = regexp.MustCompile(`"(password|secret|hash|variables|private)[^"]*"\s*:`)

func TestAPIRateLimits(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	first, second := a.newToken(db.AbilityRead), a.newToken(db.AbilityRead)

	// A token has its own allowance.
	for i := 0; i < apiPerMinute; i++ {
		if res, _ := a.call(http.MethodGet, "/api/v1/tags", first); res.StatusCode != http.StatusOK {
			t.Fatalf("call %d: %d", i+1, res.StatusCode)
		}
	}
	res, _ := a.call(http.MethodGet, "/api/v1/tags", first)
	wantStatus(t, res, http.StatusTooManyRequests)
	if res.Header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	res, _ = a.call(http.MethodGet, "/api/v1/tags", second)
	wantStatus(t, res, http.StatusOK)

	// An address has one too, counted before any token is looked at.
	for i := 0; i < apiPerMinuteByAddress; i++ {
		a.call(http.MethodGet, "/api/v1/tags", "")
	}
	res, _ = a.call(http.MethodGet, "/api/v1/tags", second)
	wantStatus(t, res, http.StatusTooManyRequests)
}

func TestATokenActsAsItsPersonNow(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ad := a.newPerson("Admin", db.RoleAdmin)
	token := ad.newToken(db.AbilityRead)
	role := func() string {
		_, body := a.call(http.MethodGet, "/api/v1/me", token)
		r, _ := decode[map[string]any](t, body)["role"].(string)
		return r
	}
	if role() != db.RoleAdmin {
		t.Fatalf("role %q", role())
	}
	res, _ := a.post("/team", "/team/members/"+ad.user.ID+"/role", url.Values{"role": {db.RoleMember}})
	wantRedirect(t, res, "/team")
	if role() != db.RoleMember {
		t.Fatalf("after the role changed: %q", role())
	}
}

// Changing or resetting a password, and turning the second step on, end
// the API tokens with the sessions: a token asks for neither.
func TestAPITokensEndWithThePassword(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ad := a.newPerson("Admin", db.RoleAdmin)
	mem := a.newPerson("Member", db.RoleMember)
	works := func(token string) bool {
		res, _ := a.call(http.MethodGet, "/api/v1/me", token)
		return res.StatusCode == http.StatusOK
	}

	// A reset through a link.
	token := mem.newToken(operator...)
	res, body := ad.post("/team/members/"+mem.user.ID+"/reset", nil)
	wantStatus(t, res, http.StatusOK)
	link := linkRE.FindStringSubmatch(html.UnescapeString(body))[3]
	fresh := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/reset/"+link, nil)
	_, page := a.do(fresh, req)
	if !works(token) {
		t.Fatal("the token should work until the password is changed")
	}
	res, _ = a.postRaw(fresh, "/reset/"+link, url.Values{"_csrf": {csrfRE.FindStringSubmatch(page)[1]}, "password": {"a brand new password"}}, nil)
	wantRedirect(t, res, "/login")
	if works(token) {
		t.Fatal("an API token outlived a password reset")
	}

	// A change on the Account page.
	token = ad.newToken(db.AbilityRead)
	res, _ = ad.post("/account/password", url.Values{"current": {testPassword}, "password": {"another long password"}})
	wantRedirect(t, res, "/account")
	if works(token) {
		t.Fatal("an API token outlived a password change")
	}

	// Turning the second step on.
	token = a.newToken(db.AbilityRead)
	a.turnOnTwoStep()
	if works(token) {
		t.Fatal("an API token outlived turning the second step on")
	}
}

// Each permission opens its own routes and no other's: reading does not
// deploy, deploying does not read, and writing is neither. Root opens all.
func TestAPITokenPermissions(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	database := a.newDatabase(projectID, env, "postgres", "main", nil)
	a.waitDatabase(database.ID)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "blog", nil)
	a.waitService(svc.ID)

	type call struct{ method, path string }
	routes := map[string][]call{
		db.AbilityRead: {
			{http.MethodGet, "/api/v1/me"}, {http.MethodGet, "/api/v1/apps"}, {http.MethodGet, "/api/v1/apps/" + appID},
			{http.MethodGet, "/api/v1/apps/" + appID + "/envs"}, {http.MethodGet, "/api/v1/databases/" + database.ID},
			{http.MethodGet, "/api/v1/services"},
		},
		db.AbilityDeploy: {
			{http.MethodPost, "/api/v1/apps/" + appID + "/deploy"}, {http.MethodPost, "/api/v1/services/" + svc.ID + "/deploy"},
			{http.MethodPost, "/api/v1/deploy?uuid=" + appID},
		},
		db.AbilityWrite: {
			{http.MethodPost, "/api/v1/apps/" + appID + "/stop"}, {http.MethodPost, "/api/v1/databases/" + database.ID + "/stop"},
			{http.MethodPost, "/api/v1/databases/" + database.ID + "/start"}, {http.MethodPost, "/api/v1/services/" + svc.ID + "/stop"},
		},
	}
	// Every route of the API is in the table above or is one of the lists
	// it has an example of: a new route has to be given a permission here.
	for _, tc := range []struct {
		name      string
		abilities []string
		opens     []string
	}{
		{"read", []string{db.AbilityRead}, []string{db.AbilityRead}},
		{"deploy", []string{db.AbilityDeploy}, []string{db.AbilityDeploy}},
		{"write", []string{db.AbilityWrite}, []string{db.AbilityWrite}},
		{"read sensitive", []string{db.AbilitySensitive}, []string{db.AbilityRead}},
		{"read and deploy", []string{db.AbilityRead, db.AbilityDeploy}, []string{db.AbilityRead, db.AbilityDeploy}},
		// Root with other boxes checked is root.
		{"root", []string{db.AbilityRoot, db.AbilityRead}, []string{db.AbilityRead, db.AbilityDeploy, db.AbilityWrite}},
	} {
		token := a.newToken(tc.abilities...)
		for needs, calls := range routes {
			for _, c := range calls {
				res, body := a.call(c.method, c.path, token)
				refused := res.StatusCode == http.StatusForbidden
				if want := !slices.Contains(tc.opens, needs); refused != want {
					t.Errorf("a %s token at %s %s: status %d, refused=%v, want %v", tc.name, c.method, c.path, res.StatusCode, refused, want)
				}
				// The refusal names what is missing, as the dialog calls it.
				if refused && !strings.Contains(body, apiNeeds[needs]+" permission") {
					t.Errorf("a %s token at %s: the refusal does not name the %s permission: %s", tc.name, c.path, apiNeeds[needs], body)
				}
				// Let through, a call may still find the app busy with the
				// deployment an earlier one started: that is not a refusal.
				if !refused && res.StatusCode >= 300 && res.StatusCode != http.StatusConflict {
					t.Errorf("a %s token at %s %s: status %d: %s", tc.name, c.method, c.path, res.StatusCode, body)
				}
			}
		}
		// What the token says of itself.
		_, body := a.call(http.MethodGet, "/api/v1/me", token)
		if slices.Contains(tc.opens, db.AbilityRead) {
			want, _ := db.NormalAbilities(tc.abilities)
			got := decode[map[string]any](t, body)["token"].(map[string]any)["abilities"]
			if fmt.Sprint(got) != "["+strings.ReplaceAll(want, ",", " ")+"]" {
				t.Errorf("a %s token says it has %v, want %s", tc.name, got, want)
			}
		}
	}

	// The route table agrees: every API route asks for a permission this
	// test has an example of.
	seen := 0
	for _, route := range a.server.routes {
		if strings.Contains(route.pattern, " /api/v1/") {
			seen++
		}
	}
	if seen != 20 {
		t.Fatalf("the API has %d routes: give the new one a permission, and this test an example of it", seen)
	}
}
