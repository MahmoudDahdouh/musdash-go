package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// The Overview of a service says how its latest deployment went, and
// opens no stream: a deployment's log is read where it is asked for, on
// the deployment's own page.
func TestAServicesOverviewShowsTheProcessNotTheLog(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("front", "3000", nil)
	s := a.newService(projectID, env, "site", nil)
	base := a.servicePath(s.ID)

	// Before the first deployment.
	_, page := a.get(base)
	page = html.UnescapeString(page)
	wantAll(t, "the overview before a deployment", page, "Latest deployment", "Nothing has been deployed yet", `href="`+base+`/deployments"`)
	wantNone(t, "the overview before a deployment", page, "sse-connect", "deploy-log", `id="deploy-log"`)
	_, tab := a.get(base + "/deployments")
	wantAll(t, "the empty tab", html.UnescapeString(tab), "No deployments yet")

	res, _ := a.post(base, base+"/deploy", url.Values{})
	wantStatus(t, res, http.StatusSeeOther)
	if got := res.Header.Get("Location"); got != base {
		t.Fatalf("Deploy led to %s", got)
	}
	a.waitService(s.ID)
	list, err := a.db.ListServiceDeployments(ctx, s.ID, 10)
	if err != nil || len(list) != 1 || list[0].Status != db.DeploySuccess || list[0].Trigger != db.TriggerManual {
		t.Fatalf("deployments: %+v, %v", list, err)
	}
	dep := list[0]
	own := base + "/deployments/" + dep.ID

	// The card: the deployment in words, its stages all done, and the way
	// to its page. Still no stream.
	_, page = a.get(base)
	page = html.UnescapeString(page)
	wantAll(t, "the overview", page, "Latest deployment", "Deployed", "Succeeded", "Manual", `href="`+own+`"`, "All deployments",
		"Read the Compose file", "Pull the images", "Start the containers", `data-state="done"`,
		`hx-get="`+base+`/deployments/latest?seen=`, `hx-trigger="every 15s, stack-changed from:body"`)
	wantNone(t, "the overview", page, "sse-connect", "deploy-log", "Fetch the repository", `data-state="now"`, `data-state="waiting"`)

	// The card asks for itself, and is told when nothing is new.
	latest := between(t, base, page, base+`/deployments/latest?seen=`, `"`)
	if res, body := a.get(latest); res.StatusCode != http.StatusNoContent || body != "" {
		t.Errorf("the card's poll with nothing new: %d %q", res.StatusCode, body)
	}
	if res, body := a.get(base + "/deployments/latest?seen=other"); res.StatusCode != http.StatusOK || !strings.Contains(body, `id="latest-deployment"`) || strings.Contains(body, "<html") {
		t.Errorf("the card's poll with something new: %d\n%s", res.StatusCode, body)
	}

	// The tab lists it; its page has the status, the stages and the log.
	_, tab = a.get(base + "/deployments")
	wantAll(t, "the tab", html.UnescapeString(tab), `href="`+own+`"`, "Succeeded", "Manual", "View log", `aria-current="page"`)
	res, page = a.get(own)
	wantStatus(t, res, http.StatusOK)
	page = html.UnescapeString(page)
	wantAll(t, "the deployment's page", page, "Succeeded", "Start the containers", `sse-connect="`+own+`/stream"`)
	// Over: its status does not ask again.
	wantNone(t, "the deployment's page", page, own+"/status")
	res, stream := a.get(own + "/stream")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(stream, "Reading the Compose file") || !strings.Contains(stream, "Deployed.") || !strings.Contains(stream, "event: done") {
		t.Fatalf("the deployment's log:\n%s", stream)
	}
	if res, body := a.get(own + "/status"); res.StatusCode != http.StatusOK || !strings.Contains(body, `id="deployment-status"`) {
		t.Errorf("the status fragment: %d\n%s", res.StatusCode, body)
	}

	// One that failed says where on its page, and why. The Overview has the
	// reason once, in its notice.
	failed, _ := a.db.CreateServiceDeployment(ctx, s.ID, "push")
	a.db.StartServiceDeployment(ctx, failed.ID)
	a.db.SetServiceDeploymentStep(ctx, failed.ID, db.ServiceStepImages)
	a.db.FinishServiceDeployment(ctx, failed.ID, db.DeployFailed, "pull the images: no such image")
	a.db.SetServiceState(ctx, s.ID, db.AppRunning, "pull the images: no such image")
	_, page = a.get(base)
	page = html.UnescapeString(page)
	wantAll(t, "the overview after a failure", page, "Failed while pulling the images", `data-state="failed"`, `data-state="done"`, `data-state="waiting"`, "Push")
	if n := strings.Count(page, "no such image"); n != 1 {
		t.Errorf("the reason is on the overview %d times, want once", n)
	}
	_, page = a.get(base + "/deployments/" + failed.ID)
	wantAll(t, "the failed deployment's page", html.UnescapeString(page), "Pull the images: no such image", "Failed while pulling the images")

	// In progress: the card and the status ask again every two seconds.
	running, _ := a.db.CreateServiceDeployment(ctx, s.ID, db.TriggerManual)
	_, page = a.get(base)
	wantAll(t, "the overview with a waiting deployment", html.UnescapeString(page), "Waiting for its turn", "Queued", `hx-trigger="every 2s"`)
	a.db.StartServiceDeployment(ctx, running.ID)
	a.db.SetServiceDeploymentStep(ctx, running.ID, db.ServiceStepRead)
	_, page = a.get(base)
	wantAll(t, "the overview with a running deployment", html.UnescapeString(page), "Reading the Compose file", `data-state="now"`, `hx-trigger="every 2s"`)
	// Another one queued behind it: the card stays with the one that runs.
	behind, _ := a.db.CreateServiceDeployment(ctx, s.ID, "push")
	_, page = a.get(base)
	wantAll(t, "the overview with one running and one waiting", html.UnescapeString(page), "Reading the Compose file", `href="`+base+"/deployments/"+running.ID+`"`)
	wantNone(t, "the overview with one running and one waiting", html.UnescapeString(page), "Waiting for its turn", `href="`+base+"/deployments/"+behind.ID+`"`)
	// The card stands before the containers: it is what changes while
	// something is being deployed.
	if i, j := strings.Index(page, `id="latest-deployment"`), strings.Index(page, ">Containers<"); i < 0 || j < 0 || i > j {
		t.Errorf("the card is at %d and the containers at %d", i, j)
	}
	_, page = a.get(base + "/deployments/" + running.ID)
	wantAll(t, "a running deployment's page", html.UnescapeString(page), base+"/deployments/"+running.ID+"/status?seen=", `hx-trigger="every 2s"`)

	// A deployment of another service is not this one's.
	other := a.newService(projectID, env, "other", nil)
	theirs, _ := a.db.CreateServiceDeployment(ctx, other.ID, db.TriggerManual)
	for _, path := range []string{"", "/status", "/stream"} {
		if res, _ := a.get(base + "/deployments/" + theirs.ID + path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET another service's deployment%s: %d", path, res.StatusCode)
		}
	}
}

// wantNone fails if text holds any of the pieces.
func wantNone(t *testing.T, what, text string, pieces ...string) {
	t.Helper()
	for _, not := range pieces {
		if strings.Contains(text, not) {
			t.Errorf("%s holds %q", what, not)
		}
	}
}

// A stack from Git has the repository as its first stage, names its
// commit, and pulls and builds.
func TestAGitServicesDeploymentPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	s := a.newService(projectID, env, "stack", nil)
	if _, err := a.db.Exec(`UPDATE services SET template = ?, repo_url = 'https://github.com/acme/stack', repo_name = 'acme/stack', branch = 'main', compose_path = 'compose.yaml' WHERE id = ?`, db.TemplateGit, s.ID); err != nil {
		t.Fatal(err)
	}
	dep, _ := a.db.CreateServiceDeployment(ctx, s.ID, "push")
	a.db.StartServiceDeployment(ctx, dep.ID)
	a.db.SetServiceDeploymentStep(ctx, dep.ID, db.ServiceStepClone)
	base := a.servicePath(s.ID)
	_, page := a.get(base + "/deployments/" + dep.ID)
	page = html.UnescapeString(page)
	wantAll(t, "a deployment from Git", page, "Fetch the repository", "Pull and build the images", "Fetching the repository")
	a.db.SetServiceDeploymentCommit(ctx, dep.ID, "0123456789abcdef0123")
	a.db.FinishServiceDeployment(ctx, dep.ID, db.DeploySuccess, "")
	_, tab := a.get(base + "/deployments")
	wantAll(t, "the tab of a stack from Git", html.UnescapeString(tab), "Commit", "0123456789ab")
	wantNone(t, "the tab of a stack from Git", html.UnescapeString(tab), "0123456789abc")
}

// A service that was deployed before deployments were recorded has none
// on record. Its Overview does not call it undeployed.
func TestAServiceDeployedBeforeDeploymentsWereRecorded(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	s := a.newService(projectID, env, "site", nil)
	a.db.SetServiceMembers(ctx, s.ID, []string{"front", "db"})
	a.db.SetServiceState(ctx, s.ID, db.AppRunning, "")
	_, page := a.get(a.servicePath(s.ID))
	page = html.UnescapeString(page)
	wantAll(t, "the overview", page, "No deployment on record")
	wantNone(t, "the overview", page, "Not deployed yet", "sse-connect")
}

// Deleting a service removes the logs of its deployments, and the one log
// it had before each deployment had its own.
func TestDeletingAServiceRemovesItsDeploymentLogs(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.stackServer("front", "3000", nil)
	s := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	list, _ := a.db.ListServiceDeployments(ctx, s.ID, 10)
	if len(list) != 1 {
		t.Fatalf("deployments: %+v", list)
	}
	logs := []string{a.cfg.ServiceLogPath(list[0].ID), a.cfg.ServiceLogPath(s.ID)}
	if err := os.WriteFile(logs[1], []byte("from before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logs[0]); err != nil {
		t.Fatalf("the deployment has no log: %v", err)
	}
	base := a.servicePath(s.ID)
	res, _ := a.post(base+"/settings", base+"/delete", url.Values{"confirm": {"site"}})
	wantStatus(t, res, http.StatusSeeOther)
	for _, path := range logs {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s is still there", path)
		}
	}
}

// A log reaches the page in as many events as it was written in pieces,
// not one a line: each event is put into the page and the page laid out
// again, and a deployment's log has thousands of lines.
func TestALogIsSentInFewEvents(t *testing.T) {
	rec := httptest.NewRecorder()
	out := startSSE(rec)
	var text strings.Builder
	for i := 0; i < 1000; i++ {
		text.WriteString("line <b>&</b> of output\n")
	}
	// An empty line, one that ends in a carriage return, and one that is
	// cut for its length, in the middle of a piece.
	text.WriteString("\nredrawn\r\n" + strings.Repeat("x", maxLogLine+10) + "\n")
	// As a follower hands it over: in two pieces, the first ending in the
	// middle of a line.
	whole := text.String()
	cut := len(whole)/2 + 5
	if _, err := out.Write([]byte(whole[:cut])); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte(whole[cut:] + "the end")); err != nil {
		t.Fatal(err)
	}
	out.finish()
	body := rec.Body.String()
	if n := strings.Count(body, "event: line\n"); n != 3 {
		t.Errorf("%d events for two pieces and a last line, want 3", n)
	}
	// What the browser makes of it: the data lines of an event joined by
	// line breaks. It reads the same as the text that was written.
	var got strings.Builder
	for _, event := range strings.Split(body, "\n\n") {
		if !strings.HasPrefix(event, "event: line\n") {
			continue
		}
		var data []string
		for _, l := range strings.Split(strings.TrimPrefix(event, "event: line\n"), "\n") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(l, "data:"), " "))
		}
		got.WriteString(strings.Join(data, "\n"))
	}
	shown := strings.Replace(whole, "redrawn\r\n", "redrawn\n", 1)
	shown = strings.Replace(shown, strings.Repeat("x", maxLogLine+10), strings.Repeat("x", maxLogLine)+" …", 1)
	if want := html.EscapeString(shown + "the end\n"); got.String() != want {
		t.Errorf("the log reads differently:\n%.200q\nwant\n%.200q", got.String(), want)
	}
	if strings.Contains(body, "<b>") {
		t.Error("output was sent as markup")
	}
}
