package db

import (
	"context"
	"database/sql"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// The tables in this file hang off a (kind, id) pair so apps, databases and
// services share them. Callers pass a resource they have already loaded
// through a team-scoped query; these functions do not re-check the team.

type Domain struct {
	ID           string
	ResourceKind string
	ResourceID   string
	Host         string
	TLS          bool
	RedirectWWW  bool
	CreatedAt    int64
}

func (d *DB) ListDomains(ctx context.Context, kind, id string) ([]Domain, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, resource_kind, resource_id, host, tls, redirect_www, created_at
		FROM domains WHERE resource_kind = ? AND resource_id = ? ORDER BY created_at, rowid`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var m Domain
		if err := rows.Scan(&m.ID, &m.ResourceKind, &m.ResourceID, &m.Host, &m.TLS, &m.RedirectWWW, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddDomain routes a host to a resource. A host already in use anywhere on
// the install fails with a unique violation (see IsUnique).
func (d *DB) AddDomain(ctx context.Context, kind, id, host string, tls, redirectWWW bool) (Domain, error) {
	m := Domain{ID: secret.RandomID(), ResourceKind: kind, ResourceID: id, Host: host, TLS: tls, RedirectWWW: redirectWWW, CreatedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, m.ID, m.ResourceKind, m.ResourceID, m.Host, m.TLS, m.RedirectWWW, m.CreatedAt)
	return m, err
}

// DeleteDomain removes one domain of the given resource.
func (d *DB) DeleteDomain(ctx context.Context, kind, resourceID, domainID string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM domains WHERE id = ? AND resource_kind = ? AND resource_id = ?`, domainID, kind, resourceID))
}

// HostInUse reports whether any resource already routes the host.
func (d *DB) HostInUse(ctx context.Context, host string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE host = ?`, host).Scan(&n)
	return n > 0, err
}

// EnvVar is one environment variable. Value holds the sealed text in the
// database and the plain text once opened by the caller.
type EnvVar struct {
	Key       string
	Value     string
	BuildTime bool
}

// ListEnvVars returns a resource's variables with values still sealed.
func (d *DB) ListEnvVars(ctx context.Context, kind, id string) ([]EnvVar, error) {
	rows, err := d.QueryContext(ctx, `SELECT key, value, build_time FROM env_vars
		WHERE resource_kind = ? AND resource_id = ? ORDER BY key`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var v EnvVar
		if err := rows.Scan(&v.Key, &v.Value, &v.BuildTime); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReplaceEnvVars swaps a resource's whole variable set in one transaction.
// Values must already be sealed.
func (d *DB) ReplaceEnvVars(ctx context.Context, kind, id string, vars []EnvVar) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM env_vars WHERE resource_kind = ? AND resource_id = ?`, kind, id); err != nil {
			return err
		}
		for _, v := range vars {
			if _, err := tx.ExecContext(ctx, `INSERT INTO env_vars (id, resource_kind, resource_id, key, value, build_time)
				VALUES (?, ?, ?, ?, ?, ?)`, secret.RandomID(), kind, id, v.Key, v.Value, v.BuildTime); err != nil {
				return err
			}
		}
		return nil
	})
}

// Storage kinds.
const (
	StorageVolume = "volume" // a Docker-managed volume
	StorageBind   = "bind"   // a directory on the server
	StorageFile   = "file"   // one file whose content musdash writes
)

type Storage struct {
	ID           string
	ResourceKind string
	ResourceID   string
	Kind         string
	Source       string
	Target       string
	Content      string // sealed
	CreatedAt    int64
}

func (d *DB) ListStorages(ctx context.Context, kind, id string) ([]Storage, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, resource_kind, resource_id, kind, source, target, content, created_at
		FROM storages WHERE resource_kind = ? AND resource_id = ? ORDER BY target`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Storage
	for rows.Next() {
		var s Storage
		if err := rows.Scan(&s.ID, &s.ResourceKind, &s.ResourceID, &s.Kind, &s.Source, &s.Target, &s.Content, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) AddStorage(ctx context.Context, s Storage) (Storage, error) {
	s.ID = secret.RandomID()
	s.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO storages (id, resource_kind, resource_id, kind, source, target, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.ID, s.ResourceKind, s.ResourceID, s.Kind, s.Source, s.Target, s.Content, s.CreatedAt)
	return s, err
}

func (d *DB) DeleteStorage(ctx context.Context, kind, resourceID, storageID string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM storages WHERE id = ? AND resource_kind = ? AND resource_id = ?`, storageID, kind, resourceID))
}

// Deployment statuses.
const (
	DeployQueued  = "queued"
	DeployRunning = "running"
	DeploySuccess = "success"
	DeployFailed  = "failed"
)

type Deployment struct {
	ID         string
	AppID      string
	Status     string
	Trigger    string
	Image      string
	CommitSHA  string
	LogPath    string
	Error      string
	CreatedAt  int64
	StartedAt  int64
	FinishedAt int64
}

const deploymentColumns = `id, app_id, status, trigger, image, commit_sha, log_path, error, created_at, started_at, finished_at`

func scanDeployment(row interface{ Scan(...any) error }) (Deployment, error) {
	var m Deployment
	err := row.Scan(&m.ID, &m.AppID, &m.Status, &m.Trigger, &m.Image, &m.CommitSHA, &m.LogPath, &m.Error, &m.CreatedAt, &m.StartedAt, &m.FinishedAt)
	return m, err
}

func (d *DB) CreateDeployment(ctx context.Context, m Deployment) (Deployment, error) {
	m.ID = secret.RandomID()
	m.Status = DeployQueued
	m.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO deployments (id, app_id, status, trigger, image, commit_sha, log_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.AppID, m.Status, m.Trigger, m.Image, m.CommitSHA, m.LogPath, m.CreatedAt)
	return m, err
}

// Deployment loads one deployment of the given app.
func (d *DB) Deployment(ctx context.Context, appID, id string) (Deployment, error) {
	m, err := scanDeployment(d.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ? AND app_id = ?`, id, appID))
	return m, notFound(err)
}

// DeploymentByID loads a deployment for a background job.
func (d *DB) DeploymentByID(ctx context.Context, id string) (Deployment, error) {
	m, err := scanDeployment(d.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, id))
	return m, notFound(err)
}

// ListDeployments returns an app's most recent deployments, newest first.
func (d *DB) ListDeployments(ctx context.Context, appID string, limit int) ([]Deployment, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE app_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		m, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// QueuedDeployment returns the app's deployment that is waiting to start,
// or ErrNotFound. A burst of pushes then results in one more deployment,
// which builds the newest commit, rather than one per push.
func (d *DB) QueuedDeployment(ctx context.Context, appID string) (Deployment, error) {
	m, err := scanDeployment(d.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments
		WHERE app_id = ? AND status = 'queued' ORDER BY created_at DESC, rowid DESC LIMIT 1`, appID))
	return m, notFound(err)
}

// SetDeploymentBuild records what a Git deployment built.
func (d *DB) SetDeploymentBuild(ctx context.Context, id, image, commit string) error {
	_, err := d.ExecContext(ctx, `UPDATE deployments SET image = ?, commit_sha = ? WHERE id = ?`, image, commit, id)
	return err
}

func (d *DB) StartDeployment(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE deployments SET status = ?, started_at = ? WHERE id = ?`, DeployRunning, now(), id)
	return err
}

func (d *DB) FinishDeployment(ctx context.Context, id, status, errText string) error {
	_, err := d.ExecContext(ctx, `UPDATE deployments SET status = ?, error = ?, finished_at = ? WHERE id = ?`, status, errText, now(), id)
	return err
}

// FailStaleDeployments marks deployments that were queued or running when the
// process stopped and whose job is gone. Called at start-up.
func (d *DB) FailStaleDeployments(ctx context.Context, reason string) error {
	_, err := d.ExecContext(ctx, `UPDATE deployments SET status = ?, error = ?, finished_at = ?
		WHERE status IN ('queued', 'running') AND id NOT IN (
			SELECT json_extract(payload, '$.deployment_id') FROM jobs WHERE kind = 'deploy' AND status IN ('queued', 'running'))`,
		DeployFailed, reason, now())
	return err
}

// ProtectedDeployments returns the ids of deployments whose containers must
// not be cleaned up: those queued or running, and those that finished after
// the given time (their old container may still be draining).
func (d *DB) ProtectedDeployments(ctx context.Context, finishedAfter int64) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT id FROM deployments WHERE status IN ('queued', 'running') OR finished_at > ?`, finishedAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeploymentIDs returns every deployment id of an app.
func (d *DB) DeploymentIDs(ctx context.Context, appID string) ([]string, error) {
	return d.queryIDs(ctx, `SELECT id FROM deployments WHERE app_id = ?`, appID)
}

// PruneDeployments deletes an app's finished deployments beyond the newest
// keep and returns the ids removed, so their log files can go too.
func (d *DB) PruneDeployments(ctx context.Context, appID string, keep int) ([]string, error) {
	ids, err := d.queryIDs(ctx, `SELECT id FROM deployments WHERE app_id = ? AND status IN ('success', 'failed')
		ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?`, appID, keep)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	for _, id := range ids {
		if _, err := d.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (d *DB) queryIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountDomains reports how many domains a resource has.
func (d *DB) CountDomains(ctx context.Context, kind, id string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE resource_kind = ? AND resource_id = ?`, kind, id).Scan(&n)
	return n, err
}

// RouteRow is one host to publish in a server's routes file.
type RouteRow struct {
	Host        string
	TLS         bool
	RedirectWWW bool
	HostPort    int
}

// RoutesForServer returns the domains of every app that is serving on the
// given server.
func (d *DB) RoutesForServer(ctx context.Context, serverID string) ([]RouteRow, error) {
	// An app is routed while it has a serving container; a service's
	// endpoint while its stack is up, also when part of it is not.
	rows, err := d.QueryContext(ctx, `SELECT m.host, m.tls, m.redirect_www, a.host_port
		FROM domains m JOIN apps a ON m.resource_kind = 'app' AND m.resource_id = a.id
		WHERE a.server_id = ? AND a.host_port > 0 AND a.container <> '' AND a.status <> 'stopped'
		UNION ALL
		SELECT m.host, m.tls, m.redirect_www, ep.host_port
		FROM domains m JOIN service_endpoints ep ON m.resource_kind = 'service' AND m.resource_id = ep.id
			JOIN services s ON s.id = ep.service_id
		WHERE s.server_id = ? AND ep.host_port > 0 AND s.status IN ('running', 'degraded')
		ORDER BY 1`, serverID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRow
	for rows.Next() {
		var r RouteRow
		if err := rows.Scan(&r.Host, &r.TLS, &r.RedirectWWW, &r.HostPort); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
