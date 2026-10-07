package web

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
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
	// The page shows them, and its dialog offers the team's tags ticked.
	if !regexp.MustCompile(`name="tag" value="frontend" checked`).MatchString(body) || !strings.Contains(body, `href="/tags/nightly"`) {
		t.Fatal("the settings page does not show the tags")
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
	if !strings.Contains(body, `href="/tags/nightly"`) || !strings.Contains(body, `href="/tags/frontend"`) {
		t.Fatal("the Tags page does not list the tags")
	}
	res, body = a.get("/tags/nightly")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"/apps/" + web, "/apps/" + api, "/services/" + svc.ID, `hx-get="/switch/tags?at=nightly"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the tag's page lacks %q", want)
		}
	}
	// How a pipeline does the same is on the Keys page, not here.
	for _, gone := range []string{"/api/v1/", "MUSDASH_TOKEN", "API token", "From a pipeline"} {
		if strings.Contains(body, gone) {
			t.Errorf("the tag's page still has %q", gone)
		}
	}
	for _, path := range []string{"/tags/nosuchtag", "/tags/Not%20A%20Tag", "/tags/..%2f"} {
		res, _ = a.get(path)
		wantStatus(t, res, http.StatusNotFound)
	}

	// The page does not deploy: that is a pipeline's to do, through the
	// API. The button went, and its address with it.
	if strings.Contains(body, "Deploy all") || strings.Contains(body, "/tags/nightly/deploy") {
		t.Fatal("the tag's page still offers to deploy everything")
	}
	before := a.deployJobs()
	res, _ = a.post("/tags/nightly", "/tags/nightly/deploy", nil)
	wantStatus(t, res, http.StatusNotFound)
	if got := a.deployJobs() - before; got != 0 {
		t.Fatalf("%d deployments queued through an address that is gone", got)
	}

	// The tag's step of the trail is a switcher: the team's tags, the
	// current one marked, how many things have each, and the way to the
	// list.
	res, menu := a.get("/switch/tags?at=nightly")
	wantStatus(t, res, http.StatusOK)
	if !regexp.MustCompile(`href="/tags/nightly"[^>]*aria-selected="true"`).MatchString(menu) || !regexp.MustCompile(`href="/tags/frontend"[^>]*aria-selected="false"`).MatchString(menu) {
		t.Fatalf("tag switcher: %s", menu)
	}
	if !strings.Contains(menu, `href="/tags"`) || !strings.Contains(menu, ">3<") {
		t.Fatalf("the tag switcher lacks the way to the list, or the count of what has nightly: %s", menu)
	}

	// Ticked tags and typed ones are saved together.
	res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tag": {"nightly"}, "tags": {"blue"}})
	wantRedirect(t, res, settings+"#tags")
	if got := tagsOf(db.KindApp, web); !reflect.DeepEqual(got, []string{"blue", "nightly"}) {
		t.Fatalf("ticked and typed tags: %v", got)
	}
	res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tag": {"nightly", "not a tag"}})
	wantRedirect(t, res, settings+"#tags")
	if got := tagsOf(db.KindApp, web); !reflect.DeepEqual(got, []string{"blue", "nightly"}) {
		t.Fatalf("a ticked value that is no tag was not refused: %v", got)
	}

	// Sending none removes the app's tags; the tags themselves stay.
	res, _ = a.post(settings, "/apps/"+web+"/tags", url.Values{"tags": {""}})
	wantRedirect(t, res, settings+"#tags")
	res, body = a.get("/tags/frontend")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Nothing has this tag yet") || strings.Contains(body, "Deploy all") {
		t.Fatal("an empty tag's page does not say it is empty, or offers to deploy it")
	}

	// Deleting an app takes its tags along.
	res, _ = a.post(settings, "/apps/"+api+"/delete", url.Values{"confirm": {"api"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete app: %d", res.StatusCode)
	}
	a.waitGone(api)
	for _, m := range mustTags(t, a, team) {
		if m.Apps != 0 || (m.Tag == "nightly") != (m.Services == 1) {
			t.Fatalf("tags after deleting the app: %+v", m)
		}
	}
}

func mustTags(t *testing.T, a *app, team string) []db.TagCount {
	t.Helper()
	list, err := a.db.ListTags(context.Background(), team)
	if err != nil || len(list) == 0 {
		t.Fatalf("tags: %v %v", list, err)
	}
	return list
}

// A tag is made, renamed and deleted on its own, before anything has it.
func TestTagsAreMadeRenamedAndDeleted(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", false, nil)

	res, _ := a.post("/tags", "/tags", url.Values{"name": {" Nightly "}})
	wantRedirect(t, res, "/tags/nightly")
	res, body := a.get("/tags")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, `href="/tags/nightly"`) || !strings.Contains(body, "Nothing has it yet") {
		t.Fatal("the empty tag is not listed")
	}
	for name, want := range map[string]string{"nightly": "already has a tag called nightly", "not a tag": "Use lowercase letters", "": "Use lowercase letters"} {
		res, body = a.post("/tags", "/tags", url.Values{"name": {name}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) || !strings.Contains(body, "data-autoopen") {
			t.Errorf("making %q: status %d, want 422 with %q in the open dialog", name, res.StatusCode, want)
		}
	}
	// The app's tags dialog offers it, unticked.
	_, body = a.get("/apps/" + web + "/settings")
	if !strings.Contains(body, `name="tag" value="nightly"`) || regexp.MustCompile(`name="tag" value="nightly" checked`).MatchString(body) {
		t.Fatal("the tags dialog does not offer the team's tag unticked")
	}
	a.post("/apps/"+web+"/settings", "/apps/"+web+"/tags", url.Values{"tag": {"nightly"}})

	// A tag nothing has: its page says so, and the switcher too.
	a.post("/tags", "/tags", url.Values{"name": {"empty"}})
	if _, menu := a.get("/switch/tags?at=empty"); !regexp.MustCompile(`(?s)href="/tags/empty"[^>]*aria-selected="true".*?>empty<.*?>empty<`).MatchString(menu) {
		t.Fatalf("the switcher does not say that a tag nothing has is empty: %s", menu)
	}

	// Renaming takes the app along; a name in use, or one that is no tag, is refused.
	for name, want := range map[string]string{"empty": "already has a tag called empty", "No Good": "Use lowercase letters"} {
		res, body = a.post("/tags/nightly", "/tags/nightly", url.Values{"name": {name}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("renaming to %q: status %d, want 422 with %q", name, res.StatusCode, want)
		}
	}
	res, _ = a.post("/tags/nightly", "/tags/nightly", url.Values{"name": {"daily"}})
	wantRedirect(t, res, "/tags/daily")
	if got, _ := a.db.TagsOf(ctx, team, db.KindApp, web); !reflect.DeepEqual(got, []string{"daily"}) {
		t.Fatalf("the app's tags after the rename: %v", got)
	}
	res, _ = a.get("/tags/nightly")
	wantStatus(t, res, http.StatusNotFound)

	// Deleting takes the tag off the app and leaves the app.
	res, _ = a.post("/tags/daily", "/tags/daily/delete", nil)
	wantRedirect(t, res, "/tags")
	if got, _ := a.db.TagsOf(ctx, team, db.KindApp, web); len(got) != 0 {
		t.Fatalf("the app's tags after the delete: %v", got)
	}
	if _, err := a.db.App(ctx, team, web); err != nil {
		t.Fatalf("the app went with its tag: %v", err)
	}
	res, _ = a.post("/tags", "/tags/daily/delete", nil)
	wantStatus(t, res, http.StatusNotFound)

	// The API lists an empty tag with no resources.
	_, raw := a.call(http.MethodGet, "/api/v1/tags", a.newToken(db.AbilityRead))
	tags := decode[[]struct {
		Tag      string `json:"tag"`
		Apps     int    `json:"apps"`
		Services int    `json:"services"`
	}](t, raw)
	if len(tags) != 1 || tags[0].Tag != "empty" || tags[0].Apps != 0 || tags[0].Services != 0 {
		t.Fatalf("the API's tags: %+v", tags)
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
	res, _ = a.post("/tags", "/tags/nightly/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
	if _, menu := a.get("/switch/tags?at=nightly"); strings.Contains(menu, "nightly") {
		t.Fatal("the tag switcher lists another team's tag")
	}
	res, _ = a.post("/tags", "/tags/nightly", url.Values{"name": {"mine"}})
	wantStatus(t, res, http.StatusNotFound)
	if ok, _ := a.db.TagExists(ctx, "otherteam", "nightly"); !ok {
		t.Fatal("another team's tag was renamed or deleted")
	}
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
