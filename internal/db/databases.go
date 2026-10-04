package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// KindDatabase is the resource kind of a database.
const KindDatabase = "database"

// ErrNameTaken is returned when an environment already has a resource with
// the requested name. Apps and databases share one namespace there: the
// name is their address on the environment's network.
var ErrNameTaken = errors.New("the name is already used in this environment")

// Database is a managed database server. Its statuses are the App ones.
type Database struct {
	ID            string
	EnvironmentID string
	ServerID      string
	Name          string
	Engine        string
	Image         string
	Username      string
	Password      string // sealed
	DBName        string
	PublicPort    int
	MemoryMB      int
	CPUs          float64
	Status        string
	Container     string
	LastError     string
	CreatedAt     int64
	UpdatedAt     int64
}

const databaseColumns = `d.id, d.environment_id, d.server_id, d.name, d.engine, d.image, d.username, d.password, d.db_name,
	d.public_port, d.memory_mb, d.cpus, d.status, d.container, d.last_error, d.created_at, d.updated_at`

func scanDatabase(row interface{ Scan(...any) error }) (Database, error) {
	var m Database
	err := row.Scan(&m.ID, &m.EnvironmentID, &m.ServerID, &m.Name, &m.Engine, &m.Image, &m.Username, &m.Password, &m.DBName,
		&m.PublicPort, &m.MemoryMB, &m.CPUs, &m.Status, &m.Container, &m.LastError, &m.CreatedAt, &m.UpdatedAt)
	return m, notFound(err)
}

// nameTaken reports whether an app or database in the environment already
// uses the name. except is the id of the resource being renamed, if any.
func nameTaken(ctx context.Context, tx *sql.Tx, environmentID, name, except string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM apps WHERE environment_id = ? AND name = ? AND id <> ?) +
		(SELECT count(*) FROM databases WHERE environment_id = ? AND name = ? AND id <> ?)`,
		environmentID, name, except, environmentID, name, except).Scan(&n)
	return n > 0, err
}

// CreateDatabase inserts a database into an environment the team owns.
func (d *DB) CreateDatabase(ctx context.Context, teamID string, m Database) (Database, error) {
	if _, err := d.Environment(ctx, teamID, m.EnvironmentID); err != nil {
		return Database{}, err
	}
	if _, err := d.Server(ctx, teamID, m.ServerID); err != nil {
		return Database{}, err
	}
	m.ID = secret.RandomID()
	m.Status = AppCreated
	m.CreatedAt = now()
	m.UpdatedAt = m.CreatedAt
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		taken, err := nameTaken(ctx, tx, m.EnvironmentID, m.Name, "")
		if err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO databases (id, environment_id, server_id, name, engine, image, username, password, db_name,
				public_port, memory_mb, cpus, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.EnvironmentID, m.ServerID, m.Name, m.Engine, m.Image, m.Username, m.Password, m.DBName,
			m.PublicPort, m.MemoryMB, m.CPUs, m.Status, m.CreatedAt, m.UpdatedAt)
		return err
	})
	return m, err
}

const databaseTeamJoin = ` JOIN environments e ON e.id = d.environment_id JOIN projects p ON p.id = e.project_id `

// Database loads one database, scoped to the team.
func (d *DB) Database(ctx context.Context, teamID, id string) (Database, error) {
	return scanDatabase(d.QueryRowContext(ctx, `SELECT `+databaseColumns+` FROM databases d`+databaseTeamJoin+`WHERE d.id = ? AND p.team_id = ?`, id, teamID))
}

// DatabaseByID loads a database without a team check, for background work.
func (d *DB) DatabaseByID(ctx context.Context, id string) (Database, error) {
	return scanDatabase(d.QueryRowContext(ctx, `SELECT `+databaseColumns+` FROM databases d WHERE d.id = ?`, id))
}

func (d *DB) queryDatabases(ctx context.Context, query string, args ...any) ([]Database, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		m, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListDatabases returns the databases of one environment. The caller has
// already checked the environment belongs to the team.
func (d *DB) ListDatabases(ctx context.Context, environmentID string) ([]Database, error) {
	return d.queryDatabases(ctx, `SELECT `+databaseColumns+` FROM databases d WHERE d.environment_id = ? ORDER BY d.name`, environmentID)
}

// DatabasesOnServer returns every database placed on a server.
func (d *DB) DatabasesOnServer(ctx context.Context, serverID string) ([]Database, error) {
	return d.queryDatabases(ctx, `SELECT `+databaseColumns+` FROM databases d WHERE d.server_id = ? ORDER BY d.created_at, d.rowid`, serverID)
}

// UpdateDatabaseSettings saves the limits and the public port.
func (d *DB) UpdateDatabaseSettings(ctx context.Context, teamID string, m Database) error {
	return affected(d.ExecContext(ctx, `UPDATE databases SET public_port = ?, memory_mb = ?, cpus = ?, updated_at = ?
		WHERE id = ? AND environment_id IN (
			SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?)`,
		m.PublicPort, m.MemoryMB, m.CPUs, now(), m.ID, teamID))
}

// SetDatabaseState records what a database is doing and why it last failed.
func (d *DB) SetDatabaseState(ctx context.Context, id, status, container, lastError string) error {
	return affected(d.ExecContext(ctx, `UPDATE databases SET status = ?, container = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, container, lastError, now(), id))
}

// SetDatabaseStatusIf is the database counterpart of SetAppStatusIf.
func (d *DB) SetDatabaseStatusIf(ctx context.Context, id, container, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE databases SET status = ?, updated_at = ? WHERE id = ? AND container = ? AND status NOT IN (?, ?)`,
		status, now(), id, container, AppDeploying, AppStopped)
	return err
}

// PublicPortsInUse returns the public ports already given to databases on a
// server.
func (d *DB) PublicPortsInUse(ctx context.Context, serverID string) (map[int]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT public_port FROM databases WHERE server_id = ? AND public_port > 0`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := make(map[int]bool)
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

func (d *DB) DeleteDatabase(ctx context.Context, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM databases WHERE id = ?`, id))
}
