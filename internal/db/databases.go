package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

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
	// VolumePath is where the data volume is mounted in the container. It
	// is empty until the first start has chosen it.
	VolumePath string
	MemoryMB   int
	CPUs       float64
	Status     string
	Container  string
	LastError  string
	CreatedAt  int64
	UpdatedAt  int64
}

const databaseColumns = `d.id, d.environment_id, d.server_id, d.name, d.engine, d.image, d.username, d.password, d.db_name,
	d.public_port, d.volume_path, d.memory_mb, d.cpus, d.status, d.container, d.last_error, d.created_at, d.updated_at`

func scanDatabase(row interface{ Scan(...any) error }) (Database, error) {
	var m Database
	err := row.Scan(&m.ID, &m.EnvironmentID, &m.ServerID, &m.Name, &m.Engine, &m.Image, &m.Username, &m.Password, &m.DBName,
		&m.PublicPort, &m.VolumePath, &m.MemoryMB, &m.CPUs, &m.Status, &m.Container, &m.LastError, &m.CreatedAt, &m.UpdatedAt)
	return m, notFound(err)
}

// nameTaken reports whether an app, database or service in the environment
// already uses the name. except is the id of the resource being renamed, if any.
func nameTaken(ctx context.Context, tx *sql.Tx, environmentID, name, except string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM apps WHERE environment_id = ? AND name = ? AND id <> ?) +
		(SELECT count(*) FROM databases WHERE environment_id = ? AND name = ? AND id <> ?) +
		(SELECT count(*) FROM services WHERE environment_id = ? AND name = ? AND id <> ?)`,
		environmentID, name, except, environmentID, name, except, environmentID, name, except).Scan(&n)
	if err != nil || n > 0 {
		return n > 0, err
	}
	// A stack that joined the environment's network answers there to the
	// names of its Compose services too.
	rows, err := tx.QueryContext(ctx, `SELECT members FROM services WHERE environment_id = ? AND connect_env = 1 AND id <> ?`, environmentID, except)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var members string
		if err := rows.Scan(&members); err != nil {
			return false, err
		}
		for _, m := range strings.Split(members, ",") {
			if m == name {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// Public ports musdash hands out to databases that are made public without
// a port being named.
const (
	PublicPortMin = 30000
	PublicPortMax = 30999
)

// PickPort as a database's PublicPort asks CreateDatabase and
// UpdateDatabaseSettings to choose a free one.
const PickPort = -1

// ErrNoFreePort is returned when every port musdash picks from is taken.
var ErrNoFreePort = errors.New("no public port is free")

// freePublicPort returns the lowest unused port of the range on a server.
func freePublicPort(ctx context.Context, tx *sql.Tx, serverID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT public_port FROM databases WHERE server_id = ? AND public_port BETWEEN ? AND ? ORDER BY public_port`,
		serverID, PublicPortMin, PublicPortMax)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	next := PublicPortMin
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return 0, err
		}
		if p == next {
			next++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if next > PublicPortMax {
		return 0, ErrNoFreePort
	}
	return next, nil
}

// checkPublicPort settles a database's public port inside a transaction:
// it picks one when asked to, and refuses one another database has.
func checkPublicPort(ctx context.Context, tx *sql.Tx, m *Database) error {
	switch {
	case m.PublicPort == PickPort:
		port, err := freePublicPort(ctx, tx, m.ServerID)
		m.PublicPort = port
		return err
	case m.PublicPort > 0:
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM databases WHERE server_id = ? AND public_port = ? AND id <> ?`,
			m.ServerID, m.PublicPort, m.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrPortTaken
		}
	}
	return nil
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
		if err := checkPublicPort(ctx, tx, &m); err != nil {
			return err
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

// ErrPortTaken is returned when another database on the server already has
// the requested public port.
var ErrPortTaken = errors.New("the public port is already used by another database")

// UpdateDatabaseSettings saves the image, the limits and the public port,
// and returns the port that was stored.
func (d *DB) UpdateDatabaseSettings(ctx context.Context, teamID string, m Database) (publicPort int, err error) {
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		if err := checkPublicPort(ctx, tx, &m); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `UPDATE databases SET image = ?, public_port = ?, memory_mb = ?, cpus = ?, updated_at = ?
			WHERE id = ? AND environment_id IN (
				SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?)`,
			m.Image, m.PublicPort, m.MemoryMB, m.CPUs, now(), m.ID, teamID))
	})
	return m.PublicPort, err
}

// SetDatabaseVolumePath records where a database's volume is mounted. It
// is set once: a later call with another path changes nothing.
func (d *DB) SetDatabaseVolumePath(ctx context.Context, id, path string) error {
	_, err := d.ExecContext(ctx, `UPDATE databases SET volume_path = ? WHERE id = ? AND volume_path = ''`, path, id)
	return err
}

// BeginDatabaseStart marks a database as starting and reports whether it
// was not already: two requests to start it then queue one start.
func (d *DB) BeginDatabaseStart(ctx context.Context, id string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE databases SET status = ?, last_error = '', updated_at = ? WHERE id = ? AND status <> ?`,
		AppDeploying, now(), id, AppDeploying)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetDatabaseState records what a database is doing and why it last failed.
func (d *DB) SetDatabaseState(ctx context.Context, id, status, container, lastError string) error {
	return affected(d.ExecContext(ctx, `UPDATE databases SET status = ?, container = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, container, lastError, now(), id))
}

// SetDatabaseStatusIf is the database counterpart of SetAppStatusIf.
func (d *DB) SetDatabaseStatusIf(ctx context.Context, id, container, status string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE databases SET status = ?, updated_at = ? WHERE id = ? AND container = ? AND status NOT IN (?, ?, ?)`,
		status, now(), id, container, AppDeploying, AppStopped, status)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ResetStuckDatabases repairs databases left "deploying" by a process that
// died: with no job queued or running for them, nothing would ever move
// them on. One whose container was already created is marked as not
// running, and the monitor's reconcile corrects that from Docker right
// after; one without a container has failed to start.
func (d *DB) ResetStuckDatabases(ctx context.Context, reason string) error {
	_, err := d.ExecContext(ctx, `UPDATE databases
		SET status = CASE WHEN container <> '' THEN ? ELSE ? END,
		    last_error = CASE WHEN container <> '' THEN '' ELSE ? END, updated_at = ?
		WHERE status = ? AND id NOT IN (
			SELECT json_extract(payload, '$.id') FROM jobs WHERE kind = 'database' AND status IN ('queued', 'running'))`,
		AppExited, AppFailed, reason, now(), AppDeploying)
	return err
}

func (d *DB) DeleteDatabase(ctx context.Context, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM databases WHERE id = ?`, id))
}
