package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

var onceRE = regexp.MustCompile(`name="_once" value="([^"]+)"`)

// onceOf reads the value a form of the page carries so that it is acted on
// once: the one of the form that posts to action.
func (a *app) onceOf(page, action string) string {
	a.t.Helper()
	_, body := a.get(page)
	at := strings.Index(body, `action="`+action+`"`)
	if at < 0 {
		a.t.Fatalf("%s has no form that posts to %s", page, action)
	}
	m := onceRE.FindStringSubmatch(body[at:])
	if m == nil {
		a.t.Fatalf("the form of %s that posts to %s has no _once field", page, action)
	}
	return m[1]
}

// The Keys page is two tabs, each one table: the private keys, and every
// token. It shows no secret unasked, lists nothing of another team, and
// lets a Member do there what a Member may do.
func TestKeysPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	image := a.newApp(projectID, env, "storefront", false, nil)
	git := a.newGitApp(projectID, env, "api", nil)

	// Private Keys is the first tab, API Tokens the second.
	res, page := a.get("/keys")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{`href="/keys"`, `href="/keys/tokens"`, "Private Keys", "API Tokens", `id="ssh-keys"`, `id="new-key"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the Private Keys tab is missing %q", want)
		}
	}
	if strings.Contains(page, `id="new-token"`) || strings.Contains(page, `id="tokens"`) {
		t.Error("the Private Keys tab holds the tokens as well")
	}
	if !strings.Contains(page, `class="tab" href="/keys" aria-current="page"`) || strings.Contains(page, `class="tab" href="/keys/tokens" aria-current="page"`) {
		t.Error("the Private Keys tab is not the one marked as open")
	}

	// With nothing made, the tokens tab is empty and offers all three.
	res, page = a.get("/keys/tokens")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, `class="tab" href="/keys/tokens" aria-current="page"`) {
		t.Error("the API Tokens tab is not the one marked as open")
	}
	for _, want := range []string{
		`id="tokens"`, "No tokens yet", `id="new-token"`, `id="new-deploy-token"`, `id="new-hook-secret"`,
		// The five permissions, as checkboxes, Read checked.
		`id="token_perm_read" name="perm_read" value="1" checked`, `name="perm_write"`, `name="perm_deploy"`, `name="perm_sensitive"`,
		// Root is the box that stands for the others.
		`name="perm_root" value="1" data-alone="token-perms"`, `id="token-perms"`,
		// The lifetimes.
		`data-value="7"`, `data-value="30"`, `data-value="60"`, `data-value="90"`, `data-value="365"`, `data-value="never"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the API Tokens tab is missing %q", want)
		}
	}
	if strings.Contains(page, `id="ssh-keys"`) || strings.Contains(page, "<table") {
		t.Error("the API Tokens tab holds the keys, or a table with nothing in it")
	}
	// Every app can have a deploy token; only one from Git a webhook.
	deployDialog := page[strings.Index(page, `id="new-deploy-token"`):strings.Index(page, `id="new-hook-secret"`)]
	hookDialog := page[strings.Index(page, `id="new-hook-secret"`):]
	if !strings.Contains(deployDialog, `data-value="`+image+`"`) || !strings.Contains(deployDialog, `data-value="`+git.ID+`"`) || !strings.Contains(deployDialog, "Shop / production") {
		t.Error("the deploy token dialog does not offer both apps, with where they are")
	}
	if strings.Contains(hookDialog, `data-value="`+image+`"`) || !strings.Contains(hookDialog, `data-value="`+git.ID+`"`) {
		t.Error("the webhook dialog offers an app from an image, or not the one from Git")
	}

	// One of each, made from the page's own dialogs: they are rows of the
	// one table, each with its type.
	a.newToken(db.AbilityRead, db.AbilityDeploy)
	res, page = a.post("/keys/tokens", "/keys/deploy-tokens", url.Values{"deploy_resource": {image}})
	wantStatus(t, res, http.StatusOK)
	deployToken := deployTokenRE.FindString(page)
	if deployToken == "" || !strings.Contains(page, "Deploy token for storefront created") || !strings.Contains(page, "/api/v1/deploy?uuid="+image) {
		t.Fatal("the new deploy token is not shown with how to use it")
	}
	res, _ = a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {git.ID}})
	wantRedirect(t, res, "/keys/tokens")
	stored, _ := a.db.AppByID(ctx, git.ID)
	hookSecret, err := a.server.Box.OpenString(stored.WebhookSecret)
	if err != nil || len(hookSecret) != 48 {
		t.Fatalf("the webhook secret: %q, %v", hookSecret, err)
	}
	res, page = a.get("/keys/tokens")
	wantStatus(t, res, http.StatusOK)
	if strings.Count(page, "<table") != 1 || strings.Count(page, "<tr>") != 4 {
		t.Errorf("want one table of a head and three rows: %d tables, %d rows", strings.Count(page, "<table"), strings.Count(page, "<tr>"))
	}
	for _, want := range []string{
		`<td class="whitespace-nowrap">API token</td>`, `<span class="badge">Read</span>`, `<span class="badge">Deploy</span>`,
		`<td class="whitespace-nowrap">Deploy token</td>`, `action="/apps/` + image + `/deploy-token"`, "/api/v1/deploy?uuid=" + image,
		`<td class="whitespace-nowrap">Webhook secret</td>`, `action="/apps/` + git.ID + `/webhook-secret"`, "/webhooks/git/" + git.ID, `hx-get="/apps/` + git.ID + `/webhook-secret"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the table of tokens is missing %q", want)
		}
	}
	if strings.Contains(page, hookSecret) || strings.Contains(page, deployToken) || strings.Contains(page, "msd_") {
		t.Fatal("the API Tokens tab holds a secret that was not asked for")
	}
	// What has one already is no longer offered one.
	deployDialog = page[strings.Index(page, `id="new-deploy-token"`):]
	if strings.Contains(deployDialog, `data-value="`+image+`"`) || !strings.Contains(deployDialog, `data-value="`+git.ID+`"`) {
		t.Error("the deploy token dialog offers the app that has a token, or not the one without")
	}
	if strings.Contains(page, `id="new-hook-secret"`) {
		t.Error("a webhook secret is offered though nothing is left without one")
	}

	// No other page talks about tokens: not Account, not an app's
	// Settings, not Sources.
	for _, path := range []string{"/account", "/apps/" + git.ID + "/settings", "/apps/" + image + "/settings", "/sources"} {
		_, body := a.get(path)
		// The sidebar's link is the one way to the Keys page.
		if n := strings.Count(body, `href="/keys`); n != 1 {
			t.Errorf("%s has %d links to the Keys page, want the sidebar's alone", path, n)
		}
		for _, gone := range []string{"Deploy from outside", "kept with every other key and token", `id="tokens"`, "Deploy keys", hookSecret} {
			if strings.Contains(body, gone) {
				t.Errorf("%s still has %q", path, gone)
			}
		}
	}

	// An app without a secret has none to show.
	res, _ = a.get("/apps/" + image + "/webhook-secret")
	wantStatus(t, res, http.StatusNotFound)

	// A token that is refused comes back in the open dialog, as it was
	// filled in.
	form := tokenForm("", "7", db.AbilityWrite, db.AbilitySensitive)
	res, page = a.post("/keys/tokens", "/account/tokens", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "data-autoopen") || !strings.Contains(page, "Enter a name") {
		t.Fatalf("a refused token: %d", res.StatusCode)
	}
	for _, want := range []string{`name="perm_write" value="1" checked`, `name="perm_sensitive" value="1" checked`, `name="token_expires" value="7"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the refused dialog lost %q", want)
		}
	}
	if strings.Contains(page, `name="perm_read" value="1" checked`) {
		t.Error("the refused dialog checked Read, which was not")
	}
	res, page = a.post("/keys/tokens", "/account/tokens", tokenForm("x", "never"))
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Choose at least one permission") {
		t.Fatalf("a token with no permission: %d", res.StatusCode)
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
	if _, page = a.get("/keys/tokens"); strings.Contains(page, "secret-app") || strings.Contains(page, theirs.ID) {
		t.Fatal("another team's app is listed on the Keys page")
	}
	res, body := a.get("/apps/" + theirs.ID + "/webhook-secret")
	if res.StatusCode != http.StatusNotFound || strings.Contains(body, "their-webhook-secret") {
		t.Fatalf("another team's webhook secret: %d", res.StatusCode)
	}

	// A Member sees both tabs and the team's keys, makes tokens of their
	// own, and cannot add or delete an SSH key.
	a.post("/keys", "/sources/keys", url.Values{"key_name": {"shop repository"}})
	keys, _ := a.db.ListSSHKeys(ctx, firstTeam(t, a))
	if len(keys) != 1 {
		t.Fatalf("%d keys", len(keys))
	}
	_, page = a.get("/keys")
	if strings.Count(page, "<table") != 1 || !strings.Contains(page, "shop repository") || !strings.Contains(page, "ssh-ed25519 ") || !strings.Contains(page, "/sources/keys/"+keys[0].ID+"/delete") {
		t.Fatal("the Private Keys tab does not list the key in its table, with a way to delete it")
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
	// The Member's tokens tab: the team's deploy token and webhook secret,
	// their own API tokens and nobody else's.
	m.newToken(db.AbilityRead)
	res, page = m.get("/keys/tokens")
	wantStatus(t, res, http.StatusOK)
	if strings.Count(page, `<td class="whitespace-nowrap">API token</td>`) != 1 || !strings.Contains(page, `<td class="whitespace-nowrap">Deploy token</td>`) || !strings.Contains(page, `<td class="whitespace-nowrap">Webhook secret</td>`) {
		t.Fatal("a Member's tokens tab does not hold their one token with the team's deploy token and webhook secret")
	}
}

// The two dialogs that choose a resource take only what they offered: the
// team's own, no preview, and nothing that already has what is asked for.
func TestKeysDialogsTakeOnlyWhatTheyOffer(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	image := a.newApp(projectID, env, "storefront", false, nil)
	git := a.newGitApp(projectID, env, "api", nil)
	preview := a.newGitApp(projectID, env, "api-pr-1", nil)
	if _, err := a.db.Exec(`UPDATE apps SET preview_of = ?, pr_number = 1 WHERE id = ?`, git.ID, preview.ID); err != nil {
		t.Fatal(err)
	}
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, host, created_at) VALUES ('theirserver', 'otherteam', 'theirs', 'ssh', '203.0.113.9', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	theirs, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-app", Source: db.SourceGit,
		RepoURL: "https://github.com/their/app", RepoName: "their/app", Branch: "main", BuildPack: "dockerfile", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	secrets := func() (n int) {
		a.db.QueryRow(`SELECT count(*) FROM apps WHERE deploy_token_hash <> '' OR webhook_secret <> ''`).Scan(&n)
		return n
	}

	for name, id := range map[string]string{"nothing": "", "an unknown id": "nosuchid", "another team's app": theirs.ID, "a preview": preview.ID} {
		res, page := a.post("/keys/tokens", "/keys/deploy-tokens", url.Values{"deploy_resource": {id}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Choose an app or a service") || deployTokenRE.MatchString(page) {
			t.Errorf("a deploy token for %s: %d", name, res.StatusCode)
		}
		res, page = a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {id}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Choose an app or a service") {
			t.Errorf("a webhook secret for %s: %d", name, res.StatusCode)
		}
	}
	// An app from an image has no repository to send pushes.
	res, _ := a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {image}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if n := secrets(); n != 0 {
		t.Fatalf("a refused dialog made a token or a secret for %d apps", n)
	}

	// The second time, the app has one: that is Replace's to do, which asks.
	res, page := a.post("/keys/tokens", "/keys/deploy-tokens", url.Values{"deploy_resource": {git.ID}})
	wantStatus(t, res, http.StatusOK)
	first := deployTokenRE.FindString(page)
	res, _ = a.post("/keys/tokens", "/keys/deploy-tokens", url.Values{"deploy_resource": {git.ID}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if stored, _ := a.db.AppByID(ctx, git.ID); first == "" || stored.DeployTokenHash != secret.HashToken(first) {
		t.Fatal("the dialog replaced a deploy token that existed")
	}
	res, _ = a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {git.ID}})
	wantRedirect(t, res, "/keys/tokens")
	before, _ := a.db.AppByID(ctx, git.ID)
	res, _ = a.post("/keys/tokens", "/keys/webhook-secrets", url.Values{"hook_resource": {git.ID}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if after, _ := a.db.AppByID(ctx, git.ID); after.WebhookSecret != before.WebhookSecret {
		t.Fatal("the dialog replaced a webhook secret that existed")
	}
}

// A form whose answer shows what it made is acted on once, however often a
// browser sends it: Refresh on that answer sends the POST again.
func TestFormsThatShowASecretActOnce(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	app := a.newApp(projectID, env, "storefront", false, nil)
	// again sends the same form a second time and wants the refusal: a
	// redirect to the page, which then says why nothing new is on it.
	again := func(path string, form url.Values, back string) {
		t.Helper()
		res, body := a.post(back, path, form)
		wantRedirect(t, res, back)
		if strings.Contains(body, "msd_") || deployTokenRE.MatchString(body) || strings.Contains(body, "ssh-ed25519") {
			t.Fatalf("the second answer of %s holds a secret", path)
		}
		if _, page := a.get(back); !strings.Contains(page, "had been sent already") {
			t.Fatalf("%s does not say that the form was sent twice", back)
		}
	}

	// An API token.
	form := tokenForm("ci", "30", db.AbilityRead)
	form.Set("_once", a.onceOf("/keys/tokens", "/account/tokens"))
	res, page := a.post("/keys/tokens", "/account/tokens", form)
	wantStatus(t, res, http.StatusOK)
	// The page that shows it says which address a refresh should fetch.
	if !newTokenRE.MatchString(page) || !strings.Contains(page, `data-address="/keys/tokens"`) {
		t.Fatal("the token is not shown, or the page does not give its own address")
	}
	again("/account/tokens", form, "/keys/tokens")
	if list, _ := a.db.ListAPITokens(ctx, owner.ID, team); len(list) != 1 {
		t.Fatalf("the form sent twice made %d tokens", len(list))
	}
	// The form as the page draws it next is another form, and is acted on.
	next := a.onceOf("/keys/tokens", "/account/tokens")
	if next == form.Get("_once") {
		t.Fatal("two renderings of the form carry the same value")
	}
	form.Set("_once", next)
	res, _ = a.post("/keys/tokens", "/account/tokens", form)
	wantStatus(t, res, http.StatusOK)
	if list, _ := a.db.ListAPITokens(ctx, owner.ID, team); len(list) != 2 {
		t.Fatalf("a new form made %d tokens in all, want 2", len(list))
	}
	// A form that was refused has spent nothing: corrected, it works.
	form = tokenForm("", "30", db.AbilityRead)
	form.Set("_once", next+"x")
	res, _ = a.post("/keys/tokens", "/account/tokens", form)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	form.Set("token_name", "third")
	res, _ = a.post("/keys/tokens", "/account/tokens", form)
	wantStatus(t, res, http.StatusOK)
	// An ordinary page gives no address: only one that answers a POST does.
	if _, page = a.get("/keys/tokens"); strings.Contains(page, "data-address") {
		t.Fatal("a page that was fetched gives an address to replace its own")
	}

	// An SSH key.
	keyForm := url.Values{"key_name": {"shop repository"}, "_once": {a.onceOf("/keys", "/sources/keys")}}
	res, page = a.post("/keys", "/sources/keys", keyForm)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "Key created") || !strings.Contains(page, `data-address="/keys"`) {
		t.Fatal("the key is not shown, or the page does not give its own address")
	}
	again("/sources/keys", keyForm, "/keys")
	if keys, _ := a.db.ListSSHKeys(ctx, team); len(keys) != 1 {
		t.Fatalf("the form sent twice made %d keys", len(keys))
	}

	// A deploy token: sent again, the form would replace the token that
	// was just shown with one nobody has seen.
	deployForm := url.Values{"deploy_resource": {app}, "_once": {a.onceOf("/keys/tokens", "/keys/deploy-tokens")}}
	res, page = a.post("/keys/tokens", "/keys/deploy-tokens", deployForm)
	wantStatus(t, res, http.StatusOK)
	token := deployTokenRE.FindString(page)
	if token == "" || !strings.Contains(page, `data-address="/keys/tokens"`) {
		t.Fatal("the deploy token is not shown, or the page does not give its own address")
	}
	// The dialog's own form again: it names an app that has a token by
	// now, and is still answered as a repeat, not as a wrong choice.
	again("/keys/deploy-tokens", deployForm, "/keys/tokens")
	if stored, _ := a.db.AppByID(ctx, app); stored.DeployTokenHash != secret.HashToken(token) {
		t.Fatal("the dialog's form sent twice replaced the token")
	}
	// Replace, from the row the token now has, and that form again.
	replace := url.Values{"_once": {a.onceOf("/keys/tokens", "/apps/"+app+"/deploy-token")}}
	res, page = a.post("/keys/tokens", "/apps/"+app+"/deploy-token", replace)
	wantStatus(t, res, http.StatusOK)
	replaced := deployTokenRE.FindString(page)
	if replaced == "" || replaced == token {
		t.Fatal("Replace did not show a new token")
	}
	again("/apps/"+app+"/deploy-token", replace, "/keys/tokens")
	if stored, _ := a.db.AppByID(ctx, app); stored.DeployTokenHash != secret.HashToken(replaced) {
		t.Fatal("the form sent twice replaced the token that had just been shown")
	}
	// Revoke is not such a form: it shows nothing, and answers with a redirect.
	replace.Set("revoke", "1")
	res, _ = a.post("/keys/tokens", "/apps/"+app+"/deploy-token", replace)
	wantRedirect(t, res, "/keys/tokens")
	if stored, _ := a.db.AppByID(ctx, app); stored.DeployTokenHash != "" {
		t.Fatal("the token was not revoked")
	}

	// A reset link for a member.
	m := a.newPerson("Member", db.RoleMember)
	resetPath := "/team/members/" + m.user.ID + "/reset"
	reset := url.Values{"_once": {a.onceOf("/team", resetPath)}}
	res, page = a.post("/team", resetPath, reset)
	wantStatus(t, res, http.StatusOK)
	if !linkRE.MatchString(page) || !strings.Contains(page, `data-address="/team"`) {
		t.Fatal("the reset link is not shown, or the page does not give its own address")
	}
	resets := func() (n int) {
		a.db.QueryRow(`SELECT count(*) FROM password_resets WHERE user_id = ?`, m.user.ID).Scan(&n)
		return n
	}
	made := resets()
	again(resetPath, reset, "/team")
	if resets() != made || made == 0 {
		t.Fatalf("the form sent twice made another reset link: %d, then %d", made, resets())
	}

	// An invitation.
	invite := url.Values{"email": {"new@example.com"}, "role": {db.RoleMember}, "_once": {a.onceOf("/team", "/team/invitations")}}
	res, page = a.post("/team", "/team/invitations", invite)
	wantStatus(t, res, http.StatusOK)
	if !linkRE.MatchString(page) {
		t.Fatal("the invitation link is not shown")
	}
	again("/team/invitations", invite, "/team")

	// A form that was let through and then made nothing has spent nothing
	// either: with as many tokens as a person may have, the form is
	// refused, and the very same form works once one is revoked.
	for {
		list, _ := a.db.ListAPITokens(ctx, owner.ID, team)
		if len(list) >= db.MaxAPITokens {
			break
		}
		a.newToken(db.AbilityRead)
	}
	full := tokenForm("one too many", "30", db.AbilityRead)
	full.Set("_once", a.onceOf("/keys/tokens", "/account/tokens"))
	res, _ = a.post("/keys/tokens", "/account/tokens", full)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	list, _ := a.db.ListAPITokens(ctx, owner.ID, team)
	res, _ = a.post("/keys/tokens", "/account/tokens/"+list[0].ID+"/delete", nil)
	wantRedirect(t, res, "/keys/tokens")
	res, page = a.post("/keys/tokens", "/account/tokens", full)
	if res.StatusCode != http.StatusOK || !newTokenRE.MatchString(page) {
		t.Fatalf("the form that made nothing the first time is refused the second: %d", res.StatusCode)
	}
	// A dialog that was refused for its choice has spent nothing: the same
	// form with a resource it may name is acted on.
	other := a.newApp(projectID, env, "other", false, nil)
	wrong := url.Values{"deploy_resource": {"nosuchid"}, "_once": {a.onceOf("/keys/tokens", "/keys/deploy-tokens")}}
	res, _ = a.post("/keys/tokens", "/keys/deploy-tokens", wrong)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	wrong.Set("deploy_resource", other)
	res, page = a.post("/keys/tokens", "/keys/deploy-tokens", wrong)
	if res.StatusCode != http.StatusOK || !deployTokenRE.MatchString(page) {
		t.Fatalf("the corrected dialog is refused: %d", res.StatusCode)
	}

	// One person's value is not another's: the Member's first form is
	// acted on although the Owner sent a form with the same value.
	mine := tokenForm("mine", "30", db.AbilityRead)
	mine.Set("_once", next)
	res, _ = m.post("/account/tokens", mine)
	wantStatus(t, res, http.StatusOK)
}
