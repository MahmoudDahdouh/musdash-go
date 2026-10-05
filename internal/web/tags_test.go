package web

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

func TestTagsOnAppsAndServices(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", false, nil)
	api := a.newApp(projectID, env, "api", false, nil)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "blog", nil)
	tagsOf := func(kind, id string) []string {
		got, err := a.db.TagsOf(ctx, team, kind, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// No tags yet.
	res, body := a.get("/tags")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No tags yet") {
		t.Fatalf("empty Tags page:\n%s", body)
	}

	settings := "/apps/" + web + "/settings"
	res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tags": {"Nightly, frontend nightly"}})
	wantRedirect(t, res, settings+"#tags")
	if got := tagsOf(db.KindApp, web); !reflect.DeepEqual(got, []string{"frontend", "nightly"}) {
		t.Fatalf("tags of web: %v", got)
	}
	_, body = a.get(settings)
	if !strings.Contains(body, `value="frontend, nightly"`) || !strings.Contains(body, `href="/tags/nightly"`) {
		t.Fatalf("the settings page does not show the tags:\n%s", body)
	}
	res, _ = a.post(settings, "/apps/"+api+"/tags", url.Values{"tags": {"nightly"}})
	wantRedirect(t, res, "/apps/"+api+"/settings#tags")
	svcSettings := "/services/" + svc.ID + "/settings"
	res, _ = a.post(svcSettings, "/services/"+svc.ID+"/tags", url.Values{"tags": {"nightly"}})
	wantRedirect(t, res, svcSettings+"#tags")
	if got := tagsOf(db.KindService, svc.ID); !reflect.DeepEqual(got, []string{"nightly"}) {
		t.Fatalf("tags of the service: %v", got)
	}

	// What is not a tag is refused whole, and too many are.
	for _, bad := range []string{"ok, not/ok", "<script>", "../../x", "a b c d e f g h i j k"} {
		res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tags": {bad}})
		wantRedirect(t, res, settings+"#tags")
		if got := tagsOf(db.KindApp, web); !reflect.DeepEqual(got, []string{"frontend", "nightly"}) {
			t.Fatalf("after %q: %v", bad, got)
		}
	}
	_, body = a.get(settings)
	if strings.Contains(body, "<script>") && !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("a refused tag was echoed into the page")
	}

	// The Tags page and a tag's page.
	_, body = a.get("/tags")
	if !strings.Contains(body, `href="/tags/nightly"`) || !strings.Contains(body, "2 apps, 1 service") || !strings.Contains(body, `href="/tags/frontend"`) {
		t.Fatalf("Tags page:\n%s", body)
	}
	res, body = a.get("/tags/nightly")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"/apps/" + web, "/apps/" + api, "/services/" + svc.ID, "/api/v1/deploy?tag=nightly", "Deploy all"} {
		if !strings.Contains(body, want) {
			t.Errorf("the tag's page lacks %q", want)
		}
	}
	for _, path := range []string{"/tags/nosuchtag", "/tags/Not%20A%20Tag", "/tags/..%2f"} {
		res, _ = a.get(path)
		wantStatus(t, res, http.StatusNotFound)
	}

	// Deploy all: one deployment for each app and for the service.
	before := a.deployJobs()
	res, _ = a.post("/tags/nightly", "/tags/nightly/deploy", nil)
	wantRedirect(t, res, "/tags/nightly")
	for _, id := range []string{web, api} {
		if dep := a.waitDeployed(id); dep.Trigger != "tag" || dep.Status != db.DeploySuccess {
			t.Fatalf("deployment of %s: %+v", id, dep)
		}
	}
	if got := a.deployJobs() - before; got != 2 {
		t.Fatalf("%d app deployments queued, want 2", got)
	}
	var serviceJobs int
	a.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'service' AND json_extract(payload, '$.id') = ?`, svc.ID).Scan(&serviceJobs)
	if serviceJobs != 1 {
		t.Fatalf("%d service deployments, want 1", serviceJobs)
	}
	if got := a.waitService(svc.ID); got.Status != db.AppRunning {
		t.Fatalf("the service after Deploy all: %s: %s", got.Status, got.LastError)
	}
	res, _ = a.post("/tags/nightly", "/tags/nosuchtag/deploy", nil)
	wantStatus(t, res, http.StatusNotFound)

	// Clearing the field removes the tags; a tag nothing has is gone.
	res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tags": {""}})
	wantRedirect(t, res, settings+"#tags")
	res, _ = a.get("/tags/frontend")
	wantStatus(t, res, http.StatusNotFound)

	// Deleting an app takes its tags along.
	res, _ = a.post(settings, "/apps/"+api+"/delete", url.Values{"confirm": {"api"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete app: %d", res.StatusCode)
	}
	a.waitGone(api)
	if list, _ := a.db.ListTags(ctx, team); len(list) != 1 || list[0].Apps != 0 || list[0].Services != 1 {
		t.Fatalf("tags after deleting the app: %+v", list)
	}
}

func TestAnotherTeamsTagsAreNotShown(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, host, created_at) VALUES ('theirserver', 'otherteam', 'theirs', 'ssh', '203.0.113.9', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	theirs, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	a.db.SetTags(ctx, "otherteam", db.KindApp, theirs.ID, []string{"nightly"})

	_, body := a.get("/tags")
	if strings.Contains(body, "nightly") {
		t.Fatal("another team's tag is listed")
	}
	res, body := a.get("/tags/nightly")
	wantStatus(t, res, http.StatusNotFound)
	if strings.Contains(body, "secret-app") {
		t.Fatal("another team's app is shown")
	}
	res, _ = a.post("/tags", "/tags/nightly/deploy", nil)
	wantStatus(t, res, http.StatusNotFound)
	// And their app cannot be tagged from here.
	res, _ = a.post("/tags", "/apps/"+theirs.ID+"/tags", url.Values{"tags": {"mine"}})
	wantStatus(t, res, http.StatusNotFound)
	if got, _ := a.db.TagsOf(ctx, "otherteam", db.KindApp, theirs.ID); !reflect.DeepEqual(got, []string{"nightly"}) {
		t.Fatalf("their tags changed: %v", got)
	}
	if n := a.deployJobs(); n != 0 {
		t.Fatalf("%d deployments queued", n)
	}
	_ = team
}

func TestAPreviewCannotBeTagged(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	parent := a.newGitApp(projectID, env, "web", nil)
	preview, err := a.db.CreatePreview(ctx, parent, 7, "web-pr-7", "feature")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := a.post("/apps/"+parent.ID+"/settings", "/apps/"+preview.ID+"/tags", url.Values{"tags": {"nightly"}})
	wantRedirect(t, res, "/apps/"+preview.ID)
	if got, _ := a.db.TagsOf(ctx, team, db.KindApp, preview.ID); len(got) != 0 {
		t.Fatalf("a preview was tagged: %v", got)
	}
}
