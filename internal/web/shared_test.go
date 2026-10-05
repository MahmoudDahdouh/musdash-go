package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// sharedValue opens one stored shared variable.
func (a *app) sharedValue(team, scope, id, key string) string {
	a.t.Helper()
	list, err := a.db.SharedVars(context.Background(), team, scope, id)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, v := range list {
		if v.Key == key {
			plain, err := a.server.Box.OpenString(v.Value)
			if err != nil || plain == v.Value {
				a.t.Fatalf("%s is not stored sealed", key)
			}
			return plain
		}
	}
	return ""
}

func TestSharedVariablePages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	server, _ := a.db.EnsureLocalServer(ctx, team, "")

	pages := map[string][2]string{ // page → scope, scope id
		"/team/variables":                        {db.ScopeTeam, team},
		"/projects/" + projectID + "/variables":  {db.ScopeProject, projectID},
		"/environments/" + env.ID + "/variables": {db.ScopeEnvironment, env.ID},
		"/servers/" + server.ID + "/variables":   {db.ScopeServer, server.ID},
	}
	for page, where := range pages {
		res, body := a.get(page)
		wantStatus(t, res, http.StatusOK)
		if !strings.Contains(body, "{{"+where[0]+".NAME}}") {
			t.Errorf("%s does not say how its variables are named", page)
		}
		res, _ = a.post(page, page, url.Values{"vars": {"SMTP_HOST=mail.example.com\n# a note\nTOKEN=\"s3cret value\"\n"}})
		wantRedirect(t, res, page)
		if got := a.sharedValue(team, where[0], where[1], "TOKEN"); got != "s3cret value" {
			t.Errorf("%s: stored %q", page, got)
		}
		// The page lists the names. The values come when asked for, and
		// the editor gives them back for editing.
		_, body = a.get(page)
		if !strings.Contains(body, "SMTP_HOST") || strings.Contains(body, "mail.example.com") || strings.Contains(body, "s3cret value") {
			t.Errorf("%s should list the names and no value", page)
		}
		if _, body = a.get(page + "/values"); !strings.Contains(body, "mail.example.com") || !strings.Contains(body, "s3cret value") {
			t.Errorf("%s/values does not show what was saved", page)
		}
		if _, body = a.get(page + "/values?hide=1"); strings.Contains(body, "s3cret value") {
			t.Errorf("%s/values?hide=1 sends a value", page)
		}
		if _, body = a.get(page + "/edit"); !strings.Contains(body, "SMTP_HOST=mail.example.com") || !strings.Contains(body, "TOKEN=s3cret value") {
			t.Errorf("%s/edit does not show what was saved", page)
		}
	}

	// What cannot be saved.
	for name, text := range map[string]string{
		"not a variable": "this is not a variable",
		"names another":  "A={{team.TOKEN}}",
		"too many":       strings.Repeat("X", 1) + "0=1\n" + manyVars(maxSharedVars),
	} {
		res, body := a.post("/team/variables", "/team/variables", url.Values{"vars": {text}})
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d\n%s", name, res.StatusCode, body)
		}
	}
	if got := a.sharedValue(team, db.ScopeTeam, team, "TOKEN"); got != "s3cret value" {
		t.Fatalf("a refused form changed what was stored: %q", got)
	}
	// Saving an empty form removes them.
	res, _ := a.post("/team/variables", "/team/variables", url.Values{"vars": {""}})
	wantRedirect(t, res, "/team/variables")
	if list, _ := a.db.SharedVars(ctx, team, db.ScopeTeam, team); len(list) != 0 {
		t.Fatalf("still stored: %+v", list)
	}
	// Unknown ids.
	for _, page := range []string{"/projects/nosuchid/variables", "/environments/nosuchid/variables", "/servers/nosuchid/variables"} {
		res, _ := a.get(page)
		wantStatus(t, res, http.StatusNotFound)
	}
}

func manyVars(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("V" + itoa(i) + "=1\n")
	}
	return b.String()
}

func TestSharedVariablesByRole(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	server, _ := a.db.EnsureLocalServer(ctx, team, "")
	mem := a.newPerson("Member", db.RoleMember)
	ad := a.newPerson("Admin", db.RoleAdmin)

	teamPage, serverPage := "/team/variables", "/servers/"+server.ID+"/variables"
	for _, page := range []string{teamPage, serverPage} {
		res, _ := a.post(page, page, url.Values{"vars": {"TOKEN=owner-set-secret"}})
		wantRedirect(t, res, page)

		// A Member sees the name, not the value, and cannot save.
		res, body := mem.get(page)
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(body, "owner-set-secret") {
			t.Errorf("%s shows a Member the value", page)
		}
		if !strings.Contains(body, ".TOKEN}}") || strings.Contains(body, "Save variables") {
			t.Errorf("%s should show a Member the name and no form", page)
		}
		res, body = mem.post(page, url.Values{"vars": {"TOKEN=changed"}})
		wantStatus(t, res, http.StatusForbidden)
		if !strings.Contains(body, refused) {
			t.Errorf("%s: not refused for the role", page)
		}
		// Nor can a Member ask for the values or open the editor.
		for _, held := range []string{"/values", "/edit"} {
			res, body = mem.get(page + held)
			if res.StatusCode != http.StatusForbidden || strings.Contains(body, "owner-set-secret") {
				t.Errorf("%s%s: a Member got %d", page, held, res.StatusCode)
			}
		}
		// An Admin sees and saves, and is told who else can use them.
		if _, body = ad.get(page + "/values"); !strings.Contains(body, "owner-set-secret") {
			t.Errorf("%s does not show an Admin the value", page)
		}
		_, body = ad.get(page)
		if strings.Contains(body, "owner-set-secret") {
			t.Errorf("%s holds the value before it is asked for", page)
		}
		if !strings.Contains(body, "Every member can use these") {
			t.Errorf("%s does not say that members can use these", page)
		}
		res, _ = ad.post(page, url.Values{"vars": {"TOKEN=admin-set"}})
		wantRedirect(t, res, page)
	}
	if got := a.sharedValue(team, db.ScopeTeam, team, "TOKEN"); got != "admin-set" {
		t.Fatalf("team variable: %q", got)
	}

	// A project's and an environment's are a Member's to change.
	for _, page := range []string{"/projects/" + projectID + "/variables", "/environments/" + env.ID + "/variables"} {
		res, _ := mem.post(page, url.Values{"vars": {"STAGE=member-set"}})
		wantRedirect(t, res, page)
	}
	if got := a.sharedValue(team, db.ScopeEnvironment, env.ID, "STAGE"); got != "member-set" {
		t.Fatalf("environment variable: %q", got)
	}
}

func TestAnotherTeamsScopesHaveNoVariablePages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, host, created_at) VALUES ('theirserver', 'otherteam', 'theirs', 'ssh', '203.0.113.9', 1)`)
	sealed := a.seal("their-secret")
	for scope, id := range map[string]string{db.ScopeProject: p.ID, db.ScopeEnvironment: envs[0].ID, db.ScopeServer: "theirserver"} {
		if err := a.db.ReplaceSharedVars(ctx, "otherteam", scope, id, []db.EnvVar{{Key: "THEIRS", Value: sealed}}); err != nil {
			t.Fatal(err)
		}
	}
	token := a.csrf("/team/variables")
	for _, page := range []string{"/projects/" + p.ID + "/variables", "/environments/" + envs[0].ID + "/variables", "/servers/theirserver/variables"} {
		res, body := a.get(page)
		wantStatus(t, res, http.StatusNotFound)
		if strings.Contains(body, "THEIRS") || strings.Contains(body, "their-secret") {
			t.Fatalf("%s leaked another team's variable", page)
		}
		res, _ = a.postRaw(a.client, page, url.Values{"_csrf": {token}, "vars": {"THEIRS=mine"}}, nil)
		wantStatus(t, res, http.StatusNotFound)
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM shared_vars WHERE team_id = 'otherteam'`).Scan(&n)
	if n != 3 {
		t.Fatalf("the other team's variables were changed: %d left", n)
	}
}

func TestSavingAnAppsVariablesWarnsAboutMissingSharedOnes(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	id := a.newApp(projectID, env, "web", false, nil)
	if err := a.db.ReplaceSharedVars(ctx, team, db.ScopeProject, projectID, []db.EnvVar{{Key: "DB_HOST", Value: a.seal("db.internal")}}); err != nil {
		t.Fatal(err)
	}
	page := "/apps/" + id + "/environment"

	res, _ := a.post(page, page, url.Values{"vars": {"A={{project.DB_HOST}}\nB={{team.NOPE}}\nC={{ .tmpl }}"}})
	wantRedirect(t, res, page)
	_, body := a.get(page)
	if !strings.Contains(body, "{{team.NOPE}}") || !strings.Contains(body, "do not exist yet") {
		t.Fatalf("no warning about the missing name:\n%s", body)
	}
	// The variables were saved as they were typed.
	_, body = a.get(page + "/edit")
	if !strings.Contains(body, "A={{project.DB_HOST}}") {
		t.Fatalf("the page does not show the name as typed:\n%s", body)
	}

	// With every name known there is no warning.
	res, _ = a.post(page, page, url.Values{"vars": {"A={{project.DB_HOST}}"}})
	wantRedirect(t, res, page)
	if _, body = a.get(page); strings.Contains(body, "do not exist yet") {
		t.Fatal("a warning without a missing name")
	}
}
