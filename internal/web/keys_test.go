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

// The Keys page gathers every token, secret and key, shows none of them
// unasked, and lets a Member do there what a Member may do.
func TestKeysPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	image := a.newApp(projectID, env, "storefront", false, nil)
	git := a.newGitApp(projectID, env, "api", nil)
	secret := a.webhookSecret(git.ID)

	res, page := a.get("/keys")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{
		`id="tokens"`, `id="deploy-tokens"`, `id="webhooks"`, `id="ssh-keys"`,
		// Every app can have a deploy token; only one from Git has a webhook.
		`action="/apps/` + image + `/deploy-token"`, `action="/apps/` + git.ID + `/deploy-token"`,
		`action="/apps/` + git.ID + `/webhook-secret"`, "Shop / production",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the Keys page is missing %q", want)
		}
	}
	if strings.Contains(page, `action="/apps/`+image+`/webhook-secret"`) {
		t.Error("an app from an image is offered a webhook secret")
	}
	if strings.Contains(page, secret) {
		t.Fatal("the Keys page holds a webhook secret that was not asked for")
	}
	// The account page and the app's settings lead here instead of holding them.
	for _, path := range []string{"/account", "/apps/" + git.ID + "/settings", "/sources"} {
		if _, body := a.get(path); !strings.Contains(body, `href="/keys#`) || strings.Contains(body, secret) {
			t.Errorf("%s does not lead to the Keys page, or shows a secret", path)
		}
	}

	// An app without a secret has none to show.
	res, _ = a.get("/apps/" + image + "/webhook-secret")
	wantStatus(t, res, http.StatusNotFound)

	// A token made here comes back on this page, in the open dialog when refused.
	res, page = a.post("/keys", "/account/tokens", url.Values{"token_name": {""}, "token_ability": {db.AbilityRead}, "token_expires": {"30"}, "token_password": {testPassword}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "data-autoopen") || !strings.Contains(page, "Enter a name") {
		t.Fatalf("a refused token: %d", res.StatusCode)
	}

	// Another team's app answers nothing.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, host, created_at) VALUES ('theirserver', 'otherteam', 'theirs', 'ssh', '203.0.113.9', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	theirs, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-app", Source: db.SourceGit,
		RepoURL: "https://github.com/their/app", RepoName: "their/app", Branch: "main", BuildPack: "dockerfile", Port: 80, WebhookSecret: a.seal("their-webhook-secret")})
	if err != nil {
		t.Fatal(err)
	}
	if _, page = a.get("/keys"); strings.Contains(page, "secret-app") || strings.Contains(page, theirs.ID) {
		t.Fatal("another team's app is listed on the Keys page")
	}
	res, body := a.get("/apps/" + theirs.ID + "/webhook-secret")
	if res.StatusCode != http.StatusNotFound || strings.Contains(body, "their-webhook-secret") {
		t.Fatalf("another team's webhook secret: %d", res.StatusCode)
	}

	// A Member sees the page and the team's keys, makes tokens of their
	// own, and cannot add or delete an SSH key.
	a.post("/keys", "/sources/keys", url.Values{"key_name": {"shop repository"}})
	keys, _ := a.db.ListSSHKeys(ctx, firstTeam(t, a))
	if len(keys) != 1 {
		t.Fatalf("%d keys", len(keys))
	}
	m := a.newPerson("Member", db.RoleMember)
	res, page = m.get("/keys")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "shop repository") || strings.Contains(page, `id="new-key"`) || strings.Contains(page, "/sources/keys/"+keys[0].ID+"/delete") {
		t.Fatal("a Member's Keys page lacks the team's key, or offers to add or delete one")
	}
	res, _ = m.post("/sources/keys", url.Values{"key_name": {"mine"}})
	wantStatus(t, res, http.StatusForbidden)
	res, _ = m.post("/sources/keys/"+keys[0].ID+"/delete", nil)
	wantStatus(t, res, http.StatusForbidden)
	// The private half is nowhere on the page.
	if regexp.MustCompile(`PRIVATE KEY`).MatchString(page) {
		t.Fatal("a private key is on the Keys page")
	}
}
