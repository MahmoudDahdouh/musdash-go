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

// asksAgain finds the address a fragment asks for itself at, in a page or
// in the fragment alone.
func asksAgain(t *testing.T, body, id string) string {
	t.Helper()
	tag := regexp.MustCompile(`<[a-z]+ [^>]*id="` + id + `"[^>]*>`).FindString(body)
	if tag == "" {
		t.Fatalf("no element %q in:\n%s", id, body)
	}
	m := regexp.MustCompile(`hx-get="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// TestAPollThatFindsNothingNewLeavesThePageAlone is what keeps a page still
// while something is in progress. A fragment that asked for itself every
// two seconds was put into the page again every two seconds, the same as
// it was: a spinner started over, and a button under the pointer was taken
// away and put back. The answer to a poll is 204 unless there is something
// new, and htmx swaps nothing for a 204.
func TestAPollThatFindsNothingNewLeavesThePageAlone(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	ctx := context.Background()

	// A deployment that nothing will run: it stays queued until the test
	// moves it on.
	dep, err := a.db.CreateDeployment(ctx, db.Deployment{AppID: appID, Trigger: "manual", Image: "nginx:alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetAppStatus(ctx, appID, db.AppDeploying); err != nil {
		t.Fatal(err)
	}

	res, page := a.get(a.appPath(appID) + "/deployments/" + dep.ID)
	wantStatus(t, res, http.StatusOK)
	status := asksAgain(t, page, "deployment-status")
	header := asksAgain(t, page, "app-header")
	for name, at := range map[string]string{"deployment-status": status, "app-header": header} {
		if !strings.Contains(at, "seen=") {
			t.Fatalf("%s does not say what it holds when it asks again: %q", name, at)
		}
	}

	// Nothing happened: neither has anything to put into the page.
	for _, at := range []string{status, header} {
		res, body := a.get(at)
		if res.StatusCode != http.StatusNoContent || body != "" {
			t.Fatalf("GET %s with nothing new: %d\n%s", at, res.StatusCode, body)
		}
	}

	// Asked without saying what the page holds, the fragment is answered.
	res, frag := a.get(a.appPath(appID) + "/deployments/" + dep.ID + "/status")
	wantStatus(t, res, http.StatusOK)
	if asksAgain(t, frag, "deployment-status") != status {
		t.Fatalf("the fragment alone asks at %q, the page's at %q", asksAgain(t, frag, "deployment-status"), status)
	}

	// The job starts: the status line has something new, and what it asks
	// with next is not what it asked with before.
	if err := a.db.StartDeployment(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	res, frag = a.get(status)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(frag, "Running") {
		t.Fatalf("the status line of a started deployment:\n%s", frag)
	}
	running := asksAgain(t, frag, "deployment-status")
	if running == status || !strings.Contains(running, "seen=") {
		t.Fatalf("a started deployment asks again at %q, as the queued one did at %q", running, status)
	}
	if res, body := a.get(running); res.StatusCode != http.StatusNoContent {
		t.Fatalf("GET %s with nothing new: %d\n%s", running, res.StatusCode, body)
	}

	// It fails: the answer says why, and asks no more.
	if err := a.db.FinishDeployment(ctx, dep.ID, db.DeployFailed, "The image could not be pulled."); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetAppStatus(ctx, appID, db.AppFailed); err != nil {
		t.Fatal(err)
	}
	res, frag = a.get(running)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(frag, "Failed") || !strings.Contains(frag, "The image could not be pulled.") {
		t.Fatalf("the status line of a failed deployment does not say why:\n%s", frag)
	}
	if at := asksAgain(t, frag, "deployment-status"); at != "" {
		t.Fatalf("a finished deployment still asks for its status, at %q", at)
	}

	// The header follows: no longer deploying, so it is sent, and it asks
	// at the slower pace with what it holds now.
	res, frag = a.get(header)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(frag, `hx-trigger="every 15s"`) {
		t.Fatalf("the header of an app that is not deploying:\n%s", frag)
	}
	settled := asksAgain(t, frag, "app-header")
	if res, body := a.get(settled); res.StatusCode != http.StatusNoContent {
		t.Fatalf("GET %s with nothing new: %d\n%s", settled, res.StatusCode, body)
	}
}

// TestAPolledHeaderStillReloadsThePageWhenItSettles pins the header that
// says more in its address than what it holds: a database's (and a
// service's) also says the status it was drawn with, and the page is loaded
// again when a start is over, because the rest of it was drawn before. An
// answer of 204 must not get in the way of that.
func TestAPolledHeaderStillReloadsThePageWhenItSettles(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	ctx := context.Background()
	setStatus := func(status string) {
		t.Helper()
		if _, err := a.db.ExecContext(ctx, `UPDATE databases SET status = ? WHERE id = ?`, status, m.ID); err != nil {
			t.Fatal(err)
		}
	}

	setStatus(db.AppDeploying)
	res, page := a.get(a.databasePath(m.ID))
	wantStatus(t, res, http.StatusOK)
	starting := asksAgain(t, page, "database-header")
	if !strings.Contains(starting, "?was="+db.AppDeploying+"&seen=") {
		t.Fatalf("the header of a starting database asks at %q", starting)
	}
	if res, body := a.get(starting); res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Refresh") != "" {
		t.Fatalf("GET %s with nothing new: %d, HX-Refresh %q\n%s", starting, res.StatusCode, res.Header.Get("HX-Refresh"), body)
	}

	setStatus(db.AppRunning)
	res, _ = a.get(starting)
	wantStatus(t, res, http.StatusOK)
	if res.Header.Get("HX-Refresh") != "true" {
		t.Fatal("the page of a database that has started is not loaded again")
	}
}
