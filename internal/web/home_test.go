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
// It has no list of what is wrong: that is said where the thing is, by a
// project's count and in Recent activity.
func TestHomeFollowsAnInstall(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()

	res, page := a.get("/")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"Getting started", `data-open="new-project"`, "Connect GitHub", "Nothing has been deployed, backed up or run yet.", "This machine"} {
		if !strings.Contains(page, want) {
			t.Errorf("a new install's Home lacks %q", want)
		}
	}
	if strings.Contains(page, `hx-get="/home/live`) {
		t.Fatal("a new install has nothing to wait for")
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
	for _, want := range []string{"Succeeded", `href="` + a.appPath(appID) + `/deployments/` + dep.ID + `"`, "1 of 1 running", "All 1 project"} {
		if !strings.Contains(page, want) {
			t.Errorf("Home after the first deployment lacks %q", want)
		}
	}
	// With nothing wrong, Home says nothing about attention at all.
	if strings.Contains(page, "Getting started") || strings.Contains(page, `id="home-attention"`) || strings.Contains(page, "Nothing needs attention") || strings.Contains(page, `hx-get="/home/live`) {
		t.Fatal("the steps stayed, a note about attention is shown, or the page keeps asking although nothing is in progress")
	}

	// The app stops on its own.
	if err := a.db.SetAppStatus(ctx, appID, db.AppExited); err != nil {
		t.Fatal(err)
	}
	_, page = a.get("/")
	for _, want := range []string{"1 down", "0 of 1 running"} {
		if !strings.Contains(page, want) {
			t.Errorf("Home with an app down lacks %q", want)
		}
	}
	for _, gone := range []string{`id="home-attention"`, "Needs attention", "It stopped on its own."} {
		if strings.Contains(page, gone) {
			t.Errorf("Home with an app down still has %q", gone)
		}
	}

	// It runs again, and a deployment of it fails: the old one serves.
	a.db.SetAppStatus(ctx, appID, db.AppRunning)
	failed, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: appID, Trigger: "manual"})
	a.db.FinishDeployment(ctx, failed.ID, db.DeployFailed, "no such image")
	if _, page = a.get("/"); !strings.Contains(page, `/deployments/`+failed.ID+`"`) || !strings.Contains(page, "Failed") || strings.Contains(page, `id="home-attention"`) {
		t.Fatal("a failed deployment is not in Recent activity, or Home lists what is wrong apart from it")
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

	// One of everything Home lists, each failed.
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

	_, page := a.get("/")
	rows := homeRowRE.FindAllStringSubmatch(page, -1)
	seen := map[string]bool{}
	for _, m := range rows {
		href := html.UnescapeString(m[1])
		if seen[href] {
			continue
		}
		seen[href] = true
		res, body := a.get(href)
		if res.StatusCode == http.StatusSeeOther {
			// A project's row: its page is its first environment's.
			res, body = a.get(res.Header.Get("Location"))
		}
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "<html") {
			t.Errorf("the row to %s leads to status %d, a whole page: %v", href, res.StatusCode, strings.Contains(body, "<html"))
		}
	}
	for _, want := range []string{a.appPath(appID) + "/deployments/" + dep.ID, a.databasePath(maindb.ID) + "/backups", a.appPath(appID) + "/tasks/" + task.ID, "/projects/" + projectID} {
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
		for _, secret := range []string{"Secret project", "secret-app", "secret-server", app.ID, dep.ID} {
			if strings.Contains(page, secret) {
				t.Errorf("%s shows %q of another team", path, secret)
			}
		}
		if !strings.Contains(page, "All 1 project") {
			t.Errorf("%s does not count the team's own project alone", path)
		}
	}
}

// What a server uses is shown from its last stored reading.
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
	if _, page := a.get("/"); !strings.Contains(page, "No recent reading of what it uses.") || strings.Contains(page, "19.0 GiB") {
		t.Fatal("an old reading was shown as the present one")
	}
	if err := a.db.AddSamples(ctx, server.ID, time.Now().Unix(), map[string]db.Sample{"": reading}); err != nil {
		t.Fatal(err)
	}
	_, page := a.get("/")
	for _, want := range []string{"42.5%", "1.0 GiB of 4.0 GiB", "19.0 GiB of 20.0 GiB"} {
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

// figureRE is one of the figures Home opens with: its label, where it
// leads (none for a card that is no link), and all it says.
var figureRE = regexp.MustCompile(`(?s)<li class="tile figure">.*?<h2 class="figure-label">(?:<a class="tile-link" href="([^"]*)">)?([^<]*).*?</h2>(.*?)</li>`)

// figures reads them from a page: what each says, by its label, and where
// each leads.
func figures(page string) (said, leads map[string]string) {
	said, leads = map[string]string{}, map[string]string{}
	for _, m := range figureRE.FindAllStringSubmatch(page, -1) {
		label := strings.TrimSpace(m[2])
		said[label] = regexp.MustCompile(`\s+`).ReplaceAllString(regexp.MustCompile(`(?s)<svg.*?</svg>|<[^>]*>`).ReplaceAllString(m[3], " "), " ")
		leads[label] = html.UnescapeString(m[1])
	}
	return said, leads
}

// Home opens with four figures, the four things a person opens it to find
// out: is everything up, did the deployments work, are the servers there,
// is the data safe. Each says how it stands and leads to where that is
// looked into. An install with nothing deployed has the first steps there.
func TestHomeOpensWithFourFigures(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()

	_, page := a.get("/")
	if said, _ := figures(page); len(said) != 0 || !strings.Contains(page, "Getting started") {
		t.Fatalf("an install with nothing deployed has figures %v, or no first steps", said)
	}

	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", true, nil)
	_, page = a.get("/")
	said, leads := figures(page)
	if len(said) != 4 {
		t.Fatalf("Home has %d figures, want 4: %v", len(said), said)
	}
	for label, want := range map[string][]string{
		"Running":       {"1 of 1", "All running", "Apps, databases and services"},
		"Deployments":   {"1 ", "All succeeded", "In the last 24 hours"},
		"Servers ready": {"1 of 1", "All ready"},
		"Last backup":   {"–", "No databases to back up"},
	} {
		for _, w := range want {
			if !strings.Contains(said[label], w) {
				t.Errorf("%s says %q, which lacks %q", label, said[label], w)
			}
		}
	}
	if leads["Running"] != "/projects" || leads["Deployments"] != "#home-activity" || leads["Servers ready"] != "/servers" || leads["Last backup"] != "" {
		t.Errorf("the figures lead to %v", leads)
	}
	// They are part of what Home asks for again while something runs.
	if live := between(t, "/", page, `<div id="home-live"`, `<section class="card" aria-labelledby="home-activity"`); !strings.Contains(live, `class="figures"`) {
		t.Error("the figures are not inside the part of Home that refreshes")
	}

	// Things go wrong, one of each kind.
	api := a.newApp(projectID, env, "api", false, nil)
	a.db.SetAppStatus(ctx, web, db.AppExited)
	dep, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: api, Trigger: "manual"})
	a.db.FinishDeployment(ctx, dep.ID, db.DeployFailed, "")
	running, _ := a.db.CreateDeployment(ctx, db.Deployment{AppID: api, Trigger: "manual"})
	a.db.StartDeployment(ctx, running.ID)
	remote, _ := a.remoteServer("edge")
	if _, err := a.db.Exec(`UPDATE servers SET status = ? WHERE id = ?`, db.ServerUnreachable, remote.ID); err != nil {
		t.Fatal(err)
	}
	maindb := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	a.newDatabase(projectID, env, "redis", "cache", nil)
	backup, _ := a.db.CreateBackup(ctx, maindb.ID, db.TriggerManual)
	backup.Status = db.RunFailed
	a.db.FinishBackup(ctx, backup)

	_, page = a.get("/")
	said, leads = figures(page)
	for label, want := range map[string][]string{
		"Running":       {"2 of 4", "1 down", "1 stopped or not deployed"},
		"Deployments":   {"3 ", "1 failed", "1 in progress now"},
		"Servers ready": {"1 of 2", "1 not ready"},
		"Last backup":   {"None yet", "1 failed", "0 of 2 databases on a schedule"},
	} {
		for _, w := range want {
			if !strings.Contains(said[label], w) {
				t.Errorf("%s says %q, which lacks %q", label, said[label], w)
			}
		}
	}
	// The failed backup's figure leads to the database it failed for, and
	// that page is there.
	if want := a.databasePath(maindb.ID) + "/backups"; leads["Last backup"] != want {
		t.Errorf("the backups figure leads to %q, want %q", leads["Last backup"], want)
	} else if res, _ := a.get(want); res.StatusCode != http.StatusOK {
		t.Errorf("%s: status %d", want, res.StatusCode)
	}
	// A flag is a pill: said in words, with an icon, not by colour alone.
	if !regexp.MustCompile(`<span class="pill pill-danger"><svg[^>]*>.*?</svg>\s*1 down\s*</span>`).MatchString(page) {
		t.Error("the flag of what is down is not a pill with an icon and words")
	}

	// A backup that worked, and a schedule: the figure says when, and that
	// one database still has none.
	good, _ := a.db.CreateBackup(ctx, maindb.ID, db.TriggerManual)
	good.Status = db.RunSuccess
	a.db.FinishBackup(ctx, good)
	if _, err := a.db.Exec(`INSERT INTO backup_configs (database_id, schedule, enabled) VALUES (?, '0 3 * * *', 1)`, maindb.ID); err != nil {
		t.Fatal(err)
	}
	_, page = a.get("/")
	said, leads = figures(page)
	for _, w := range []string{"Just now", "1 not scheduled", "1 of 2 databases on a schedule"} {
		if !strings.Contains(said["Last backup"], w) {
			t.Errorf("Last backup says %q, which lacks %q", said["Last backup"], w)
		}
	}
	if leads["Last backup"] != "#home-activity" {
		t.Errorf("with nothing failed the backups figure leads to %q", leads["Last backup"])
	}
}

// How full the fullest disk is, is what the Servers figure says under its
// count, from the readings Home already has: no server is asked.
func TestHomeServersFigureSaysTheFullestDisk(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", true, nil)

	_, page := a.get("/")
	if said, _ := figures(page); !strings.Contains(said["Servers ready"], "No recent reading of their disks") {
		t.Errorf("without a reading the figure says %q", said["Servers ready"])
	}
	local := a.localServerID()
	minute := time.Now().Unix() / 60 * 60
	if err := a.db.SetServerSampled(ctx, firstTeam(t, a), local, true); err != nil {
		t.Fatal(err)
	}
	if err := a.db.AddSamples(ctx, local, minute, map[string]db.Sample{"": {CPU: 100, Mem: 1 << 30, MemTotal: 2 << 30, DiskUsed: 92 << 30, DiskTotal: 100 << 30}}); err != nil {
		t.Fatal(err)
	}
	_, page = a.get("/")
	said, _ := figures(page)
	for _, w := range []string{"Disk 92% full", "Disk nearly full"} {
		if !strings.Contains(said["Servers ready"], w) {
			t.Errorf("Servers ready says %q, which lacks %q", said["Servers ready"], w)
		}
	}
}
