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

	// For a server reached over SSH.
	SSHKeyID      string // the key musdash signs in with
	HostKey       string // the server's own key once seen, as an authorized_keys line
	DataDir       string // where musdash keeps its files there; "" for the local server
	Arch          string // "amd64", "arm64", … as last checked
	DockerVersion string
	Status        string // ServerUnknown, ServerOK, ServerUnreachable
	StatusDetail  string
	CheckedAt     int64
	Proxy         string // ProxyNone, ProxyInstalled
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

	// Git source. Empty for apps deployed from a prebuilt image.
	RepoURL        string
	RepoName       string // "owner/name", lower case
	Branch         string
	BuildPack      string
	DockerfilePath string
	BaseDir        string
	PublishDir     string
	SPAFallback    bool
	GitSourceID    string // the GitHub App to clone through, if any
	SSHKeyID       string // the deploy key to clone with, if any
	AutoDeploy     bool
	WebhookSecret  string // sealed
	// DeployTokenHash is the SHA-256 of the token an external CI system
	// uses to trigger a deployment.
	DeployTokenHash string

	// Runtime overrides.
	StartCommand  string
	DockerOptions string

	// BuildServerID is the server the image is built on when that is not
	// the one the app runs on; "" builds where it runs.
	BuildServerID string
}

// Sources of an app's image.
const (
	SourceImage = "image"
	SourceGit   = "git"
)

const appColumns = `a.id, a.environment_id, a.server_id, a.name, a.source, a.image, a.port, a.memory_mb, a.cpus,
	a.health_path, a.health_cmd, a.health_timeout, a.status, a.container, a.host_port, a.deployed_image, a.created_at, a.updated_at,
	a.repo_url, a.repo_name, a.branch, a.build_pack, a.dockerfile_path, a.base_dir, a.publish_dir, a.spa_fallback,
	a.git_source_id, a.ssh_key_id, a.auto_deploy, a.webhook_secret, a.deploy_token_hash, a.start_command, a.docker_options,
	a.build_server_id`

func scanApp(row interface{ Scan(...any) error }) (App, error) {
	var a App
	err := row.Scan(&a.ID, &a.EnvironmentID, &a.ServerID, &a.Name, &a.Source, &a.Image, &a.Port, &a.MemoryMB, &a.CPUs,
		&a.HealthPath, &a.HealthCmd, &a.HealthTimeout, &a.Status, &a.Container, &a.HostPort, &a.DeployedImage, &a.CreatedAt, &a.UpdatedAt,
		&a.RepoURL, &a.RepoName, &a.Branch, &a.BuildPack, &a.DockerfilePath, &a.BaseDir, &a.PublishDir, &a.SPAFallback,
		&a.GitSourceID, &a.SSHKeyID, &a.AutoDeploy, &a.WebhookSecret, &a.DeployTokenHash, &a.StartCommand, &a.DockerOptions,
		&a.BuildServerID)
	return a, err
}

// EnsureLocalServer returns the install's local server, creating it for the
// team when it does not exist yet. A unique index allows only one, so two
// callers racing to create it end up with the same row.
func (d *DB) EnsureLocalServer(ctx context.Context, teamID, ip string) (Server, error) {
	s, err := d.localServer(ctx)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return s, err
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO servers (id, team_id, name, kind, ip, created_at) VALUES (?, ?, 'localhost', 'local', ?, ?)
		ON CONFLICT DO NOTHING`, secret.RandomID(), teamID, ip, now()); err != nil {
		return Server{}, err
	}
	return d.localServer(ctx)
}

const serverColumns = `id, team_id, name, kind, host, port, ssh_user, ip, created_at,
	ssh_key_id, host_key, data_dir, arch, docker_version, status, status_detail, checked_at, proxy`

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.TeamID, &s.Name, &s.Kind, &s.Host, &s.Port, &s.SSHUser, &s.IP, &s.CreatedAt,
		&s.SSHKeyID, &s.HostKey, &s.DataDir, &s.Arch, &s.DockerVersion, &s.Status, &s.StatusDetail, &s.CheckedAt, &s.Proxy)
	return s, notFound(err)
}

// Server statuses, and whether the proxy was installed.
const (
	ServerUnknown     = "unknown"
	ServerOK          = "ok"
	ServerProblem     = "problem" // reached, but something a deployment needs is missing
	ServerUnreachable = "unreachable"

	ProxyNone      = "none"
	ProxyInstalled = "installed"
)

// CreateServer adds a server reached over SSH. Its key must be the team's.
func (d *DB) CreateServer(ctx context.Context, s Server) (Server, error) {
	if _, err := d.SSHKey(ctx, s.TeamID, s.SSHKeyID); err != nil {
		return Server{}, err
	}
	s.ID = secret.RandomID()
	s.Kind = ServerSSH
	s.Status = ServerUnknown
	s.Proxy = ProxyNone
	s.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO servers (id, team_id, name, kind, host, port, ssh_user, ip, created_at, ssh_key_id, data_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TeamID, s.Name, s.Kind, s.Host, s.Port, s.SSHUser, s.IP, s.CreatedAt, s.SSHKeyID, s.DataDir)
	return s, err
}

// SetServerHostKey records the key a server presented, if none is recorded
// yet. It reports whether this call recorded it: when two first connections
// race, only one key becomes the server's.
func (d *DB) SetServerHostKey(ctx context.Context, id, hostKey string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE servers SET host_key = ? WHERE id = ? AND host_key = ''`, hostKey, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ForgetServerHostKey drops the recorded key, so that the next check
// records whatever the server then presents.
func (d *DB) ForgetServerHostKey(ctx context.Context, teamID, id string) error {
	return affected(d.ExecContext(ctx, `UPDATE servers SET host_key = '', status = ?, status_detail = '' WHERE id = ? AND team_id = ? AND kind = ?`,
		ServerUnknown, id, teamID, ServerSSH))
}

// SetServerChecked records what a check of the server found.
func (d *DB) SetServerChecked(ctx context.Context, s Server) error {
	return affected(d.ExecContext(ctx, `UPDATE servers SET status = ?, status_detail = ?, checked_at = ?, arch = ?, docker_version = ?, ip = ?, data_dir = ? WHERE id = ?`,
		s.Status, s.StatusDetail, now(), s.Arch, s.DockerVersion, s.IP, s.DataDir, s.ID))
}

// SetServerProxy records whether the proxy is installed on a server.
func (d *DB) SetServerProxy(ctx context.Context, id, state string) error {
	return affected(d.ExecContext(ctx, `UPDATE servers SET proxy = ? WHERE id = ?`, state, id))
}

// ServerUse counts what runs on a server.
func (d *DB) ServerUse(ctx context.Context, id string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM apps WHERE server_id = ?1 OR build_server_id = ?1)
		+ (SELECT count(*) FROM databases WHERE server_id = ?1) + (SELECT count(*) FROM services WHERE server_id = ?1)`, id).Scan(&n)
	return n, err
}

// DeleteServer removes a server that nothing runs on. The local server is
// never removed.
func (d *DB) DeleteServer(ctx context.Context, teamID, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM apps WHERE server_id = ?1 OR build_server_id = ?1)
			+ (SELECT count(*) FROM databases WHERE server_id = ?1) + (SELECT count(*) FROM services WHERE server_id = ?1)`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM servers WHERE id = ? AND team_id = ? AND kind = ?`, id, teamID, ServerSSH))
	})
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

// CreateApp inserts an app into an environment the team owns. A Git source
// or deploy key it names must belong to the same team.
func (d *DB) CreateApp(ctx context.Context, teamID string, a App) (App, error) {
	if _, err := d.Environment(ctx, teamID, a.EnvironmentID); err != nil {
		return App{}, err
	}
	if _, err := d.Server(ctx, teamID, a.ServerID); err != nil {
		return App{}, err
	}
	if err := d.checkSourceOwnership(ctx, teamID, a); err != nil {
		return App{}, err
	}
	a.ID = secret.RandomID()
	a.Status = AppCreated
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	if a.Source == "" {
		a.Source = SourceImage
	}
	if a.HealthTimeout == 0 {
		a.HealthTimeout = 60
	}
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		// Apps and databases share the environment's network names.
		taken, err := nameTaken(ctx, tx, a.EnvironmentID, a.Name, "")
		if err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO apps (id, environment_id, server_id, name, source, image, port, memory_mb, cpus,
				health_path, health_cmd, health_timeout, status, created_at, updated_at,
				repo_url, repo_name, branch, build_pack, dockerfile_path, base_dir, publish_dir, spa_fallback,
				git_source_id, ssh_key_id, auto_deploy, webhook_secret)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.EnvironmentID, a.ServerID, a.Name, a.Source, a.Image, a.Port, a.MemoryMB, a.CPUs,
			a.HealthPath, a.HealthCmd, a.HealthTimeout, a.Status, a.CreatedAt, a.UpdatedAt,
			a.RepoURL, a.RepoName, a.Branch, a.BuildPack, a.DockerfilePath, a.BaseDir, a.PublishDir, a.SPAFallback,
			a.GitSourceID, a.SSHKeyID, a.AutoDeploy, a.WebhookSecret)
		return err
	})
	return a, err
}

// checkSourceOwnership refuses a Git source or deploy key of another team.
func (d *DB) checkSourceOwnership(ctx context.Context, teamID string, a App) error {
	if a.GitSourceID != "" {
		if _, err := d.GitSource(ctx, teamID, a.GitSourceID); err != nil {
			return err
		}
	}
	if a.SSHKeyID != "" {
		if _, err := d.SSHKey(ctx, teamID, a.SSHKeyID); err != nil {
			return err
		}
	}
	return nil
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

// teamApps is the WHERE clause that limits an apps update to one team.
const teamApps = ` AND environment_id IN (
	SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?)`

// UpdateAppSettings saves the general fields a person edits. A new name must
// be free among the environment's apps and databases.
func (d *DB) UpdateAppSettings(ctx context.Context, teamID string, a App) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		taken, err := nameTaken(ctx, tx, a.EnvironmentID, a.Name, a.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		return affected(tx.ExecContext(ctx, `UPDATE apps SET name = ?, image = ?, port = ?, memory_mb = ?, cpus = ?,
				health_path = ?, health_cmd = ?, health_timeout = ?, start_command = ?, docker_options = ?, updated_at = ?
			WHERE id = ?`+teamApps,
			a.Name, a.Image, a.Port, a.MemoryMB, a.CPUs, a.HealthPath, a.HealthCmd, a.HealthTimeout,
			a.StartCommand, a.DockerOptions, now(), a.ID, teamID))
	})
}

// UpdateAppSource saves where a Git app's code comes from and how it is
// built.
func (d *DB) UpdateAppSource(ctx context.Context, teamID string, a App) error {
	if err := d.checkSourceOwnership(ctx, teamID, a); err != nil {
		return err
	}
	// The port is saved with the source: the build pack decides it for a
	// static site.
	return affected(d.ExecContext(ctx, `UPDATE apps SET repo_url = ?, repo_name = ?, branch = ?, build_pack = ?, dockerfile_path = ?,
			base_dir = ?, publish_dir = ?, spa_fallback = ?, git_source_id = ?, ssh_key_id = ?, auto_deploy = ?, port = ?, updated_at = ?
		WHERE id = ? AND source = 'git'`+teamApps,
		a.RepoURL, a.RepoName, a.Branch, a.BuildPack, a.DockerfilePath, a.BaseDir, a.PublishDir, a.SPAFallback,
		a.GitSourceID, a.SSHKeyID, a.AutoDeploy, a.Port, now(), a.ID, teamID))
}

// SetAppBuildServer chooses where a Git app's image is built: another
// server of the team, or "" for the app's own.
func (d *DB) SetAppBuildServer(ctx context.Context, teamID, id, serverID string) error {
	if serverID != "" {
		if _, err := d.Server(ctx, teamID, serverID); err != nil {
			return err
		}
	}
	// The app's own server is the same as no choice.
	return affected(d.ExecContext(ctx, `UPDATE apps SET build_server_id = CASE WHEN server_id = ?1 THEN '' ELSE ?1 END, updated_at = ?2 WHERE id = ?3`+teamApps,
		serverID, now(), id, teamID))
}

// SetAppWebhookSecret stores the (sealed) secret of the app's own push
// webhook.
func (d *DB) SetAppWebhookSecret(ctx context.Context, teamID, id, sealed string) error {
	return affected(d.ExecContext(ctx, `UPDATE apps SET webhook_secret = ?, updated_at = ? WHERE id = ?`+teamApps, sealed, now(), id, teamID))
}

// SetAppDeployToken stores the hash of the app's deploy token; "" revokes it.
func (d *DB) SetAppDeployToken(ctx context.Context, teamID, id, hash string) error {
	return affected(d.ExecContext(ctx, `UPDATE apps SET deploy_token_hash = ?, updated_at = ? WHERE id = ?`+teamApps, hash, now(), id, teamID))
}

// AppByDeployToken finds the app whose deploy token has this hash.
func (d *DB) AppByDeployToken(ctx context.Context, id, hash string) (App, error) {
	if hash == "" {
		return App{}, ErrNotFound
	}
	a, err := scanApp(d.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps a WHERE a.id = ? AND a.deploy_token_hash = ?`, id, hash))
	return a, notFound(err)
}

// AppsForPush returns the apps that a push to the given repository and
// branch should redeploy. With sourceID set, only apps connected through
// that GitHub App are returned.
func (d *DB) AppsForPush(ctx context.Context, sourceID, repoName, branch string) ([]App, error) {
	query := `SELECT ` + appColumns + ` FROM apps a WHERE a.source = 'git' AND a.auto_deploy = 1 AND a.repo_name = ? AND a.branch = ?`
	args := []any{strings.ToLower(repoName), branch}
	if sourceID != "" {
		query += ` AND a.git_source_id = ?`
		args = append(args, sourceID)
	}
	return d.queryApps(ctx, query+` ORDER BY a.created_at, a.rowid`, args...)
}

// SetAppStatus records what the app is doing.
func (d *DB) SetAppStatus(ctx context.Context, id, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE apps SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	return err
}

// SetAppStatusIf changes the status only when the app's serving container is
// still the given one. The monitor uses it so an event from a container that
// has since been replaced cannot overwrite the new container's status. It
// also leaves alone an app that is being deployed or was stopped on purpose:
// those statuses are set by the code doing the work, not by Docker events.
// It reports whether the status changed.
//
// serverID is the server that reported the container: what one server says
// never changes the status of an app on another.
func (d *DB) SetAppStatusIf(ctx context.Context, serverID, id, container, status string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE apps SET status = ?, updated_at = ? WHERE id = ? AND server_id = ? AND container = ? AND status NOT IN (?, ?, ?)`,
		status, now(), id, serverID, container, AppDeploying, AppStopped, status)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetAppRuntime records the container now serving the app. It returns
// ErrNotFound when the app was deleted in the meantime, so a deployment
// does not leave a container behind for an app that no longer exists.
func (d *DB) SetAppRuntime(ctx context.Context, id, status, container string, hostPort int, image string) error {
	return affected(d.ExecContext(ctx, `UPDATE apps SET status = ?, container = ?, host_port = ?, deployed_image = ?, updated_at = ? WHERE id = ?`,
		status, container, hostPort, image, now(), id))
}

// SetAppStopping marks an app stopped and takes its port out of the routes,
// while still remembering its container until that has been removed.
func (d *DB) SetAppStopping(ctx context.Context, id string) error {
	return affected(d.ExecContext(ctx, `UPDATE apps SET status = ?, host_port = 0, updated_at = ? WHERE id = ?`, AppStopped, now(), id))
}

// ClearAppContainer forgets a container once it has been removed.
func (d *DB) ClearAppContainer(ctx context.Context, id, container string) error {
	_, err := d.ExecContext(ctx, `UPDATE apps SET container = '', updated_at = ? WHERE id = ? AND container = ?`, now(), id, container)
	return err
}

// ResetStuckDeploying repairs apps left in "deploying" by a process that
// died: with no deployment queued or running for them, nothing would ever
// move them on. What was serving before is assumed to still be; the
// monitor's reconcile corrects that from Docker right after.
func (d *DB) ResetStuckDeploying(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `UPDATE apps
		SET status = CASE WHEN container <> '' THEN ? ELSE ? END, updated_at = ?
		WHERE status = ? AND id NOT IN (SELECT app_id FROM deployments WHERE status IN ('queued', 'running'))`,
		AppRunning, AppFailed, now(), AppDeploying)
	return err
}

// UsedHostPorts returns the loopback ports already assigned on a server.
func (d *DB) UsedHostPorts(ctx context.Context, serverID string) (map[int]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT host_port FROM apps WHERE server_id = ? AND host_port > 0
		UNION SELECT ep.host_port FROM service_endpoints ep JOIN services s ON s.id = ep.service_id WHERE s.server_id = ? AND ep.host_port > 0`, serverID, serverID)
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
