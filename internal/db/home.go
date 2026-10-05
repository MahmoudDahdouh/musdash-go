package db

import "context"

// What Home shows of a team: what was done lately, what is wrong now, and
// where things are. Every list here is cut by a limit and every query
// filters on the team itself, so the page costs the same however long an
// install has run.

// The kinds of Event.
const (
	EventDeployment = "deployment"
	EventBackup     = "backup"
	EventTask       = "task"
)

// Event is one thing that was done to a team's resource: a deployment of
// an app, a backup of a database, a run of a scheduled task.
type Event struct {
	Kind    string
	ID      string
	Status  string // queued, running, success or failed
	Trigger string
	// At is when it was asked for. Started and Finished are 0 until then.
	At, Started, Finished int64
	// The app or database it was done to, and where that is.
	ResourceID, Resource            string
	ProjectID, Project, Environment string
	// Detail is a deployment's commit or a run's task name, and TaskID the
	// task of a run.
	Detail, TaskID string
}

// RecentEvents returns the newest things done to the team's resources,
// newest first: every deployment, and of each database and each scheduled
// task only the latest backup or run. A task that runs every minute would
// otherwise be all the list ever holds.
func (d *DB) RecentEvents(ctx context.Context, teamID string, limit int) ([]Event, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT kind, id, status, trigger, at, started, finished, resource_id, resource, project_id, project, environment, detail, task_id FROM (
			SELECT * FROM (
				SELECT ?3 AS kind, d.id AS id, d.status AS status, d.trigger AS trigger, d.created_at AS at, d.started_at AS started, d.finished_at AS finished,
				       a.id AS resource_id, a.name AS resource, p.id AS project_id, p.name AS project, e.name AS environment, d.commit_sha AS detail, '' AS task_id
				FROM deployments d JOIN apps a ON a.id = d.app_id
				JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id
				WHERE p.team_id = ?1 ORDER BY d.created_at DESC, d.rowid DESC LIMIT ?2)
			UNION ALL
			SELECT ?4, b.id, b.status, b.trigger, b.started_at, b.started_at, b.finished_at, m.id, m.name, p.id, p.name, e.name, '', ''
			FROM databases m JOIN environments e ON e.id = m.environment_id JOIN projects p ON p.id = e.project_id
			JOIN backups b ON b.id = (SELECT id FROM backups WHERE database_id = m.id ORDER BY started_at DESC, rowid DESC LIMIT 1)
			WHERE p.team_id = ?1
			UNION ALL
			SELECT ?5, r.id, r.status, r.trigger, r.started_at, r.started_at, r.finished_at, a.id, a.name, p.id, p.name, e.name, t.name, t.id
			FROM scheduled_tasks t JOIN apps a ON a.id = t.app_id
			JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id
			JOIN task_runs r ON r.id = (SELECT id FROM task_runs WHERE task_id = t.id ORDER BY started_at DESC, rowid DESC LIMIT 1)
			WHERE p.team_id = ?1
		) ORDER BY at DESC, kind, id LIMIT ?2`, teamID, limit, EventDeployment, EventBackup, EventTask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Kind, &e.ID, &e.Status, &e.Trigger, &e.At, &e.Started, &e.Finished,
			&e.ResourceID, &e.Resource, &e.ProjectID, &e.Project, &e.Environment, &e.Detail, &e.TaskID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// The ways a resource can need somebody to look at it.
const (
	TroubleDown   = "down"       // it is not running, and nobody stopped it
	TroubleDeploy = "deployment" // its newest deployment failed; the one before still serves
	TroubleBackup = "backup"     // its newest backup failed
	TroubleTask   = "task"       // the newest run of one of its scheduled tasks failed
)

// Trouble is one thing that is wrong with a resource now.
type Trouble struct {
	What string
	// The resource: KindApp, KindDatabase or KindService, and its state.
	Kind, ID, Name, Status          string
	ProjectID, Project, Environment string
	// Ref is the deployment or the task to look at, and Detail the task's
	// name.
	Ref, Detail string
	At          int64
}

// Troubles returns what is wrong with the team's resources now, newest
// first. It is about the present: a failure that a later deployment,
// backup or run made good is not listed, and neither is anything that was
// stopped on request or never deployed. A preview is listed with its app
// and nowhere else, so not here.
func (d *DB) Troubles(ctx context.Context, teamID string, limit int) ([]Trouble, error) {
	const where = ` JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?1 `
	rows, err := d.QueryContext(ctx, `
		SELECT what, kind, id, name, status, project_id, project, environment, ref, detail, at FROM (
			SELECT ?3 AS what, ?7 AS kind, a.id AS id, a.name AS name, a.status AS status, p.id AS project_id, p.name AS project, e.name AS environment,
			       '' AS ref, '' AS detail, a.updated_at AS at
			FROM apps a JOIN environments e ON e.id = a.environment_id`+where+`AND a.preview_of = '' AND a.status IN (?10, ?11)
			UNION ALL
			-- An app that is down is listed once, as down.
			SELECT ?4, ?7, a.id, a.name, a.status, p.id, p.name, e.name, d.id, '', d.created_at
			FROM apps a JOIN environments e ON e.id = a.environment_id
			JOIN deployments d ON d.id = (SELECT id FROM deployments WHERE app_id = a.id ORDER BY created_at DESC, rowid DESC LIMIT 1)`+where+`
			AND a.preview_of = '' AND a.status NOT IN (?10, ?11) AND d.status = ?13
			UNION ALL
			SELECT ?3, ?8, m.id, m.name, m.status, p.id, p.name, e.name, '', '', m.updated_at
			FROM databases m JOIN environments e ON e.id = m.environment_id`+where+`AND m.status IN (?10, ?11)
			UNION ALL
			SELECT ?3, ?9, s.id, s.name, s.status, p.id, p.name, e.name, '', '', s.updated_at
			FROM services s JOIN environments e ON e.id = s.environment_id`+where+`AND s.status IN (?10, ?11, ?12)
			UNION ALL
			SELECT ?5, ?8, m.id, m.name, m.status, p.id, p.name, e.name, b.id, '', b.started_at
			FROM databases m JOIN environments e ON e.id = m.environment_id
			JOIN backups b ON b.id = (SELECT id FROM backups WHERE database_id = m.id ORDER BY started_at DESC, rowid DESC LIMIT 1)`+where+`
			AND b.status = ?13
			UNION ALL
			SELECT ?6, ?7, a.id, a.name, a.status, p.id, p.name, e.name, t.id, t.name, r.started_at
			FROM scheduled_tasks t JOIN apps a ON a.id = t.app_id JOIN environments e ON e.id = a.environment_id
			JOIN task_runs r ON r.id = (SELECT id FROM task_runs WHERE task_id = t.id ORDER BY started_at DESC, rowid DESC LIMIT 1)`+where+`
			AND t.enabled = 1 AND r.status = ?13
		) ORDER BY at DESC, kind, id, what, ref LIMIT ?2`,
		teamID, limit, TroubleDown, TroubleDeploy, TroubleBackup, TroubleTask, KindApp, KindDatabase, KindService,
		AppFailed, AppExited, AppDegraded, RunFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Trouble
	for rows.Next() {
		var t Trouble
		if err := rows.Scan(&t.What, &t.Kind, &t.ID, &t.Name, &t.Status, &t.ProjectID, &t.Project, &t.Environment, &t.Ref, &t.Detail, &t.At); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// placed is every app, database and service with the project it is in and
// its state, previews left out: what the counts below are counts of.
const placed = `(
	SELECT e.project_id AS project_id, a.status AS status FROM apps a JOIN environments e ON e.id = a.environment_id WHERE a.preview_of = ''
	UNION ALL SELECT e.project_id, m.status FROM databases m JOIN environments e ON e.id = m.environment_id
	UNION ALL SELECT e.project_id, s.status FROM services s JOIN environments e ON e.id = s.environment_id)`

// ActiveProject is a project with what Home says about it.
type ActiveProject struct {
	Project
	// Resources is how many apps, databases and services it holds; Running
	// and Down, how many of them run and how many failed, exited or are
	// degraded. The rest were stopped, or never deployed.
	Resources, Running, Down int
	// ActiveAt is when something was last deployed in it, or when it was
	// made if nothing was.
	ActiveAt int64
}

// ActiveProjects returns the team's projects, the one something was last
// deployed in first.
func (d *DB) ActiveProjects(ctx context.Context, teamID string, limit int) ([]ActiveProject, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.id, p.team_id, p.name, p.description, p.created_at,
		       (SELECT count(*) FROM environments e WHERE e.project_id = p.id),
		       coalesce(r.total, 0), coalesce(r.running, 0), coalesce(r.down, 0),
		       max(p.created_at, coalesce((SELECT max(d.created_at) FROM deployments d JOIN apps a ON a.id = d.app_id
		           JOIN environments e ON e.id = a.environment_id WHERE e.project_id = p.id), 0)) AS active
		FROM projects p LEFT JOIN (
			SELECT project_id, count(*) AS total, sum(status = ?3) AS running, sum(status IN (?4, ?5, ?6)) AS down
			FROM `+placed+` GROUP BY project_id) r ON r.project_id = p.id
		WHERE p.team_id = ?1 ORDER BY active DESC, p.name COLLATE NOCASE, p.id LIMIT ?2`,
		teamID, limit, AppRunning, AppFailed, AppExited, AppDegraded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveProject
	for rows.Next() {
		var p ActiveProject
		if err := rows.Scan(&p.ID, &p.TeamID, &p.Name, &p.Description, &p.CreatedAt, &p.EnvCount, &p.Resources, &p.Running, &p.Down, &p.ActiveAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Totals is how much a team has.
type Totals struct {
	Projects int
	// Resources is its apps, databases and services, and Running how many
	// of them run.
	Resources, Running int
	// Sources is the Git sources that are connected.
	Sources int
}

func (d *DB) TeamTotals(ctx context.Context, teamID string) (Totals, error) {
	var t Totals
	err := d.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM projects WHERE team_id = ?1),
		       (SELECT count(*) FROM `+placed+` r JOIN projects p ON p.id = r.project_id WHERE p.team_id = ?1),
		       (SELECT count(*) FROM `+placed+` r JOIN projects p ON p.id = r.project_id WHERE p.team_id = ?1 AND r.status = ?2),
		       (SELECT count(*) FROM git_sources WHERE team_id = ?1 AND app_id <> 0)`, teamID, AppRunning).
		Scan(&t.Projects, &t.Resources, &t.Running, &t.Sources)
	return t, err
}

// LatestServerSamples returns the newest stored reading of each of the
// team's servers, by server id. A reading from before since is left out:
// it would be shown as what the server uses now.
func (d *DB) LatestServerSamples(ctx context.Context, teamID string, since int64) (map[string]Sample, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT m.server_id, m.at, m.cpu, m.mem, m.mem_total, m.load, m.disk_used, m.disk_total
		FROM servers s JOIN metric_samples m ON m.server_id = s.id AND m.resource_id = ''
		 AND m.at = (SELECT max(at) FROM metric_samples WHERE server_id = s.id AND resource_id = '')
		WHERE s.team_id = ? AND m.at >= ?`, teamID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Sample{}
	for rows.Next() {
		var id string
		var s Sample
		if err := rows.Scan(&id, &s.At, &s.CPU, &s.Mem, &s.MemTotal, &s.Load, &s.DiskUsed, &s.DiskTotal); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}
