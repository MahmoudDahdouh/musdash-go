package db

import (
	"context"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// The parts of a service's deployment, in the order they happen. A
// deployment's Step is the one under way, or the one it failed in.
const (
	ServiceStepClone  = "clone"  // fetching the repository; a stack from Git only
	ServiceStepRead   = "read"   // reading and checking the Compose file
	ServiceStepImages = "images" // pulling, and for a stack from Git building
	ServiceStepStart  = "start"  // starting the containers and waiting for them
)

// ServiceDeployment is one deployment of a service. Its statuses are a
// Deployment's.
type ServiceDeployment struct {
	ID        string
	ServiceID string
	Status    string
	// Trigger is what asked for it, "" when that is not known.
	Trigger string
	// Step is "" until the deployment has begun its first part.
	Step string
	// CommitSHA is what a stack from Git was deployed from, once fetched.
	CommitSHA  string
	Error      string
	CreatedAt  int64
	StartedAt  int64
	FinishedAt int64
}

// Over reports whether the deployment has ended.
func (m ServiceDeployment) Over() bool {
	return m.Status == DeploySuccess || m.Status == DeployFailed
}

const serviceDeploymentColumns = `id, service_id, status, trigger, step, commit_sha, error, created_at, started_at, finished_at`

func scanServiceDeployment(row interface{ Scan(...any) error }) (ServiceDeployment, error) {
	var m ServiceDeployment
	err := row.Scan(&m.ID, &m.ServiceID, &m.Status, &m.Trigger, &m.Step, &m.CommitSHA, &m.Error, &m.CreatedAt, &m.StartedAt, &m.FinishedAt)
	return m, err
}

// CreateServiceDeployment records a deployment that waits to run.
func (d *DB) CreateServiceDeployment(ctx context.Context, serviceID, trigger string) (ServiceDeployment, error) {
	m := ServiceDeployment{ID: secret.RandomID(), ServiceID: serviceID, Status: DeployQueued, Trigger: trigger, CreatedAt: now()}
	if _, err := d.ExecContext(ctx, `INSERT INTO service_deployments (id, service_id, status, trigger, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.ServiceID, m.Status, m.Trigger, m.CreatedAt); err != nil {
		return ServiceDeployment{}, err
	}
	return m, nil
}

// ServiceDeployment loads one deployment of the given service.
func (d *DB) ServiceDeployment(ctx context.Context, serviceID, id string) (ServiceDeployment, error) {
	m, err := scanServiceDeployment(d.QueryRowContext(ctx, `SELECT `+serviceDeploymentColumns+` FROM service_deployments WHERE id = ? AND service_id = ?`, id, serviceID))
	return m, notFound(err)
}

// ServiceDeploymentByID loads a deployment for a background job.
func (d *DB) ServiceDeploymentByID(ctx context.Context, id string) (ServiceDeployment, error) {
	m, err := scanServiceDeployment(d.QueryRowContext(ctx, `SELECT `+serviceDeploymentColumns+` FROM service_deployments WHERE id = ?`, id))
	return m, notFound(err)
}

// newestServiceDeployments orders a service's deployments newest first.
// Two made in one second are told apart by which row came later.
const newestServiceDeployments = ` FROM service_deployments WHERE service_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`

// CurrentServiceDeployment returns the deployment that says what the
// service is doing: the one that is running or, when none is, the newest.
// With one running and another waiting behind it, the newest would be the
// one that has not begun. ErrNotFound for a service that has none on
// record.
func (d *DB) CurrentServiceDeployment(ctx context.Context, serviceID string) (ServiceDeployment, error) {
	m, err := scanServiceDeployment(d.QueryRowContext(ctx, `SELECT `+serviceDeploymentColumns+` FROM service_deployments WHERE service_id = ?
		ORDER BY status = 'running' DESC, created_at DESC, rowid DESC LIMIT 1`, serviceID))
	return m, notFound(err)
}

// ListServiceDeployments returns a service's most recent deployments,
// newest first.
func (d *DB) ListServiceDeployments(ctx context.Context, serviceID string, limit int) ([]ServiceDeployment, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+serviceDeploymentColumns+newestServiceDeployments, serviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceDeployment
	for rows.Next() {
		m, err := scanServiceDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StartServiceDeployment marks a deployment as under way. One that is
// started a second time, by a job that was interrupted and runs again,
// begins from nothing: its step and its end are forgotten.
func (d *DB) StartServiceDeployment(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET status = ?, step = '', error = '', started_at = ?, finished_at = 0 WHERE id = ?`, DeployRunning, now(), id)
	return err
}

// RequeueServiceDeployment makes a running deployment wait again, for one
// whose job was interrupted and will run a second time. It does not go on
// naming the part it was at while it waits its turn once more.
func (d *DB) RequeueServiceDeployment(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET status = ?, step = '', started_at = 0 WHERE id = ? AND status = ?`, DeployQueued, id, DeployRunning)
	return err
}

// SetServiceDeploymentStep records the part a deployment has begun.
func (d *DB) SetServiceDeploymentStep(ctx context.Context, id, step string) error {
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET step = ? WHERE id = ?`, step, id)
	return err
}

// SetServiceDeploymentCommit records the commit a deployment fetched.
func (d *DB) SetServiceDeploymentCommit(ctx context.Context, id, commit string) error {
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET commit_sha = ? WHERE id = ?`, commit, id)
	return err
}

// FinishServiceDeployment records how a deployment ended. Its step is
// left: of one that failed it says where.
func (d *DB) FinishServiceDeployment(ctx context.Context, id, status, errText string) error {
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET status = ?, error = ?, finished_at = ? WHERE id = ?`, status, errText, now(), id)
	return err
}

// FailStaleServiceDeployments marks deployments that were queued or
// running when the process stopped and whose job is gone. Called at
// start-up, after the queue has taken back the jobs that were running.
func (d *DB) FailStaleServiceDeployments(ctx context.Context, reason string) error {
	// A job queued before deployments were recorded names none. Left in the
	// list as NULL, it would make NOT IN true of nothing.
	_, err := d.ExecContext(ctx, `UPDATE service_deployments SET status = ?, error = ?, finished_at = ?
		WHERE status IN ('queued', 'running') AND id NOT IN (
			SELECT json_extract(payload, '$.deployment_id') FROM jobs
			WHERE kind = 'service' AND status IN ('queued', 'running') AND json_extract(payload, '$.deployment_id') IS NOT NULL)`,
		DeployFailed, reason, now())
	return err
}

// ServiceDeploymentIDs returns every deployment id of a service.
func (d *DB) ServiceDeploymentIDs(ctx context.Context, serviceID string) ([]string, error) {
	return d.queryIDs(ctx, `SELECT id FROM service_deployments WHERE service_id = ?`, serviceID)
}

// PruneServiceDeployments deletes a service's finished deployments beyond
// the newest keep and returns the ids removed, so their log files can go
// too.
func (d *DB) PruneServiceDeployments(ctx context.Context, serviceID string, keep int) ([]string, error) {
	ids, err := d.queryIDs(ctx, `SELECT id FROM service_deployments WHERE service_id = ? AND status IN ('success', 'failed')
		ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?`, serviceID, keep)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	for _, id := range ids {
		if _, err := d.ExecContext(ctx, `DELETE FROM service_deployments WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
