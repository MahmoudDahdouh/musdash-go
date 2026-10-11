package db

import (
	"context"
	"errors"
	"testing"
)

// blogOf makes a service to deploy in the fixture's environment.
func blogOf(t *testing.T, d *DB, h homeTeams) Service {
	t.Helper()
	s, err := d.CreateService(context.Background(), h.team, Service{EnvironmentID: h.env.ID, ServerID: h.server.ID, Name: "blog", Template: TemplateCustom, Compose: "services: {}"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAServiceDeploymentsLife(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	blog := blogOf(t, d, newHomeTeams(t, d))

	if _, err := d.CurrentServiceDeployment(ctx, blog.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the current deployment of a service that has none: %v", err)
	}
	dep, err := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID == "" || dep.Status != DeployQueued || dep.Trigger != TriggerManual || dep.CreatedAt == 0 || dep.Over() {
		t.Fatalf("a new deployment: %+v", dep)
	}

	if err := d.StartServiceDeployment(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceDeploymentStep(ctx, dep.ID, ServiceStepImages); err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceDeploymentCommit(ctx, dep.ID, "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	got, err := d.ServiceDeployment(ctx, blog.ID, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != DeployRunning || got.StartedAt == 0 || got.Step != ServiceStepImages || got.CommitSHA != "0123456789abcdef" || got.Over() {
		t.Fatalf("a started deployment: %+v", got)
	}

	if err := d.FinishServiceDeployment(ctx, dep.ID, DeployFailed, "no such image"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ServiceDeploymentByID(ctx, dep.ID)
	// The step stays where it failed: the page says which part that was.
	if got.Status != DeployFailed || got.Error != "no such image" || got.FinishedAt == 0 || got.Step != ServiceStepImages || !got.Over() {
		t.Fatalf("a failed deployment: %+v", got)
	}

	// Started a second time, as a job that was requeued does: it begins
	// again from nothing.
	if err := d.StartServiceDeployment(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = d.ServiceDeploymentByID(ctx, dep.ID); got.Status != DeployRunning || got.Step != "" || got.Error != "" || got.FinishedAt != 0 {
		t.Fatalf("a deployment started again: %+v", got)
	}

	// Interrupted, it waits again and names no part; one that is over is
	// left as it ended.
	d.SetServiceDeploymentStep(ctx, dep.ID, ServiceStepStart)
	if err := d.RequeueServiceDeployment(ctx, dep.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = d.ServiceDeploymentByID(ctx, dep.ID); got.Status != DeployQueued || got.Step != "" || got.StartedAt != 0 {
		t.Fatalf("a deployment that waits again: %+v", got)
	}
	d.FinishServiceDeployment(ctx, dep.ID, DeploySuccess, "")
	d.RequeueServiceDeployment(ctx, dep.ID)
	if got, _ = d.ServiceDeploymentByID(ctx, dep.ID); got.Status != DeploySuccess {
		t.Fatalf("a finished deployment was made to wait: %+v", got)
	}
	// One that cannot be recorded is no deployment at all.
	if none, err := d.CreateServiceDeployment(ctx, "no-such-service", TriggerManual); err == nil || none.ID != "" {
		t.Fatalf("a deployment of a service that does not exist: %+v, %v", none, err)
	}

	// Asked for under another service, it is not there.
	other, err := d.CreateService(ctx, blog.teamOf(t, d), Service{EnvironmentID: blog.EnvironmentID, ServerID: blog.ServerID, Name: "shop", Template: TemplateCustom})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ServiceDeployment(ctx, other.ID, dep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deployment under a service it is not of: %v", err)
	}
}

// teamOf is the team a service belongs to.
func (s Service) teamOf(t *testing.T, d *DB) string {
	t.Helper()
	var team string
	if err := d.QueryRow(`SELECT p.team_id FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = ?`, s.EnvironmentID).Scan(&team); err != nil {
		t.Fatal(err)
	}
	return team
}

func TestServiceDeploymentsAreListedNewestFirst(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	blog := blogOf(t, d, newHomeTeams(t, d))
	var ids []string
	for i := 0; i < 3; i++ {
		dep, err := d.CreateServiceDeployment(ctx, blog.ID, "push")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, dep.ID)
	}
	// Two in one second: the later row is the newer one.
	d.Exec(`UPDATE service_deployments SET created_at = 100 WHERE id IN (?, ?)`, ids[0], ids[1])
	d.Exec(`UPDATE service_deployments SET created_at = 200 WHERE id = ?`, ids[2])

	list, err := d.ListServiceDeployments(ctx, blog.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != ids[2] || list[1].ID != ids[1] || list[2].ID != ids[0] {
		t.Fatalf("order: %+v", list)
	}
	if list, _ = d.ListServiceDeployments(ctx, blog.ID, 2); len(list) != 2 {
		t.Fatalf("limited to two: %+v", list)
	}
	// With none running, the current one is the newest. One that runs is
	// the current one, though another waits behind it.
	current, err := d.CurrentServiceDeployment(ctx, blog.ID)
	if err != nil || current.ID != ids[2] {
		t.Fatalf("the current one with none running: %+v, %v", current, err)
	}
	d.StartServiceDeployment(ctx, ids[1])
	d.Exec(`UPDATE service_deployments SET created_at = 100 WHERE id = ?`, ids[1])
	if current, err = d.CurrentServiceDeployment(ctx, blog.ID); err != nil || current.ID != ids[1] {
		t.Fatalf("the current one with one running: %+v, %v", current, err)
	}
	all, err := d.ServiceDeploymentIDs(ctx, blog.ID)
	if err != nil || len(all) != 3 {
		t.Fatalf("every id: %v, %v", all, err)
	}
}

func TestPruneServiceDeploymentsKeepsTheNewestAndTheUnfinished(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	blog := blogOf(t, d, newHomeTeams(t, d))
	var ids []string
	for i := 0; i < 5; i++ {
		dep, _ := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
		d.Exec(`UPDATE service_deployments SET created_at = ? WHERE id = ?`, 100+i, dep.ID)
		ids = append(ids, dep.ID)
	}
	// The oldest never finished; the others did.
	for _, id := range ids[1:] {
		d.FinishServiceDeployment(ctx, id, DeploySuccess, "")
	}
	gone, err := d.PruneServiceDeployments(ctx, blog.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 || gone[0] != ids[2] || gone[1] != ids[1] {
		t.Fatalf("removed %v, want the two oldest finished ones of %v", gone, ids)
	}
	left, _ := d.ServiceDeploymentIDs(ctx, blog.ID)
	if len(left) != 3 {
		t.Fatalf("left: %v", left)
	}
	if _, err := d.ServiceDeploymentByID(ctx, ids[0]); err != nil {
		t.Fatalf("a deployment that has not finished was pruned: %v", err)
	}
}

func TestFailStaleServiceDeployments(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	blog := blogOf(t, d, newHomeTeams(t, d))
	lost, _ := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
	running, _ := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
	d.StartServiceDeployment(ctx, running.ID)
	waiting, _ := d.CreateServiceDeployment(ctx, blog.ID, "push")
	done, _ := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
	d.FinishServiceDeployment(ctx, done.ID, DeploySuccess, "")

	// Jobs as the queue holds them: one for the waiting deployment, and one
	// queued before deployments were recorded, whose payload names none.
	// That one must not hide the stale rows.
	for id, payload := range map[string]string{
		"job-named":   `{"id":"` + blog.ID + `","deployment_id":"` + waiting.ID + `"}`,
		"job-unnamed": `{"id":"` + blog.ID + `"}`,
	} {
		if _, err := d.Exec(`INSERT INTO jobs (id, kind, payload, status, run_after, created_at) VALUES (?, 'service', ?, 'queued', 0, 0)`, id, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.FailStaleServiceDeployments(ctx, "musdash stopped"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{lost.ID: DeployFailed, running.ID: DeployFailed, waiting.ID: DeployQueued, done.ID: DeploySuccess} {
		got, _ := d.ServiceDeploymentByID(ctx, id)
		if got.Status != want {
			t.Errorf("deployment %s is %s, want %s", id, got.Status, want)
		}
		if want == DeployFailed && (got.Error != "musdash stopped" || got.FinishedAt == 0) {
			t.Errorf("a stale deployment's end: %+v", got)
		}
	}
}

func TestServiceDeploymentsGoWithTheirService(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	blog := blogOf(t, d, newHomeTeams(t, d))
	dep, _ := d.CreateServiceDeployment(ctx, blog.ID, TriggerManual)
	if err := d.DeleteService(ctx, blog.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ServiceDeploymentByID(ctx, dep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted service's deployment: %v", err)
	}
}
