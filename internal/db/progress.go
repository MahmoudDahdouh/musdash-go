package db

import "context"

// InProgress is one of a team's resources that is being deployed now: an
// app or a service with a deployment queued or running, or a database
// that is being started.
type InProgress struct {
	Kind string // KindApp, KindService or KindDatabase
	ID   string
	Name string
	// Where it is, for its address.
	ProjectID, EnvironmentID string
	// DeploymentID is the deployment in progress; a database has none.
	DeploymentID string
	// Status is DeployQueued or DeployRunning.
	Status string
	// At is when it was asked for.
	At int64
}

// newestInProgress picks, for the row of the outer query named by owner,
// the deployment to show of its table: the oldest that is queued or
// running among the five newest. Deployments of one resource run in the
// order they were asked for, so anything in progress is among the newest,
// and the oldest of those is the one that is running or next to. The five
// are read through the index on (owner, created_at): nothing here walks a
// table of deployments, which only grows. A status is a word of ours, so
// it is written out, which is what lets the planner use that index alone.
func newestInProgress(table, column, owner string) string {
	return `(SELECT id FROM (SELECT id, status, created_at, rowid AS r FROM ` + table + ` WHERE ` + column + ` = ` + owner + `
		ORDER BY created_at DESC, rowid DESC LIMIT 5) WHERE status IN ('` + DeployQueued + `', '` + DeployRunning + `') ORDER BY created_at, r LIMIT 1)`
}

// Deploying returns what of the team's is being deployed now, what is
// running before what waits, and of each the one asked for first. It is
// one statement with the team's id in every arm, and is asked by every
// page and again every few seconds (the note in the corner), so each arm
// starts from the team's own rows, which are few.
func (d *DB) Deploying(ctx context.Context, teamID string, limit int) ([]InProgress, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT kind, id, name, project_id, environment_id, deployment_id, status, at FROM (
			SELECT ?3 AS kind, a.id AS id, a.name AS name, e.project_id AS project_id, e.id AS environment_id, d.id AS deployment_id, d.status AS status, d.created_at AS at
			FROM projects p JOIN environments e ON e.project_id = p.id JOIN apps a ON a.environment_id = e.id
			JOIN deployments d ON d.id = `+newestInProgress("deployments", "app_id", "a.id")+`
			WHERE p.team_id = ?1
			UNION ALL
			SELECT ?4, s.id, s.name, e.project_id, e.id, d.id, d.status, d.created_at
			FROM projects p JOIN environments e ON e.project_id = p.id JOIN services s ON s.environment_id = e.id
			JOIN service_deployments d ON d.id = `+newestInProgress("service_deployments", "service_id", "s.id")+`
			WHERE p.team_id = ?1
			UNION ALL
			SELECT ?5, m.id, m.name, e.project_id, e.id, '', ?6, m.updated_at
			FROM projects p JOIN environments e ON e.project_id = p.id JOIN databases m ON m.environment_id = e.id
			WHERE p.team_id = ?1 AND m.status = ?7
		) ORDER BY status = ?6 DESC, at, kind, id LIMIT ?2`,
		teamID, limit, KindApp, KindService, KindDatabase, DeployRunning, AppDeploying)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InProgress
	for rows.Next() {
		var p InProgress
		if err := rows.Scan(&p.Kind, &p.ID, &p.Name, &p.ProjectID, &p.EnvironmentID, &p.DeploymentID, &p.Status, &p.At); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
