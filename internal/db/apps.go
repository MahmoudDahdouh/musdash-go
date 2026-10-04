package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// ErrNotEmpty is returned when deleting a project or environment that still
// has resources deployed in it.
var ErrNotEmpty = errors.New("resources are still deployed here")

// Resource kinds, used by the tables shared between apps, databases and
// services (domains, env_vars, storages).
const (
	KindApp = "app"
)

// App statuses.
const (
	AppCreated   = "created"   // never deployed
	AppDeploying = "deploying" // a deployment is in progress
	AppRunning   = "running"
	AppStopped   = "stopped" // stopped on request
	AppExited    = "exited"  // the container stopped on its own
	AppFailed    = "failed"  // the first deployment failed; nothing is serving
)

type Server struct {
	ID        string
	TeamID    string
	Name      string
	Kind      string
	Host      string
	Port      int
	SSHUser   string
	IP        string
	CreatedAt int64
}

const (
	ServerLocal = "local"
	ServerSSH   = "ssh"
)

type App struct {
	ID            string
	EnvironmentID string
	ServerID      string
	Name          string
	Source        string
	Image         string
	Port          int
	MemoryMB      int
	CPUs          float64
	HealthPath    string
	HealthCmd     string
	HealthTimeout int
	Status        string
	Container     string
	HostPort      int
	DeployedImage string
	CreatedAt     int64
	UpdatedAt     int64
}

const appColumns = `a.id, a.environment_id, a.server_id, a.name, a.source, a.image, a.port, a.memory_mb, a.cpus,
	a.health_path, a.health_cmd, a.health_timeout, a.status, a.container, a.host_port, a.deployed_image, a.created_at, a.updated_at`

func scanApp(row interface{ Scan(...any) error }) (App, error) {
	var a App
	err := row.Scan(&a.ID, &a.EnvironmentID, &a.ServerID, &a.Name, &a.Source, &a.Image, &a.Port, &a.MemoryMB, &a.CPUs,
		&a.HealthPath, &a.HealthCmd, &a.HealthTimeout, &a.Status, &a.Container, &a.HostPort, &a.DeployedImage, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// EnsureLocalServer returns the install's local server, creating it for the
// team when it does not exist yet.
func (d *DB) EnsureLocalServer(ctx context.Context, teamID, ip string) (Server, error) {
	s, err := d.localServer(ctx)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return s, err
	}
	s = Server{ID: secret.RandomID(), TeamID: teamID, Name: "localhost", Kind: ServerLocal, IP: ip, CreatedAt: now()}
	_, err = d.ExecContext(ctx, `INSERT INTO servers (id, team_id, name, kind, ip, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.TeamID, s.Name, s.Kind, s.IP, s.CreatedAt)
	return s, err
}

const serverColumns = `id, team_id, name, kind, host, port, ssh_user, ip, created_at`

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.TeamID, &s.Name, &s.Kind, &s.Host, &s.Port, &s.SSHUser, &s.IP, &s.CreatedAt)
	return s, notFound(err)
}

func (d *DB) localServer(ctx context.Context) (Server, error) {
	return scanServer(d.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE kind = 'local' ORDER BY created_at, rowid LIMIT 1`))
}

// Server loads one server, scoped to the team.
func (d *DB) Server(ctx context.Context, teamID, id string) (Server, error) {
	return scanServer(d.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ? AND team_id = ?`, id, teamID))
}

// ServerByID loads a server without a team check, for background work that
// already holds a trusted id.
func (d *DB) ServerByID(ctx context.Context, id string) (Server, error) {
	return scanServer(d.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ?`, id))
}

func (d *DB) ListServers(ctx context.Context, teamID string) ([]Server, error) {
	return d.queryServers(ctx, `SELECT `+serverColumns+` FROM servers WHERE team_id = ? ORDER BY created_at, rowid`, teamID)
}

// AllServers lists every server, for the monitor and route sync.
func (d *DB) AllServers(ctx context.Context) ([]Server, error) {
	return d.queryServers(ctx, `SELECT `+serverColumns+` FROM servers ORDER BY created_at, rowid`)
}

func (d *DB) queryServers(ctx context.Context, query string, args ...any) ([]Server, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) UpdateServerIP(ctx context.Context, teamID, id, ip string) error {
	return affected(d.ExecContext(ctx, `UPDATE servers SET ip = ? WHERE id = ? AND team_id = ?`, ip, id, teamID))
}

// Setting returns an instance setting, or "" when it is not set.
func (d *DB) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := d.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Instance setting keys.
const (
	SettingInstanceDomain = "instance_domain"
	SettingACMEEmail      = "acme_email"
)

// CreateApp inserts an app into an environment the team owns.
func (d *DB) CreateApp(ctx context.Context, teamID string, a App) (App, error) {
	if _, err := d.Environment(ctx, teamID, a.EnvironmentID); err != nil {
		return App{}, err
	}
	if _, err := d.Server(ctx, teamID, a.ServerID); err != nil {
		return App{}, err
	}
	a.ID = secret.RandomID()
	a.Status = AppCreated
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	if a.Source == "" {
		a.Source = "image"
	}
	if a.HealthTimeout == 0 {
		a.HealthTimeout = 60
	}
	_, err := d.ExecContext(ctx, `INSERT INTO apps (id, environment_id, server_id, name, source, image, port, memory_mb, cpus,
			health_path, health_cmd, health_timeout, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.EnvironmentID, a.ServerID, a.Name, a.Source, a.Image, a.Port, a.MemoryMB, a.CPUs,
		a.HealthPath, a.HealthCmd, a.HealthTimeout, a.Status, a.CreatedAt, a.UpdatedAt)
	return a, err
}

// teamJoin restricts an apps query (alias a) to one team.
const teamJoin = ` JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id `

// App loads one app, scoped to the team.
func (d *DB) App(ctx context.Context, teamID, id string) (App, error) {
	a, err := scanApp(d.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps a`+teamJoin+`WHERE a.id = ? AND p.team_id = ?`, id, teamID))
	return a, notFound(err)
}

// AppByID loads an app without a team check, for background jobs.
func (d *DB) AppByID(ctx context.Context, id string) (App, error) {
	a, err := scanApp(d.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps a WHERE a.id = ?`, id))
	return a, notFound(err)
}

func (d *DB) queryApps(ctx context.Context, query string, args ...any) ([]App, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListApps returns the apps of one environment. The caller has already
// checked the environment belongs to the team.
func (d *DB) ListApps(ctx context.Context, environmentID string) ([]App, error) {
	return d.queryApps(ctx, `SELECT `+appColumns+` FROM apps a WHERE a.environment_id = ? ORDER BY a.name`, environmentID)
}

// AppsOnServer returns every app placed on a server.
func (d *DB) AppsOnServer(ctx context.Context, serverID string) ([]App, error) {
	return d.queryApps(ctx, `SELECT `+appColumns+` FROM apps a WHERE a.server_id = ? ORDER BY a.created_at, a.rowid`, serverID)
}

// UpdateAppSettings saves the fields a person edits.
func (d *DB) UpdateAppSettings(ctx context.Context, teamID string, a App) error {
	return affected(d.ExecContext(ctx, `UPDATE apps SET name = ?, image = ?, port = ?, memory_mb = ?, cpus = ?,
			health_path = ?, health_cmd = ?, health_timeout = ?, updated_at = ?
		WHERE id = ? AND environment_id IN (
			SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?)`,
		a.Name, a.Image, a.Port, a.MemoryMB, a.CPUs, a.HealthPath, a.HealthCmd, a.HealthTimeout, now(), a.ID, teamID))
}

// SetAppStatus records what the app is doing.
func (d *DB) SetAppStatus(ctx context.Context, id, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE apps SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	return err
}

// SetAppStatusIf changes the status only when the app's serving container is
// still the given one. The monitor uses it so an event from a container that
// has since been replaced cannot overwrite the new container's status.
func (d *DB) SetAppStatusIf(ctx context.Context, id, container, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE apps SET status = ?, updated_at = ? WHERE id = ? AND container = ? AND status <> ?`,
		status, now(), id, container, AppDeploying)
	return err
}

// SetAppRuntime records the container now serving the app.
func (d *DB) SetAppRuntime(ctx context.Context, id, status, container string, hostPort int, image string) error {
	_, err := d.ExecContext(ctx, `UPDATE apps SET status = ?, container = ?, host_port = ?, deployed_image = ?, updated_at = ? WHERE id = ?`,
		status, container, hostPort, image, now(), id)
	return err
}

// UsedHostPorts returns the loopback ports already assigned on a server.
func (d *DB) UsedHostPorts(ctx context.Context, serverID string) (map[int]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT host_port FROM apps WHERE server_id = ? AND host_port > 0`, serverID)
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

// DeleteApp removes an app and everything recorded against it.
func (d *DB) DeleteApp(ctx context.Context, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"domains", "env_vars", "storages"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE resource_kind = ? AND resource_id = ?`, KindApp, id); err != nil {
				return err
			}
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id))
	})
}

// IsForeignKey reports whether err is a foreign-key violation, which is how
// SQLite refuses to delete a project or environment that still holds apps.
func IsForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
