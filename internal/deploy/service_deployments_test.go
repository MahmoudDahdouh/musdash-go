package deploy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// deployments are a service's deployments, newest first.
func (e *env) deployments(s db.Service) []db.ServiceDeployment {
	e.t.Helper()
	list, err := e.db.ListServiceDeployments(context.Background(), s.ID, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

// Every deployment of a service is a row with a log of its own: what asked
// for it, the parts it went through, and how it ended.
func TestAServicesDeploymentIsRecorded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, st := e.scriptedService(false)

	// The parts of the deployment, as a page would have seen them while it
	// ran: read when each command of the server is asked for.
	var steps []string
	inner := e.fake.Handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if list, _ := e.db.ListServiceDeployments(ctx, s.ID, 1); len(list) == 1 {
			if n := len(steps); n == 0 || steps[n-1] != list[0].Step {
				steps = append(steps, list[0].Step)
			}
		}
		return inner(line, c)
	}
	// The one log a service had before each deployment had its own.
	old := e.cfg.ServiceLogPath(s.ID)
	if err := os.WriteFile(old, []byte("from before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dep, err := e.d.EnqueueService(ctx, s, "push", false)
	if err != nil {
		t.Fatal(err)
	}
	if dep.Status != db.DeployQueued || dep.Trigger != "push" || dep.ServiceID != s.ID {
		t.Fatalf("a queued deployment: %+v", dep)
	}
	if got := e.settle(s, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%s: %s\n%s", got.Status, got.LastError, e.serviceLog(s))
	}
	list := e.deployments(s)
	if len(list) != 1 {
		t.Fatalf("deployments: %+v", list)
	}
	got := list[0]
	if got.ID != dep.ID || got.Status != db.DeploySuccess || got.Error != "" || got.StartedAt == 0 || got.FinishedAt == 0 || got.Step != db.ServiceStepStart {
		t.Fatalf("the deployment's row: %+v", got)
	}
	if want := []string{db.ServiceStepRead, db.ServiceStepImages, db.ServiceStepStart}; strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Errorf("the steps it went through: %q, want %q", steps, want)
	}
	raw, err := os.ReadFile(e.cfg.ServiceLogPath(dep.ID))
	if err != nil || !strings.Contains(string(raw), "Reading the Compose file") || !strings.Contains(string(raw), "Deployed.") {
		t.Fatalf("the deployment's own log: %v\n%s", err, raw)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the log from before deployments were recorded is still there")
	}

	// One that fails keeps the part it failed in, and why.
	st.fail = func(line string) error {
		if strings.Contains(line, " pull --ignore-buildable") {
			return &runner.ExitError{Name: "docker", Code: 1}
		}
		return nil
	}
	second, err := e.d.EnqueueService(ctx, s, db.TriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	after := e.settle(s, 10*time.Second)
	list = e.deployments(s)
	if len(list) != 2 || list[0].ID != second.ID {
		t.Fatalf("deployments: %+v", list)
	}
	if got := list[0]; got.Status != db.DeployFailed || got.Step != db.ServiceStepImages || got.Error != after.LastError || !strings.Contains(got.Error, "pull the images") || got.FinishedAt == 0 {
		t.Fatalf("the failed deployment's row: %+v", got)
	}
	// The first one's log is still its own.
	if raw, _ := os.ReadFile(e.cfg.ServiceLogPath(dep.ID)); !strings.Contains(string(raw), "Deployed.") {
		t.Errorf("the first deployment's log was written over:\n%s", raw)
	}
	if raw, _ := os.ReadFile(e.cfg.ServiceLogPath(second.ID)); !strings.Contains(string(raw), "Failed: pull the images") {
		t.Errorf("the second deployment's log:\n%s", raw)
	}
}

// A stack from Git has one part more, and its row names the commit.
func TestAGitServicesDeploymentNamesItsCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://github.com/acme/stack", RepoName: "Acme/Stack", Branch: "main", ComposePath: "deploy/compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &gitStack{project: ServiceProject(s.ID), links: map[string]bool{}, compose: gitComposeFile}
	inner := g.handle(e)
	first := ""
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if list, _ := e.db.ListServiceDeployments(ctx, s.ID, 1); len(list) == 1 && first == "" {
			first = list[0].Step
		}
		return inner(line, c)
	}
	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	list := e.deployments(s)
	if len(list) != 1 || list[0].Status != db.DeploySuccess || list[0].CommitSHA == "" || list[0].CommitSHA != got.Commit {
		t.Fatalf("deployment %+v of a stack at commit %q", list, got.Commit)
	}
	if first != db.ServiceStepClone {
		t.Errorf("the first part of a deployment from Git was %q", first)
	}
}

// A second deployment asked for while the first runs has a row of its own,
// and a service that is busy answers a plain request with no row at all.
func TestDeploymentsQueuedBehindOneAnother(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	// Held by something else, the service's lock keeps both waiting.
	mu := e.d.lockFor(s.ID)
	mu.Lock()
	defer mu.Unlock()
	if _, err := e.d.EnqueueService(ctx, s, db.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.EnqueueService(ctx, s, db.TriggerManual, false); err != ErrBusy {
		t.Fatalf("a deployment of a service that is being deployed: %v", err)
	}
	if list := e.deployments(s); len(list) != 1 {
		t.Fatalf("a refused deployment left a row: %+v", list)
	}
	if _, err := e.d.EnqueueService(ctx, s, "push", true); err != nil {
		t.Fatal(err)
	}
	list := e.deployments(s)
	if len(list) != 2 || list[0].Trigger != "push" || list[0].Status != db.DeployQueued || list[1].Status != db.DeployQueued {
		t.Fatalf("two waiting deployments: %+v", list)
	}
}

// A job queued by the version before deployments were recorded names
// none. It is not lost: it gets its row when it runs.
func TestAJobWithoutADeploymentMakesOne(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	if err := e.d.runServiceJob(ctx, []byte(`{"id":"`+s.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	list := e.deployments(s)
	if len(list) != 1 || list[0].Status != db.DeploySuccess || list[0].Trigger != "" {
		t.Fatalf("deployments: %+v", list)
	}
}

// Stop pressed while a deployment waited: the deployment ends, having
// deployed nothing, and says so. The service stays stopped.
func TestADeploymentOfAStoppedServiceEnds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	dep, err := e.db.CreateServiceDeployment(ctx, s.ID, db.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetServiceState(ctx, s.ID, db.AppStopped, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.d.runServiceJob(ctx, []byte(`{"id":"`+s.ID+`","deployment_id":"`+dep.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := e.db.ServiceDeploymentByID(ctx, dep.ID)
	if got.Status != db.DeployFailed || !strings.Contains(got.Error, "stopped before") || got.FinishedAt == 0 {
		t.Fatalf("the deployment: %+v", got)
	}
	if svc, _ := e.db.ServiceByID(ctx, s.ID); svc.Status != db.AppStopped || svc.LastError != "" {
		t.Fatalf("the service: %s %q", svc.Status, svc.LastError)
	}
}

// The oldest deployments go, and their logs with them.
func TestOldServiceDeploymentsArePruned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	// A history that is full already, each with its log.
	var oldest string
	for i := 0; i < keepDeployments; i++ {
		dep, err := e.db.CreateServiceDeployment(ctx, s.ID, db.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		e.db.FinishServiceDeployment(ctx, dep.ID, db.DeploySuccess, "")
		e.db.Exec(`UPDATE service_deployments SET created_at = ? WHERE id = ?`, 100+i, dep.ID)
		if err := os.WriteFile(e.cfg.ServiceLogPath(dep.ID), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = dep.ID
		}
	}
	if got := e.deployService(s, 10*time.Second); got.Status != db.AppRunning {
		t.Fatalf("%s: %s", got.Status, got.LastError)
	}
	list := e.deployments(s)
	if len(list) != keepDeployments {
		t.Fatalf("%d deployments are kept, want %d", len(list), keepDeployments)
	}
	for _, d := range list {
		if d.ID == oldest {
			t.Fatal("the oldest deployment was kept")
		}
	}
	if _, err := os.Stat(e.cfg.ServiceLogPath(oldest)); err == nil {
		t.Error("the pruned deployment's log is still there")
	}
}

// A write that fails in the middle of the job must not leave the record
// waiting and the service "deploying": its page offers nothing while it
// is, and the job is not tried again.
func TestAJobThatCannotRecordItsStartEndsTheDeployment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	// The database refuses to mark a deployment as running, and nothing
	// else.
	if _, err := e.db.Exec(`CREATE TRIGGER refuse_start BEFORE UPDATE ON service_deployments
		WHEN NEW.status = 'running' BEGIN SELECT RAISE(ABORT, 'the disk is full'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(e.fake.Calls())
	dep, err := e.d.EnqueueService(ctx, s, db.TriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.settle(s, 10*time.Second)
	if got.Status == db.AppDeploying || !strings.Contains(got.LastError, "the disk is full") {
		t.Fatalf("the service: %s %q", got.Status, got.LastError)
	}
	row, _ := e.db.ServiceDeploymentByID(ctx, dep.ID)
	if row.Status != db.DeployFailed || !strings.Contains(row.Error, "stopped before it had finished") || !strings.Contains(row.Error, "the disk is full") || row.FinishedAt == 0 {
		t.Fatalf("the deployment: %+v", row)
	}
	for _, call := range e.fake.Calls()[before:] {
		if strings.Contains(call, " up ") || strings.Contains(call, " pull ") {
			t.Fatalf("a deployment that could not be started ran %s", call)
		}
	}
}

// A deployment that musdash's own shutdown interrupts runs again. Until
// then its record waits, as its job does, and names no part.
func TestAnInterruptedDeploymentWaitsAgain(t *testing.T) {
	e := newEnv(t)
	s, st := e.scriptedService(false)
	dep, err := e.db.CreateServiceDeployment(context.Background(), s.ID, db.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	ctx, shutDown := context.WithCancel(context.Background())
	defer shutDown()
	st.fail = func(line string) error {
		if strings.Contains(line, " pull --ignore-buildable") {
			shutDown()
			return context.Canceled
		}
		return nil
	}
	if err := e.d.runServiceJob(ctx, []byte(`{"id":"`+s.ID+`","deployment_id":"`+dep.ID+`"}`)); err == nil {
		t.Fatal("an interrupted job reports that it is done")
	}
	row, _ := e.db.ServiceDeploymentByID(context.Background(), dep.ID)
	if row.Status != db.DeployQueued || row.Step != "" || row.StartedAt != 0 || row.Error != "" {
		t.Fatalf("the interrupted deployment: %+v", row)
	}
	// Run again, it goes through.
	st.fail = nil
	if err := e.d.runServiceJob(context.Background(), []byte(`{"id":"`+s.ID+`","deployment_id":"`+dep.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if row, _ = e.db.ServiceDeploymentByID(context.Background(), dep.ID); row.Status != db.DeploySuccess {
		t.Fatalf("the deployment run again: %+v", row)
	}
}
