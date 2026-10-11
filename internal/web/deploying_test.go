package web

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// noteRE is the note in the corner as a page holds it: where it asks, how
// often, and what it shows.
var noteRE = regexp.MustCompile(`(?s)<div id="deploying" class="deploying" aria-live="polite" hx-get="([^"]*)" hx-swap="outerHTML" hx-trigger="([^"]*)">(.*?)</div>`)

// note reads the note out of a page.
func note(t *testing.T, body string) (ask, every, shown string) {
	t.Helper()
	m := noteRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the page has no note of what is deploying:\n%.600s", body[max(0, len(body)-600):])
	}
	return html.UnescapeString(m[1]), m[2], m[3]
}

// Every signed-in page says in its corner what is being deployed now, each
// thing leading to where its deployment is seen, and nothing of another
// team. With nothing in progress it shows nothing and asks seldom.
func TestDeployingNowIsSaidOnEveryPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", true, nil)
	a.stackServer("front", "3000", nil)
	site := a.newService(projectID, env, "site", nil)
	maindb := a.newDatabase(projectID, env, "postgres", "maindb", nil)

	_, body := a.get("/")
	ask, every, shown := note(t, body)
	if every != "every 15s" || strings.TrimSpace(shown) != "" || !strings.HasPrefix(ask, "/deploying?seen=") {
		t.Fatalf("with nothing in progress the note asks %q %s and shows %q", ask, every, shown)
	}
	// What the page holds is what a poll would bring: nothing is sent.
	if res, _ := a.get(ask); res.StatusCode != http.StatusNoContent {
		t.Errorf("a poll for what the page holds: status %d, want 204", res.StatusCode)
	}

	// A deployment that waits, one that runs and a database that starts.
	queued, err := a.db.CreateDeployment(ctx, db.Deployment{AppID: web, Trigger: db.TriggerManual, Image: "nginx:alpine"})
	if err != nil {
		t.Fatal(err)
	}
	running, err := a.db.CreateServiceDeployment(ctx, site.ID, db.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.StartServiceDeployment(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE databases SET status = ? WHERE id = ?`, db.AppDeploying, maindb.ID); err != nil {
		t.Fatal(err)
	}
	// Another team's deployment is none of this page's.
	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	theirs, _ := a.db.CreateProject(ctx, "otherteam", "Theirs", "")
	theirEnvs, _ := a.db.ListEnvironments(ctx, theirs.ID)
	if _, err := a.db.Exec(`INSERT INTO databases (id, environment_id, server_id, name, engine, image, status, created_at, updated_at)
		SELECT 'theirdbaaaaa', ?, server_id, 'theirdb', engine, image, ?, 1, 1 FROM databases WHERE id = ?`, theirEnvs[0].ID, db.AppDeploying, maindb.ID); err != nil {
		t.Fatal(err)
	}

	// The poll that held nothing is answered now, with the note itself.
	res, fresh := a.get(ask)
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(fresh, "<html") {
		t.Fatal("the poll is answered with a page")
	}
	for _, page := range []string{"/", "/projects", a.appPath(web) + "/logs", "/account", "/nosuchpage"} {
		_, body = a.get(page)
		_, every, shown = note(t, body)
		if every != "every 3s" {
			t.Errorf("%s: with something in progress the note asks %s", page, every)
		}
		for _, want := range []string{
			`href="` + a.servicePath(site.ID) + `/deployments/` + running.ID + `"`,
			`href="` + a.databasePath(maindb.ID) + `"`,
			`href="` + a.appPath(web) + `/deployments/` + queued.ID + `"`,
			"Deploying", "Starting", "Queued", "site", "maindb", "web", `aria-label="Deploying now"`,
		} {
			if !strings.Contains(shown, want) {
				t.Errorf("%s: the note lacks %s", page, want)
			}
		}
		if strings.Contains(shown, "theirdb") || strings.Contains(shown, "more") {
			t.Errorf("%s: the note shows another team's database, or counts more than there are:\n%s", page, shown)
		}
		// What runs is said before what waits.
		if strings.Index(shown, "Queued") < strings.Index(shown, "Deploying") || strings.Index(shown, "Queued") < strings.Index(shown, "Starting") {
			t.Errorf("%s: what waits is listed before what runs", page)
		}
	}

	// More than it has rows for: the rest is counted, and leads to where
	// all of it is listed.
	api := a.newApp(projectID, env, "api", false, nil)
	if _, err := a.db.CreateDeployment(ctx, db.Deployment{AppID: api, Trigger: db.TriggerManual, Image: "nginx:alpine"}); err != nil {
		t.Fatal(err)
	}
	_, body = a.get("/")
	if _, _, shown = note(t, body); strings.Count(shown, `class="deploying-item"`) != 3 || !strings.Contains(shown, `href="/#home-activity">and 1 more</a>`) {
		t.Errorf("four in progress are not three rows and one more:\n%s", shown)
	}

	// A second deployment of the app that waits behind the first is not a
	// second row: the app is listed once, by the one whose turn is next.
	if _, err := a.db.CreateDeployment(ctx, db.Deployment{AppID: api, Trigger: db.TriggerManual, Image: "nginx:alpine"}); err != nil {
		t.Fatal(err)
	}
	if now, err := a.db.Deploying(ctx, firstTeam(t, a), 50); err != nil || len(now) != 4 {
		t.Errorf("in progress: %d rows (%v), want 4", len(now), err)
	}

	// When it is over the note is empty again.
	for _, q := range []string{`UPDATE deployments SET status = 'failed' WHERE status = 'queued'`, `UPDATE service_deployments SET status = 'success'`, `UPDATE databases SET status = 'running'`} {
		if _, err := a.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, body = a.get("/")
	if _, every, shown = note(t, body); every != "every 15s" || strings.TrimSpace(shown) != "" {
		t.Errorf("with everything over the note asks %s and shows %q", every, shown)
	}

	// Signed out there is nothing to ask.
	if res, _ := a.do(a.newClient(), mustGet(a.url+"/deploying")); res.StatusCode != http.StatusSeeOther {
		t.Errorf("signed out: status %d", res.StatusCode)
	}
}

// The note asks every few seconds on every page whether anybody is there
// or not. That must not count as the person doing something, or a tab left
// open would never be signed out.
func TestDeployingNoteDoesNotKeepASessionAlive(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	expires := func() int64 {
		var at int64
		if err := a.db.QueryRow(`SELECT expires_at FROM sessions`).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	// A session old enough that the next request would extend it.
	if _, err := a.db.Exec(`UPDATE sessions SET expires_at = expires_at - ?`, int64(sessionRefresh.Seconds())+60); err != nil {
		t.Fatal(err)
	}
	before := expires()
	if res, _ := a.get("/deploying"); res.StatusCode != http.StatusOK {
		t.Fatalf("the note: status %d", res.StatusCode)
	}
	if expires() != before {
		t.Fatal("the note's own question extended the session")
	}
	a.get("/projects")
	if expires() <= before {
		t.Fatal("a page the person opened did not extend the session: the test proves nothing")
	}
}
