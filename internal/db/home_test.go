package db

import (
	"context"
	"errors"
	"testing"
)

// homeTeams builds two teams, each with a project, a server and an app, so
// a query that forgot the team finds the other one's rows.
type homeTeams struct {
	team, other   string
	server        Server
	project       Project
	env           Environment
	web           App
	otherApp      App
	otherProject  Project
	otherServerID string
}

func newHomeTeams(t *testing.T, d *DB) homeTeams {
	t.Helper()
	ctx := context.Background()
	var h homeTeams
	var err error
	if _, h.team, err = d.CreateFirstUser(ctx, "a@example.com", "A", "hash"); err != nil {
		t.Fatal(err)
	}
	h.other, h.otherServerID = "teamb", "serverb"
	if _, err := d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('teamb', 'B', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at, metrics) VALUES ('serverb', 'teamb', 'theirs', 'ssh', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if h.server, err = d.EnsureLocalServer(ctx, h.team, ""); err != nil {
		t.Fatal(err)
	}
	if h.project, err = d.CreateProject(ctx, h.team, "Shop", ""); err != nil {
		t.Fatal(err)
	}
	envs, _ := d.ListEnvironments(ctx, h.project.ID)
	h.env = envs[0]
	if h.web, err = d.CreateApp(ctx, h.team, App{EnvironmentID: h.env.ID, ServerID: h.server.ID, Name: "web", Image: "nginx", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if h.otherProject, err = d.CreateProject(ctx, h.other, "Theirs", ""); err != nil {
		t.Fatal(err)
	}
	otherEnvs, _ := d.ListEnvironments(ctx, h.otherProject.ID)
	if h.otherApp, err = d.CreateApp(ctx, h.other, App{EnvironmentID: otherEnvs[0].ID, ServerID: h.otherServerID, Name: "theirapp", Image: "nginx", Port: 80}); err != nil {
		t.Fatal(err)
	}
	return h
}

// deployed records a finished deployment of an app at a given time.
func deployed(t *testing.T, d *DB, appID, status string, at int64) Deployment {
	t.Helper()
	ctx := context.Background()
	dep, err := d.CreateDeployment(ctx, Deployment{AppID: appID, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.FinishDeployment(ctx, dep.ID, status, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE deployments SET created_at = ? WHERE id = ?`, at, dep.ID); err != nil {
		t.Fatal(err)
	}
	return dep
}

func ranTask(t *testing.T, d *DB, taskID, status string, at int64) TaskRun {
	t.Helper()
	ctx := context.Background()
	run, err := d.CreateTaskRun(ctx, taskID, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.FinishTaskRun(ctx, run.ID, status, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE task_runs SET started_at = ? WHERE id = ?`, at, run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func backedUp(t *testing.T, d *DB, databaseID, status string, at int64) Backup {
	t.Helper()
	ctx := context.Background()
	b, err := d.CreateBackup(ctx, databaseID, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	b.Status = status
	if err := d.FinishBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE backups SET started_at = ? WHERE id = ?`, at, b.ID); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecentEvents(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	h := newHomeTeams(t, d)
	maindb, err := d.CreateDatabase(ctx, h.team, Database{EnvironmentID: h.env.ID, ServerID: h.server.ID, Name: "maindb", Engine: "postgres", Image: "postgres:17"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask(ctx, Task{AppID: h.web.ID, Name: "cleanup", Schedule: "* * * * *", Command: "true", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	first := deployed(t, d, h.web.ID, DeploySuccess, 100)
	second := deployed(t, d, h.web.ID, DeployFailed, 400)
	// A task that runs every minute and a database backed up every hour
	// must not push the deployments off the list: each is there once, with
	// its latest.
	ranTask(t, d, task.ID, RunFailed, 200)
	lastRun := ranTask(t, d, task.ID, RunSuccess, 300)
	backedUp(t, d, maindb.ID, RunSuccess, 150)
	lastBackup := backedUp(t, d, maindb.ID, RunFailed, 250)
	// What another team did is not ours to list.
	deployed(t, d, h.otherApp.ID, DeployFailed, 500)

	got, err := d.RecentEvents(ctx, h.team, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ kind, id string }{
		{EventDeployment, second.ID}, {EventTask, lastRun.ID}, {EventBackup, lastBackup.ID}, {EventDeployment, first.ID},
	}
	if len(got) != len(want) {
		t.Fatalf("%d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].ID != w.id {
			t.Errorf("event %d is %s %s, want %s %s", i, got[i].Kind, got[i].ID, w.kind, w.id)
		}
	}
	if e := got[0]; e.Status != DeployFailed || e.ResourceID != h.web.ID || e.Resource != "web" || e.ProjectID != h.project.ID || e.Project != "Shop" || e.Environment != DefaultEnvironment || e.At != 400 {
		t.Errorf("the deployment: %+v", e)
	}
	if e := got[1]; e.Detail != "cleanup" || e.TaskID != task.ID || e.ResourceID != h.web.ID {
		t.Errorf("the task run: %+v", e)
	}
	if e := got[2]; e.ResourceID != maindb.ID || e.Resource != "maindb" || e.Status != RunFailed {
		t.Errorf("the backup: %+v", e)
	}

	// The limit is on the whole list, newest first.
	if got, _ = d.RecentEvents(ctx, h.team, 2); len(got) != 2 || got[0].ID != second.ID || got[1].ID != lastRun.ID {
		t.Fatalf("limited to two: %+v", got)
	}
	if got, _ = d.RecentEvents(ctx, h.other, 10); len(got) != 1 || got[0].ResourceID != h.otherApp.ID {
		t.Fatalf("the other team's list: %+v", got)
	}
}

func TestActiveProjectsAndTotals(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	h := newHomeTeams(t, d)
	quiet, _ := d.CreateProject(ctx, h.team, "Archive", "")
	blank, _ := d.CreateProject(ctx, h.team, "Blank", "")
	d.Exec(`UPDATE projects SET created_at = 50 WHERE id = ?`, h.project.ID)
	d.Exec(`UPDATE projects SET created_at = 300 WHERE id = ?`, quiet.ID)
	d.Exec(`UPDATE projects SET created_at = 100 WHERE id = ?`, blank.ID)
	if _, err := d.CreateEnvironment(ctx, h.team, h.project.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	maindb, err := d.CreateDatabase(ctx, h.team, Database{EnvironmentID: h.env.ID, ServerID: h.server.ID, Name: "maindb", Engine: "postgres", Image: "postgres:17"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateService(ctx, h.team, Service{EnvironmentID: h.env.ID, ServerID: h.server.ID, Name: "blog", Template: TemplateCustom, Compose: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	d.SetAppStatus(ctx, h.web.ID, AppRunning)
	d.SetDatabaseState(ctx, maindb.ID, AppExited, "", "")

	// Shop is the oldest project, and the one something was last deployed in.
	deployed(t, d, h.web.ID, DeploySuccess, 900)
	deployed(t, d, h.otherApp.ID, DeploySuccess, 5000)

	got, err := d.ActiveProjects(ctx, h.team, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != h.project.ID || got[1].ID != quiet.ID || got[2].ID != blank.ID {
		t.Fatalf("order: %+v", got)
	}
	if p := got[0]; p.Name != "Shop" || p.EnvCount != 2 || p.Resources != 3 || p.Running != 1 || p.Down != 1 || p.ActiveAt != 900 {
		t.Errorf("Shop: %+v", p)
	}
	if p := got[2]; p.Resources != 0 || p.Running != 0 || p.Down != 0 || p.ActiveAt != 100 {
		t.Errorf("Blank: %+v", p)
	}
	if got, _ = d.ActiveProjects(ctx, h.team, 1); len(got) != 1 || got[0].ID != h.project.ID {
		t.Fatalf("limited to one: %+v", got)
	}

	tot, err := d.TeamTotals(ctx, h.team)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Totals{Projects: 3, Resources: 3, Running: 1}); tot != want {
		t.Fatalf("totals %+v, want %+v", tot, want)
	}
	if tot, _ = d.TeamTotals(ctx, h.other); tot != (Totals{Projects: 1, Resources: 1}) {
		t.Fatalf("the other team's totals: %+v", tot)
	}
	// A source counts once its app exists at GitHub, not while it is being made.
	src, _ := d.StartGitSource(ctx, h.team, "pending", "state")
	if tot, _ = d.TeamTotals(ctx, h.team); tot.Sources != 0 {
		t.Fatalf("an unfinished source was counted: %+v", tot)
	}
	src.AppID = 42
	if err := d.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if tot, _ = d.TeamTotals(ctx, h.team); tot.Sources != 1 {
		t.Fatalf("sources: %+v", tot)
	}
	// A GitLab source has no App id and counts from the start.
	if _, err := d.CreateGitLabSource(ctx, h.team, "work", "https://gitlab.example.com", "ada", "sealed"); err != nil {
		t.Fatal(err)
	}
	if tot, _ = d.TeamTotals(ctx, h.team); tot.Sources != 2 {
		t.Fatalf("sources with a GitLab one: %+v", tot)
	}
}

// A GitLab source is whole when it is made: it is listed, it is nobody's
// unfinished manifest flow, and the clean-up of those leaves it alone.
func TestGitLabSourceIsNeverPending(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	h := newHomeTeams(t, d)
	lab, err := d.CreateGitLabSource(ctx, h.team, "work", "https://gitlab.example.com:8443", "ada", "sealed-token")
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := d.StartGitSource(ctx, h.team, "half-made", "state-1")
	// The row carries a state of its own in no column, but ask as the
	// clean-up and the callback would.
	if _, err := d.Exec(`UPDATE git_sources SET state = 'state-2' WHERE id = ?`, lab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PendingGitSource(ctx, h.team, "state-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a GitLab source was found as a pending manifest flow: %v", err)
	}
	if got, err := d.PendingGitSource(ctx, h.team, "state-1"); err != nil || got.ID != pending.ID {
		t.Fatalf("the pending GitHub App: %+v, %v", got, err)
	}
	list, err := d.ListGitSources(ctx, h.team)
	if err != nil || len(list) != 1 || list[0].ID != lab.ID {
		t.Fatalf("listed: %+v, %v", list, err)
	}
	got := list[0]
	if got.Kind != GitSourceGitLab || !got.Ready() || got.ReportsPushes() || got.BaseURL != "https://gitlab.example.com:8443" ||
		got.Slug != "ada" || got.HTMLURL != "https://gitlab.example.com:8443/ada" || got.Token != "sealed-token" {
		t.Fatalf("the GitLab source: %+v", got)
	}
	if err := d.DeleteStaleGitSources(ctx, now()+10); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GitSource(ctx, h.team, lab.ID); err != nil {
		t.Fatalf("the clean-up of unfinished GitHub Apps removed a GitLab source: %v", err)
	}
	if _, err := d.GitSourceByID(ctx, pending.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the unfinished GitHub App was not cleaned up: %v", err)
	}
	// Another team does not see it.
	if _, err := d.GitSource(ctx, h.other, lab.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team loaded the source: %v", err)
	}
	// A kind the table does not know is refused by the table itself.
	if _, err := d.Exec(`INSERT INTO git_sources (id, team_id, name, kind, created_at) VALUES ('x', ?, 'x', 'bitbucket', 1)`, h.team); err == nil {
		t.Fatal("a source of an unknown kind was stored")
	}
}

func TestLatestServerSamples(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	h := newHomeTeams(t, d)
	if err := d.SetServerSampled(ctx, h.team, h.server.ID, true); err != nil {
		t.Fatal(err)
	}
	for at, cpu := range map[int64]int{100: 1000, 200: 2500} {
		if err := d.AddSamples(ctx, h.server.ID, at, map[string]Sample{"": {CPU: cpu, Mem: 1, MemTotal: 4}, h.web.ID: {CPU: 9999}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddSamples(ctx, h.otherServerID, 300, map[string]Sample{"": {CPU: 7}}); err != nil {
		t.Fatal(err)
	}

	got, err := d.LatestServerSamples(ctx, h.team, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := got[h.server.ID]; len(got) != 1 || !ok || s.At != 200 || s.CPU != 2500 || s.MemTotal != 4 {
		t.Fatalf("latest: %+v", got)
	}
	// A reading from before the given time is too old to be shown as now.
	if got, _ = d.LatestServerSamples(ctx, h.team, 201); len(got) != 0 {
		t.Fatalf("a stale reading: %+v", got)
	}
}
