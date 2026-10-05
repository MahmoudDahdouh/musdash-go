package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// ErrSetupClosed is returned when first-run setup is attempted after an
// account already exists.
var ErrSetupClosed = errors.New("setup is already complete")

// ErrLastEnvironment is returned when deleting a project's only environment.
var ErrLastEnvironment = errors.New("a project needs at least one environment")

type Project struct {
	ID          string
	TeamID      string
	Name        string
	Description string
	CreatedAt   int64
	// EnvCount and the three counts of what is in the project (previews
	// left out, as everywhere they are listed) are filled by ListProjects only.
	EnvCount  int
	Apps      int
	Databases int
	Services  int
}

type Environment struct {
	ID        string
	ProjectID string
	Name      string
	CreatedAt int64
}

// DefaultEnvironment is created with every project.
const DefaultEnvironment = "production"

// CreateProject creates a project together with its first environment.
func (d *DB) CreateProject(ctx context.Context, teamID, name, description string) (Project, error) {
	p := Project{ID: secret.RandomID(), TeamID: teamID, Name: name, Description: description, CreatedAt: now()}
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, team_id, name, description, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.ID, p.TeamID, p.Name, p.Description, p.CreatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO environments (id, project_id, name, created_at) VALUES (?, ?, ?, ?)`,
			secret.RandomID(), p.ID, DefaultEnvironment, p.CreatedAt)
		return err
	})
	return p, err
}

func (d *DB) ListProjects(ctx context.Context, teamID string) ([]Project, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.id, p.team_id, p.name, p.description, p.created_at,
		       (SELECT count(*) FROM environments e WHERE e.project_id = p.id),
		       (SELECT count(*) FROM apps a JOIN environments e ON e.id = a.environment_id WHERE e.project_id = p.id AND a.preview_of = ''),
		       (SELECT count(*) FROM databases m JOIN environments e ON e.id = m.environment_id WHERE e.project_id = p.id),
		       (SELECT count(*) FROM services v JOIN environments e ON e.id = v.environment_id WHERE e.project_id = p.id)
		FROM projects p WHERE p.team_id = ? ORDER BY p.name COLLATE NOCASE, p.id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.TeamID, &p.Name, &p.Description, &p.CreatedAt, &p.EnvCount, &p.Apps, &p.Databases, &p.Services); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Project loads one project, scoped to the team.
func (d *DB) Project(ctx context.Context, teamID, id string) (Project, error) {
	var p Project
	err := d.QueryRowContext(ctx, `SELECT id, team_id, name, description, created_at FROM projects WHERE id = ? AND team_id = ?`, id, teamID).
		Scan(&p.ID, &p.TeamID, &p.Name, &p.Description, &p.CreatedAt)
	return p, notFound(err)
}

func (d *DB) UpdateProject(ctx context.Context, teamID, id, name, description string) error {
	return affected(d.ExecContext(ctx, `UPDATE projects SET name = ?, description = ? WHERE id = ? AND team_id = ?`, name, description, id, teamID))
}

func (d *DB) DeleteProject(ctx context.Context, teamID, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND team_id = ?`, id, teamID))
}

func (d *DB) ListEnvironments(ctx context.Context, projectID string) ([]Environment, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, name, created_at FROM environments WHERE project_id = ? ORDER BY created_at, rowid`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Name, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Environment loads one environment, scoped to the team through its project.
func (d *DB) Environment(ctx context.Context, teamID, id string) (Environment, error) {
	var e Environment
	err := d.QueryRowContext(ctx, `
		SELECT e.id, e.project_id, e.name, e.created_at
		FROM environments e JOIN projects p ON p.id = e.project_id
		WHERE e.id = ? AND p.team_id = ?`, id, teamID).
		Scan(&e.ID, &e.ProjectID, &e.Name, &e.CreatedAt)
	return e, notFound(err)
}

// CreateEnvironment adds an environment to a project the team owns.
func (d *DB) CreateEnvironment(ctx context.Context, teamID, projectID, name string) (Environment, error) {
	if _, err := d.Project(ctx, teamID, projectID); err != nil {
		return Environment{}, err
	}
	e := Environment{ID: secret.RandomID(), ProjectID: projectID, Name: name, CreatedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO environments (id, project_id, name, created_at) VALUES (?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.Name, e.CreatedAt)
	return e, err
}

// DeleteEnvironment removes an environment unless it is the project's last.
func (d *DB) DeleteEnvironment(ctx context.Context, teamID, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var projectID string
		err := tx.QueryRowContext(ctx, `
			SELECT e.project_id FROM environments e JOIN projects p ON p.id = e.project_id
			WHERE e.id = ? AND p.team_id = ?`, id, teamID).Scan(&projectID)
		if err != nil {
			return notFound(err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environments WHERE project_id = ?`, projectID).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastEnvironment
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM environments WHERE id = ?`, id)
		return err
	})
}

// Place says where an environment is: its project and its own name.
type Place struct {
	ProjectID string
	Project   string
	Env       string
}

// Places returns where each of the team's environments is, by environment
// id: what a list of resources from several projects needs to say where
// each one lives. A team has few environments; this is one small query.
func (d *DB) Places(ctx context.Context, teamID string) (map[string]Place, error) {
	rows, err := d.QueryContext(ctx, `SELECT e.id, p.id, p.name, e.name FROM environments e
		JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Place{}
	for rows.Next() {
		var id string
		var pl Place
		if err := rows.Scan(&id, &pl.ProjectID, &pl.Project, &pl.Env); err != nil {
			return nil, err
		}
		out[id] = pl
	}
	return out, rows.Err()
}
