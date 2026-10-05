package web

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// A new install is shown where to start, and Home follows it from there:
// the first project, the first deployment, the first thing that goes wrong.
func TestHomeFollowsAnInstall(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()

	res, page := a.get("/")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"Getting started", `href="/projects/new"`, "Connect GitHub", "Nothing has been deployed, backed up or run yet.", "This machine"} {
		if !strings.Contains(page, want) {
			t.Errorf("a new install's Home lacks %q", want)
		}
	}
	if strings.Contains(page, "Needs attention") || strings.Contains(page, "Nothing needs attention") || strings.Contains(page, `hx-get="/home/live`) {
		t.Fatal("a new install has nothing to say about attention, and nothing to wait for")
	}
	// The sidebar says where the reader is.
	if !strings.Contains(page, `href="/" aria-current="page"`) || strings.Contains(page, `href="/projects" aria-current="page"`) {
		t.Fatal("Home is not the current item of the sidebar")
	}
	if _, projects := a.get("/projects"); !strings.Contains(projects, `href="/projects" aria-current="page"`) {
		t.Fatal("Projects is not the current item on its own page")
	}

	projectID, env := a.project("Shop")
	if _, page = a.get("/"); !strings.Contains(page, "· Done") || !strings.Contains(page, `href="/projects/`+projectID+`"`) || !strings.Contains(page, "Open Shop") {
		t.Fatal("the first project did not move the steps on")
	}

	appID := a.newApp(projectID, env, "web", true, nil)
	dep := a.waitDeployed(appID)
	_, page = a.get("/")
	for _, want := range []string{"Nothing needs attention.", "Everything that is deployed here is running.", "Succeeded", `href="/apps/` + appID + `/deployments/` + dep.ID + `"`, "1 of 1 running", "All 1 project"} {
		if !strings.Contains(page, want) {
			t.Errorf("Home after the first deployment lacks %q", want)
		}
	}
	if strings.Contains(page, "Getting started") || strings.Contains(page, `hx-get="/home/live`) {
		t.Fatal("the steps stayed, or the page keeps asking although nothing is in progress")
	}

	// The app stops on its own.
	if err := a.db.SetAppStatus(ctx, appID, db.AppExited); err != nil {
		t.Fatal(err)
	}
	_, page = a.get("/")
	for _, want := range []string{"Needs attention", "It stopped on its own.", `href="/apps/` + appID + `"`, "1 down", "0 of 1 running"} {
		if !strings.Contains(page, want) {
			t.Errorf("Home with an app down lacks %q", want)
		}
	}
	if strings.Contains(page, "Nothing needs attention") {
		t.Fatal("an app is down and Home says nothing needs attention")
	}

	// It runs again, and a deployment of it fails: the old one serves.
	a.db.SetAppStatus(ctx, appID, db.AppRunning)
	failed, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: appID, Trigger: "manual"})
	a.db.FinishDeployment(ctx, failed.ID, db.DeployFailed, "no such image")
	if _, page = a.get("/"); !strings.Contains(page, "The last deployment failed. The one before it is still serving.") || !strings.Contains(page, `/deployments/`+failed.ID+`"`) {
		t.Fatal("a failed deployment of a running app is not under Needs attention")
	}
}

var homeRowRE = regexp.MustCompile(`<a class="row[^>]*href="([^"]+)"`)

// Every row on Home leads to a page on which the thing can be looked
// into: not to a 404, and not to a part of a page.
func TestHomeRowsLeadToPages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	maindb := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	a.waitDatabase(maindb.ID)

	// One of everything Home lists, each failed so that it is listed twice:
	// under Needs attention and under Recent activity.
	dep, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: appID, Trigger: "manual"})
	a.db.FinishDeployment(ctx, dep.ID, db.DeployFailed, "")
	backup, _ := a.db.CreateBackup(ctx, maindb.ID, db.TriggerSchedule)
	backup.Status = db.RunFailed
	a.db.FinishBackup(ctx, backup)
	task, err := a.db.CreateTask(ctx, db.Task{AppID: appID, Name: "cleanup", Schedule: "@daily", Command: "true", Enabled: true, NextRun: time.Now().Add(24 * time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := a.db.CreateTaskRun(ctx, task.ID, db.TriggerSchedule)
	a.db.FinishTaskRun(ctx, run.ID, db.RunFailed, 1, "")
	a.db.SetDatabaseState(ctx, maindb.ID, db.AppExited, "", "")

	_, page := a.get("/")
	for _, want := range []string{"The last deployment failed.", "The last backup failed.", "The scheduled task cleanup failed when it last ran.", "It stopped on its own."} {
		if !strings.Contains(page, want) {
			t.Errorf("Home lacks %q", want)
		}
	}
	rows := homeRowRE.FindAllStringSubmatch(page, -1)
	seen := map[string]bool{}
	for _, m := range rows {
		href := html.UnescapeString(m[1])
		if seen[href] {
			continue
		}
		seen[href] = true
		res, body := a.get(href)
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "<html") {
			t.Errorf("the row to %s leads to status %d, a whole page: %v", href, res.StatusCode, strings.Contains(body, "<html"))
		}
	}
	for _, want := range []string{"/apps/" + appID + "/deployments/" + dep.ID, "/databases/" + maindb.ID + "/backups", "/apps/" + appID + "/tasks/" + task.ID, "/databases/" + maindb.ID, "/projects/" + projectID} {
		if !seen[want] {
			t.Errorf("no row leads to %s: %v", want, seen)
		}
	}
}

// While something is in progress Home asks for itself again, and stops
// once nothing is.
func TestHomeAsksAgainOnlyWhileSomethingIsInProgress(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	// A deployment nobody runs: no job was queued for it.
	dep, err := a.db.CreateDeployment(ctx, db.Deployment{AppID: appID, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}

	_, page := a.get("/")
	if !strings.Contains(page, `hx-get="/home/live?n=1"`) || !strings.Contains(page, `hx-trigger="every 5s"`) || !strings.Contains(page, "Queued") {
		t.Fatal("Home does not ask again while a deployment waits")
	}
	a.db.StartDeployment(ctx, dep.ID)
	res, part := a.get("/home/live?n=1")
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(part, "<html") || !strings.Contains(part, `id="home-live"`) || !strings.Contains(part, `hx-get="/home/live?n=2"`) || !strings.Contains(part, "In progress") {
		t.Fatalf("the answer is not the part of the page that was asked for:\n%s", part)
	}
	// A page left open does not ask for ever.
	if _, part = a.get("/home/live?n=" + "120"); strings.Contains(part, "hx-get") {
		t.Fatal("Home keeps asking after it has asked often enough")
	}
	a.db.FinishDeployment(ctx, dep.ID, db.DeploySuccess, "")
	if _, part = a.get("/home/live?n=2"); strings.Contains(part, "hx-get") || !strings.Contains(part, "Succeeded") {
		t.Fatal("Home keeps asking although nothing is in progress")
	}
}

// Home is about the reader's team and nobody else's.
func TestHomeShowsOnlyTheTeamsOwn(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", true, nil)

	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at, status) VALUES ('theirserver', 'otherteam', 'secret-server', 'ssh', 1, 'unreachable')`); err != nil {
		t.Fatal(err)
	}
	theirs, err := a.db.CreateProject(ctx, "otherteam", "Secret project", "")
	if err != nil {
		t.Fatal(err)
	}
	envs, _ := a.db.ListEnvironments(ctx, theirs.ID)
	app, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "theirserver", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	a.db.SetAppStatus(ctx, app.ID, db.AppExited)
	dep, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: app.ID, Trigger: "manual"})
	a.db.FinishDeployment(ctx, dep.ID, db.DeployFailed, "")

	for _, path := range []string{"/", "/home/live"} {
		_, page := a.get(path)
		for _, secret := range []string{"Secret project", "secret-app", "secret-server", app.ID, dep.ID, "Needs attention"} {
			if strings.Contains(page, secret) {
				t.Errorf("%s shows %q of another team", path, secret)
			}
		}
		if !strings.Contains(page, "All 1 project") {
			t.Errorf("%s does not count the team's own project alone", path)
		}
	}
}

// What a server uses is shown from its last stored reading, and a disk
// that is nearly full is said out loud.
func TestHomeShowsAServersLastReading(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	server, err := a.db.EnsureLocalServer(ctx, team, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, page := a.get("/"); !strings.Contains(page, "No recent reading of what it uses.") || !strings.Contains(page, `href="/servers/`+server.ID+`/metrics"`) {
		t.Fatal("a server that is not sampled does not say so")
	}
	if err := a.db.SetServerSampled(ctx, team, server.ID, true); err != nil {
		t.Fatal(err)
	}
	const gib = 1 << 30
	reading := db.Sample{CPU: 4250, Mem: 1 * gib, MemTotal: 4 * gib, DiskUsed: 19 * gib, DiskTotal: 20 * gib}
	// One from an hour ago is not what the server uses now.
	if err := a.db.AddSamples(ctx, server.ID, time.Now().Add(-time.Hour).Unix(), map[string]db.Sample{"": reading}); err != nil {
		t.Fatal(err)
	}
	if _, page := a.get("/"); !strings.Contains(page, "No recent reading of what it uses.") || strings.Contains(page, "Needs attention") {
		t.Fatal("an old reading was shown as the present one")
	}
	if err := a.db.AddSamples(ctx, server.ID, time.Now().Unix(), map[string]db.Sample{"": reading}); err != nil {
		t.Fatal(err)
	}
	_, page := a.get("/")
	for _, want := range []string{"42.5%", "1.0 GiB of 4.0 GiB", "19.0 GiB of 20.0 GiB", "Needs attention", "Its disk is nearly full: 19.0 GiB of 20.0 GiB.", "Disk nearly full"} {
		if !strings.Contains(page, want) {
			t.Errorf("Home lacks %q of the server's reading", want)
		}
	}
}

// A Member cannot connect a source, so is not shown that step.
func TestHomeLeavesOutWhatAMemberCannotDo(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	m := a.newPerson("Member", db.RoleMember)
	res, page := m.get("/")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "Getting started") || strings.Contains(page, "Connect GitHub") {
		t.Fatal("a Member's steps are not the ones a Member can take")
	}
}
