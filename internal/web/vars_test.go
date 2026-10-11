package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// envValue opens what the table holds for one of an app's variables, and
// fails if the table holds the plain text.
func (a *app) envValue(appID, key string) (string, bool) {
	a.t.Helper()
	v, err := a.db.EnvVarByKey(context.Background(), db.KindApp, appID, key)
	if err != nil {
		return "", false
	}
	plain, err := a.server.Box.OpenString(v.Value)
	if err != nil || (plain != "" && plain == v.Value) {
		a.t.Fatalf("%s is not stored sealed", key)
	}
	return plain, true
}

// uniqueIDs fails for an id that two elements of a page have: a row's
// button and its menu are named after the variable, and a page has a row a
// variable.
func uniqueIDs(t *testing.T, page, body string) {
	t.Helper()
	ids := map[string]int{}
	for _, m := range elementID.FindAllStringSubmatch(body, -1) {
		ids[m[1]]++
	}
	if len(ids) < 10 {
		t.Errorf("%s: only %d ids found", page, len(ids))
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("%s has %d elements with id %q", page, n, id)
		}
	}
}

// An app's variables one at a time: a value is asked for by its name and
// is the only one that answer holds; adding, changing and deleting one
// leaves the others as they are.
func TestAppVariablesOneAtATime(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	page := a.appPath(appID) + "/environment"

	// With nothing stored the tab says so, and offers both ways to add.
	_, body := a.get(page)
	for _, want := range []string{"No variables yet", `data-open="new-var"`, `href="` + page + `/edit"`, `action="` + page + `/vars"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the empty tab lacks %s", want)
		}
	}

	add := func(key, value string) {
		t.Helper()
		res, _ := a.post(page, page+"/vars", url.Values{"key": {key}, "value": {value}})
		wantRedirect(t, res, page)
	}
	add("DATABASE_URL", "postgres://u:hunter2@db/app")
	add("API_KEY", "  k-9f3 ")
	add("EMPTY", "")
	// A pasted value ends with its line's break; a whole line pasted into
	// the name is taken apart.
	add("PASTED", "tok-123\r\n")
	add("WHOLE=line-value", "")
	for key, want := range map[string]string{"DATABASE_URL": "postgres://u:hunter2@db/app", "API_KEY": "  k-9f3 ", "EMPTY": "", "PASTED": "tok-123", "WHOLE": "line-value"} {
		if got, ok := a.envValue(appID, key); !ok || got != want {
			t.Errorf("%s stored as %q (%v), want %q", key, got, ok, want)
		}
	}

	// The tab lists the names and holds no value.
	_, body = a.get(page)
	for _, secret := range []string{"hunter2", "k-9f3", "tok-123", "line-value"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the tab holds the value %q unasked", secret)
		}
	}
	for _, want := range []string{"DATABASE_URL", `hx-get="` + page + `/vars/DATABASE_URL"`, `hx-get="` + page + `/vars/DATABASE_URL/edit"`,
		`action="` + page + `/vars/DATABASE_URL/delete"`, `hx-get="` + page + `/values"`, `hx-get="` + page + `/values?hide=1"`, `id="change-var-holder" data-fetched`} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab lacks %s", want)
		}
	}
	uniqueIDs(t, page, body)

	// One row's answer holds that row's value and no other, and is no page.
	res, body := a.get(page + "/vars/DATABASE_URL")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "postgres://u:hunter2@db/app") || strings.Contains(body, "k-9f3") || strings.Contains(body, "<html") {
		t.Fatalf("asking for one value: %s", body)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "<tr data-var data-shown>") || !strings.Contains(body, `hx-get="`+page+`/vars/DATABASE_URL?hide=1"`) || !strings.Contains(body, `data-copy="postgres://u:hunter2@db/app"`) {
		t.Errorf("a shown row cannot be hidden or copied: %s", body)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Error("a value may be kept by the browser")
	}
	if _, body = a.get(page + "/vars/DATABASE_URL?hide=1"); strings.Contains(body, "hunter2") || !strings.Contains(body, "DATABASE_URL") || strings.Contains(body, "data-shown") {
		t.Errorf("a hidden row: %s", body)
	}
	// A value that is set to nothing says so, and has nothing to copy.
	if _, body = a.get(page + "/vars/EMPTY"); !strings.Contains(body, "Empty") || strings.Contains(body, "data-copy") {
		t.Errorf("an empty value: %s", body)
	}
	// The space around a value is the value's.
	if _, body = a.get(page + "/vars/API_KEY"); !strings.Contains(body, "  k-9f3 <") {
		t.Errorf("a value with space around it: %s", body)
	}
	for _, missing := range []string{"/vars/NOPE", "/vars/NOPE/edit", "/vars/not-a-name"} {
		if res, _ := a.get(page + missing); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d", missing, res.StatusCode)
		}
	}

	// The dialog that changes one holds that one's value, and opens itself.
	_, body = a.get(page + "/vars/DATABASE_URL/edit")
	for _, want := range []string{`<dialog class="modal" id="change-var"`, "data-autoopen", `action="` + page + `/vars/DATABASE_URL"`, `value="DATABASE_URL"`, "postgres://u:hunter2@db/app</textarea>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the dialog lacks %s", want)
		}
	}
	if strings.Contains(body, "k-9f3") || strings.Contains(body, "<html") || strings.Contains(body, `name="build"`) {
		t.Errorf("the dialog holds more than its variable: %s", body)
	}

	// A change: the value, and the name.
	res, _ = a.post(page, page+"/vars/DATABASE_URL", url.Values{"key": {"DB_URL"}, "value": {"postgres://changed"}})
	wantRedirect(t, res, page)
	if _, ok := a.envValue(appID, "DATABASE_URL"); ok {
		t.Error("the old name is still there")
	}
	if got, _ := a.envValue(appID, "DB_URL"); got != "postgres://changed" {
		t.Errorf("DB_URL = %q", got)
	}

	// What is refused comes back as the tab with its dialog open, what was
	// typed in it, and the reason.
	refused := []struct {
		to   string
		form url.Values
		want string
	}{
		{"/vars", url.Values{"key": {"API_KEY"}, "value": {"typed-again"}}, "API_KEY exists already"},
		{"/vars", url.Values{"key": {"1BAD"}, "value": {"typed-again"}}, "Use letters, numbers and underscores"},
		{"/vars", url.Values{"key": {""}, "value": {"typed-again"}}, "Give the variable a name"},
		{"/vars", url.Values{"key": {"MULTI"}, "value": {"typed-again\nline two"}}, "cannot contain a line break"},
		{"/vars/DB_URL", url.Values{"key": {"API_KEY"}, "value": {"typed-again"}}, "API_KEY exists already"},
		{"/vars/DB_URL", url.Values{"key": {"DB URL"}, "value": {"typed-again"}}, "Use letters, numbers and underscores"},
	}
	for _, c := range refused {
		res, body := a.post(page, page+c.to, c.form)
		wantStatus(t, res, http.StatusUnprocessableEntity)
		dialog := `id="new-var"`
		if c.to != "/vars" {
			dialog = `id="change-var"`
		}
		if !strings.Contains(body, c.want) || !strings.Contains(body, "typed-again") || !strings.Contains(body, dialog+` aria-labelledby=`) || strings.Count(body, "data-autoopen") != 1 {
			t.Errorf("%s %v: the form did not come back open with %q", c.to, c.form, c.want)
		}
		if strings.Contains(body, "k-9f3") || strings.Contains(body, "postgres://changed") {
			t.Errorf("%s: a refused form brought a stored value with it", c.to)
		}
		// The answer is drawn at the form's address, which has no page:
		// it says where Refresh should go.
		if !strings.Contains(body, `data-address="`+page+`"`) {
			t.Errorf("%s: the refused form's page does not say its address", c.to)
		}
	}
	if got, _ := a.envValue(appID, "API_KEY"); got != "  k-9f3 " {
		t.Errorf("a refused form changed API_KEY to %q", got)
	}
	res, _ = a.post(page, page+"/vars/NOPE", url.Values{"key": {"NOPE"}, "value": {"x"}})
	wantStatus(t, res, http.StatusNotFound)

	// An app from an image has no build-time variables, whatever is sent.
	res, _ = a.post(page, page+"/vars", url.Values{"key": {"AT_BUILD"}, "value": {"x"}, "build": {"1"}})
	wantRedirect(t, res, page)
	if v, _ := a.db.EnvVarByKey(context.Background(), db.KindApp, appID, "AT_BUILD"); v.BuildTime {
		t.Error("an app that is not built has a build-time variable")
	}

	// The developer view holds them all, as lines that read back the same.
	_, body = a.get(page + "/edit")
	for _, want := range []string{"Developer view", "DB_URL=postgres://changed", `API_KEY=&#34;  k-9f3 &#34;`, "EMPTY=\n", `href="` + page + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the developer view lacks %q", want)
		}
	}
	res, _ = a.post(page, page, url.Values{"vars": {"DB_URL=postgres://changed\nAPI_KEY=\"  k-9f3 \"\nEMPTY=\n"}})
	wantRedirect(t, res, page)
	if got, _ := a.envValue(appID, "API_KEY"); got != "  k-9f3 " {
		t.Errorf("the developer view changed API_KEY to %q", got)
	}
	if _, ok := a.envValue(appID, "PASTED"); ok {
		t.Error("a line left out of the developer view is still stored")
	}

	// Delete asks first, and takes one.
	_, body = a.get(page)
	if !strings.Contains(body, `data-confirm="The app no longer gets it`) {
		t.Error("Delete does not ask")
	}
	res, _ = a.post(page, page+"/vars/EMPTY/delete", nil)
	wantRedirect(t, res, page)
	if _, ok := a.envValue(appID, "EMPTY"); ok {
		t.Error("EMPTY was not deleted")
	}
	if got, _ := a.envValue(appID, "DB_URL"); got != "postgres://changed" {
		t.Error("deleting one variable changed another")
	}
	res, _ = a.post(page, page+"/vars/EMPTY/delete", nil)
	wantStatus(t, res, http.StatusNotFound)

	// Show all and Hide all are the whole card. The one that is of use in
	// the card that arrives has the id the focus comes back to.
	_, body = a.get(page + "/values")
	if !strings.Contains(body, "postgres://changed") || !strings.Contains(body, "k-9f3") || strings.Count(body, "data-shown") != 2 {
		t.Errorf("Show all: %s", body)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), `<section class="card vars" id="app-vars">`) || strings.Contains(body, "<html") {
		t.Errorf("Show all is not answered with the card: %s", body)
	}
	_, body = a.get(page + "/values?hide=1")
	if strings.Contains(body, "postgres://changed") || strings.Contains(body, "data-shown") || !strings.Contains(body, "DB_URL") {
		t.Errorf("Hide all: %s", body)
	}

	// An app holds so many and no more, by either way in.
	res, _ = a.post(page, page, url.Values{"vars": {manyVars(maxAppVars)}})
	wantRedirect(t, res, page)
	res, body = a.post(page, page+"/vars", url.Values{"key": {"ONE_MORE"}, "value": {"x"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Keep to "+itoa(maxAppVars)) {
		t.Error("one more than an app may hold was not refused")
	}
	res, body = a.post(page, page, url.Values{"vars": {manyVars(maxAppVars + 1)}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Keep to "+itoa(maxAppVars)) {
		t.Error("the developer view took more than an app may hold")
	}
	if stored, _ := a.db.ListEnvVars(context.Background(), db.KindApp, appID); len(stored) != maxAppVars {
		t.Errorf("%d variables stored", len(stored))
	}
}

// An app from Git has build-time variables, which a dialog can make of one.
func TestAppBuildVariableOneAtATime(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	git := a.newGitApp(projectID, env, "api", nil)
	page := a.appPath(git.ID) + "/environment"

	if _, body := a.get(page); !strings.Contains(body, `name="build"`) || !strings.Contains(body, "Given to the build") {
		t.Fatal("the tab of an app from Git does not offer build-time variables")
	}
	res, _ := a.post(page, page+"/vars", url.Values{"key": {"NPM_TOKEN"}, "value": {"npm-s3cret"}, "build": {"1"}})
	wantRedirect(t, res, page)
	if v, err := a.db.EnvVarByKey(ctx, db.KindApp, git.ID, "NPM_TOKEN"); err != nil || !v.BuildTime {
		t.Fatalf("NPM_TOKEN: %+v %v", v, err)
	}
	// Two lists in one card, and still one element an id.
	res, _ = a.post(page, page+"/vars", url.Values{"key": {"NODE_ENV"}, "value": {"production"}})
	wantRedirect(t, res, page)
	_, tab := a.get(page)
	uniqueIDs(t, page, tab)
	if !strings.Contains(tab, `id="var-NPM_TOKEN-eye"`) || !strings.Contains(tab, `id="var-NODE_ENV-eye"`) || strings.Contains(tab, "npm-s3cret") {
		t.Error("the tab of an app from Git does not list both of its lists by name")
	}
	if _, body := a.get(page + "/vars/NPM_TOKEN/edit"); !strings.Contains(body, `name="build" value="1" checked`) || !strings.Contains(body, "npm-s3cret") {
		t.Errorf("the dialog does not say the variable is build-time: %s", body)
	}
	// A name the build tools read themselves cannot be a build argument.
	res, body := a.post(page, page+"/vars", url.Values{"key": {"DOCKER_HOST"}, "value": {"tcp://evil"}, "build": {"1"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "cannot be a build-time variable") {
		t.Error("a reserved build argument was not refused")
	}
	res, _ = a.post(page, page+"/vars/NPM_TOKEN", url.Values{"key": {"DOCKER_HOST"}, "value": {"x"}, "build": {"1"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	// Without the box it becomes a runtime variable.
	res, _ = a.post(page, page+"/vars/NPM_TOKEN", url.Values{"key": {"NPM_TOKEN"}, "value": {"npm-s3cret"}})
	wantRedirect(t, res, page)
	if v, _ := a.db.EnvVarByKey(ctx, db.KindApp, git.ID, "NPM_TOKEN"); v.BuildTime {
		t.Error("NPM_TOKEN is still build-time")
	}
	// A value that names a shared variable which is not there is saved,
	// and said.
	res, _ = a.post(page, page+"/vars", url.Values{"key": {"MAIL"}, "value": {"{{team.NOPE}}"}})
	wantRedirect(t, res, page)
	if _, body := a.get(page); !strings.Contains(body, "{{team.NOPE}}") || !strings.Contains(body, "do not exist yet") {
		t.Error("a missing shared variable was not warned about")
	}
}

// Another team's app has no variable to show, change or delete.
func TestAnotherTeamsAppHasNoVariableAddresses(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := a.server.Box.SealString("their-value")
	if err := a.db.AddEnvVar(ctx, db.KindApp, other.ID, db.EnvVar{Key: "THEIRS", Value: sealed}, maxAppVars); err != nil {
		t.Fatal(err)
	}
	base := "/projects/" + p.ID + "/env/" + envs[0].ID + "/app/" + other.ID + "/environment"
	for _, get := range []string{"/vars/THEIRS", "/vars/THEIRS/edit", "/values"} {
		if res, body := a.get(base + get); res.StatusCode != http.StatusNotFound || strings.Contains(body, "their-value") {
			t.Errorf("GET %s: %d", get, res.StatusCode)
		}
	}
	token := a.csrf("/projects")
	for _, post := range []string{"/vars", "/vars/THEIRS", "/vars/THEIRS/delete"} {
		res, _ := a.postRaw(a.client, base+post, url.Values{"_csrf": {token}, "key": {"THEIRS"}, "value": {"mine-now"}}, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", post, res.StatusCode)
		}
	}
	if v, err := a.db.EnvVarByKey(ctx, db.KindApp, other.ID, "THEIRS"); err != nil || v.Value != sealed {
		t.Fatalf("another team's variable was changed: %+v %v", v, err)
	}
}

// The four scopes of shared variables, one variable at a time.
func TestSharedVariablesOneAtATime(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	server, _ := a.db.EnsureLocalServer(ctx, team, "")

	pages := map[string][2]string{ // page → scope, scope id
		"/team/variables":                                          {db.ScopeTeam, team},
		"/projects/" + projectID + "/variables":                    {db.ScopeProject, projectID},
		"/projects/" + projectID + "/env/" + env.ID + "/variables": {db.ScopeEnvironment, env.ID},
		"/servers/" + server.ID + "/variables":                     {db.ScopeServer, server.ID},
	}
	for page, where := range pages {
		for key, value := range map[string]string{"SMTP_HOST": "mail.example.com", "TOKEN": "s3cret value"} {
			res, _ := a.post(page, page+"/vars", url.Values{"key": {key}, "value": {value}})
			wantRedirect(t, res, page)
		}
		if got := a.sharedValue(team, where[0], where[1], "TOKEN"); got != "s3cret value" {
			t.Errorf("%s: stored %q", page, got)
		}
		_, body := a.get(page)
		if !strings.Contains(body, "SMTP_HOST") || strings.Contains(body, "mail.example.com") || strings.Contains(body, "s3cret value") {
			t.Errorf("%s should list the names and no value", page)
		}
		uniqueIDs(t, page, body)
		if !strings.Contains(body, `hx-get="`+page+`/vars/TOKEN"`) || !strings.Contains(body, `data-confirm="What names it fails to deploy`) || strings.Contains(body, `name="build"`) {
			t.Errorf("%s: the rows of a scope's variables", page)
		}
		if _, body = a.get(page + "/vars/TOKEN"); !strings.Contains(body, "s3cret value") || strings.Contains(body, "mail.example.com") || strings.Contains(body, "<html") {
			t.Errorf("%s/vars/TOKEN: %s", page, body)
		}
		if _, body = a.get(page + "/vars/TOKEN?hide=1"); strings.Contains(body, "s3cret value") {
			t.Errorf("%s/vars/TOKEN?hide=1 sends the value", page)
		}
		if _, body = a.get(page + "/vars/TOKEN/edit"); !strings.Contains(body, "s3cret value</textarea>") || !strings.Contains(body, `action="`+page+`/vars/TOKEN"`) {
			t.Errorf("%s/vars/TOKEN/edit: %s", page, body)
		}

		// A shared value is the value itself, and a name is used once.
		res, body := a.post(page, page+"/vars", url.Values{"key": {"NESTED"}, "value": {"x-{{team.TOKEN}}"}})
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if !strings.Contains(body, "cannot take its value from another one") || !strings.Contains(body, `id="new-var" aria-labelledby="new-var-title" data-autoopen`) {
			t.Errorf("%s: a value that names a shared variable was not refused in its dialog", page)
		}
		res, body = a.post(page, page+"/vars/SMTP_HOST", url.Values{"key": {"TOKEN"}, "value": {"x"}})
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if !strings.Contains(body, "TOKEN exists already") || !strings.Contains(body, `id="change-var"`) {
			t.Errorf("%s: a rename onto a taken name was not refused in its dialog", page)
		}

		res, _ = a.post(page, page+"/vars/TOKEN", url.Values{"key": {"API_TOKEN"}, "value": {"renamed value"}})
		wantRedirect(t, res, page)
		if got := a.sharedValue(team, where[0], where[1], "API_TOKEN"); got != "renamed value" {
			t.Errorf("%s: after the change %q", page, got)
		}
		res, _ = a.post(page, page+"/vars/SMTP_HOST/delete", nil)
		wantRedirect(t, res, page)
		if list, _ := a.db.SharedVars(ctx, team, where[0], where[1]); len(list) != 1 || list[0].Key != "API_TOKEN" {
			t.Errorf("%s: after the delete %+v", page, list)
		}
		for _, missing := range []string{"/vars/SMTP_HOST", "/vars/SMTP_HOST/edit"} {
			if res, _ := a.get(page + missing); res.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s%s: %d", page, missing, res.StatusCode)
			}
		}
		res, _ = a.post(page, page+"/vars/SMTP_HOST/delete", nil)
		wantStatus(t, res, http.StatusNotFound)
	}

	// A scope holds so many and no more.
	page := "/projects/" + projectID + "/variables"
	res, _ := a.post(page, page, url.Values{"vars": {manyVars(maxSharedVars)}})
	wantRedirect(t, res, page)
	res, body := a.post(page, page+"/vars", url.Values{"key": {"ONE_MORE"}, "value": {"x"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Keep to "+itoa(maxSharedVars)) {
		t.Error("one more than a scope may hold was not refused")
	}
}

// The team's and a server's variables are an Admin's: a Member is refused
// one value as they are refused all of them, and every write.
func TestOneSharedVariableByRole(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	server, _ := a.db.EnsureLocalServer(ctx, team, "")
	mem := a.newPerson("Member", db.RoleMember)
	ad := a.newPerson("Admin", db.RoleAdmin)

	for _, page := range []string{"/team/variables", "/servers/" + server.ID + "/variables"} {
		res, _ := a.post(page, page+"/vars", url.Values{"key": {"TOKEN"}, "value": {"owner-set-secret"}})
		wantRedirect(t, res, page)

		for _, get := range []string{"/vars/TOKEN", "/vars/TOKEN?hide=1", "/vars/TOKEN/edit"} {
			res, body := mem.get(page + get)
			if res.StatusCode != http.StatusForbidden || strings.Contains(body, "owner-set-secret") {
				t.Errorf("%s%s: a Member got %d", page, get, res.StatusCode)
			}
		}
		for _, post := range []string{"/vars", "/vars/TOKEN", "/vars/TOKEN/delete"} {
			res, body := mem.post(page+post, url.Values{"key": {"TOKEN"}, "value": {"member-set"}})
			if res.StatusCode != http.StatusForbidden || !strings.Contains(body, refused) {
				t.Errorf("%s%s: a Member got %d", page, post, res.StatusCode)
			}
		}
		// A Member's page has the names, and none of the buttons.
		_, body := mem.get(page)
		if !strings.Contains(body, ".TOKEN}}") || strings.Contains(body, "/vars") || strings.Contains(body, "new-var") {
			t.Errorf("%s offers a Member what an Admin has", page)
		}
		if _, body = ad.get(page + "/vars/TOKEN"); !strings.Contains(body, "owner-set-secret") {
			t.Errorf("%s does not show an Admin the value", page)
		}
		res, _ = ad.post(page+"/vars/TOKEN", url.Values{"key": {"TOKEN"}, "value": {"admin-set"}})
		wantRedirect(t, res, page)
	}
	if got := a.sharedValue(team, db.ScopeTeam, team, "TOKEN"); got != "admin-set" {
		t.Fatalf("team variable: %q", got)
	}

	// A project's and an environment's are a Member's to change.
	for _, page := range []string{"/projects/" + projectID + "/variables", "/projects/" + projectID + "/env/" + env.ID + "/variables"} {
		res, _ := mem.post(page+"/vars", url.Values{"key": {"STAGE"}, "value": {"member-set"}})
		wantRedirect(t, res, page)
		if _, body := mem.get(page + "/vars/STAGE"); !strings.Contains(body, "member-set") {
			t.Errorf("%s does not show a Member the value they set", page)
		}
	}
}
