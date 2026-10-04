package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// GitSource is a connection to a Git host. Today that is a GitHub App.
type GitSource struct {
	ID            string
	TeamID        string
	Name          string
	Kind          string
	AppID         int64
	Slug          string
	HTMLURL       string
	ClientID      string
	ClientSecret  string // sealed
	PrivateKey    string // sealed
	WebhookSecret string // sealed
	State         string
	CreatedAt     int64
}

// Ready reports whether GitHub has returned the App's credentials. A row
// that is not ready is a manifest flow a person started and has not
// finished.
func (g GitSource) Ready() bool { return g.AppID != 0 }

const gitSourceColumns = `id, team_id, name, kind, app_id, slug, html_url, client_id, client_secret, private_key, webhook_secret, state, created_at`

func scanGitSource(row interface{ Scan(...any) error }) (GitSource, error) {
	var g GitSource
	err := row.Scan(&g.ID, &g.TeamID, &g.Name, &g.Kind, &g.AppID, &g.Slug, &g.HTMLURL, &g.ClientID, &g.ClientSecret, &g.PrivateKey, &g.WebhookSecret, &g.State, &g.CreatedAt)
	return g, notFound(err)
}

// StartGitSource records a manifest flow that is about to be sent to
// GitHub. state ties GitHub's redirect back to this row and this team.
func (d *DB) StartGitSource(ctx context.Context, teamID, name, state string) (GitSource, error) {
	g := GitSource{ID: secret.RandomID(), TeamID: teamID, Name: name, Kind: "github_app", State: state, CreatedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO git_sources (id, team_id, name, kind, state, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.TeamID, g.Name, g.Kind, g.State, g.CreatedAt)
	return g, err
}

// PendingGitSource finds the unfinished manifest flow with this state for
// the team. A state can be used once: finishing the flow clears it.
func (d *DB) PendingGitSource(ctx context.Context, teamID, state string) (GitSource, error) {
	if state == "" {
		return GitSource{}, ErrNotFound
	}
	return scanGitSource(d.QueryRowContext(ctx, `SELECT `+gitSourceColumns+` FROM git_sources
		WHERE team_id = ? AND state = ? AND app_id = 0`, teamID, state))
}

// FinishGitSource stores the credentials GitHub returned. Secret fields
// must already be sealed.
func (d *DB) FinishGitSource(ctx context.Context, g GitSource) error {
	return affected(d.ExecContext(ctx, `UPDATE git_sources SET name = ?, app_id = ?, slug = ?, html_url = ?, client_id = ?,
			client_secret = ?, private_key = ?, webhook_secret = ?, state = ''
		WHERE id = ? AND team_id = ? AND app_id = 0`,
		g.Name, g.AppID, g.Slug, g.HTMLURL, g.ClientID, g.ClientSecret, g.PrivateKey, g.WebhookSecret, g.ID, g.TeamID))
}

// GitSource loads one source, scoped to the team.
func (d *DB) GitSource(ctx context.Context, teamID, id string) (GitSource, error) {
	return scanGitSource(d.QueryRowContext(ctx, `SELECT `+gitSourceColumns+` FROM git_sources WHERE id = ? AND team_id = ?`, id, teamID))
}

// GitSourceByID loads a source without a team check, for webhooks and jobs.
func (d *DB) GitSourceByID(ctx context.Context, id string) (GitSource, error) {
	return scanGitSource(d.QueryRowContext(ctx, `SELECT `+gitSourceColumns+` FROM git_sources WHERE id = ?`, id))
}

// ListGitSources returns the team's connected sources. Unfinished manifest
// flows are left out.
func (d *DB) ListGitSources(ctx context.Context, teamID string) ([]GitSource, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+gitSourceColumns+` FROM git_sources WHERE team_id = ? AND app_id <> 0 ORDER BY created_at, rowid`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitSource
	for rows.Next() {
		g, err := scanGitSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ErrInUse is returned when deleting a source or key that apps still use.
var ErrInUse = errors.New("still used by an app")

// DeleteGitSource removes a source unless an app still deploys through it.
func (d *DB) DeleteGitSource(ctx context.Context, teamID, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM apps WHERE git_source_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM git_sources WHERE id = ? AND team_id = ?`, id, teamID))
	})
}

// DeleteStaleGitSources removes manifest flows that were never finished.
func (d *DB) DeleteStaleGitSources(ctx context.Context, olderThan int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM git_sources WHERE app_id = 0 AND created_at < ?`, olderThan)
	return err
}

// SSHKey is a key pair musdash generated. The private half is sealed.
type SSHKey struct {
	ID         string
	TeamID     string
	Name       string
	PublicKey  string
	PrivateKey string // sealed
	CreatedAt  int64
}

const sshKeyColumns = `id, team_id, name, public_key, private_key, created_at`

func scanSSHKey(row interface{ Scan(...any) error }) (SSHKey, error) {
	var k SSHKey
	err := row.Scan(&k.ID, &k.TeamID, &k.Name, &k.PublicKey, &k.PrivateKey, &k.CreatedAt)
	return k, notFound(err)
}

func (d *DB) CreateSSHKey(ctx context.Context, teamID, name, publicKey, sealedPrivate string) (SSHKey, error) {
	k := SSHKey{ID: secret.RandomID(), TeamID: teamID, Name: name, PublicKey: publicKey, PrivateKey: sealedPrivate, CreatedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO ssh_keys (id, team_id, name, public_key, private_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		k.ID, k.TeamID, k.Name, k.PublicKey, k.PrivateKey, k.CreatedAt)
	return k, err
}

// SSHKey loads one key, scoped to the team.
func (d *DB) SSHKey(ctx context.Context, teamID, id string) (SSHKey, error) {
	return scanSSHKey(d.QueryRowContext(ctx, `SELECT `+sshKeyColumns+` FROM ssh_keys WHERE id = ? AND team_id = ?`, id, teamID))
}

// SSHKeyByID loads a key without a team check, for jobs.
func (d *DB) SSHKeyByID(ctx context.Context, id string) (SSHKey, error) {
	return scanSSHKey(d.QueryRowContext(ctx, `SELECT `+sshKeyColumns+` FROM ssh_keys WHERE id = ?`, id))
}

func (d *DB) ListSSHKeys(ctx context.Context, teamID string) ([]SSHKey, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+sshKeyColumns+` FROM ssh_keys WHERE team_id = ? ORDER BY created_at, rowid`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHKey
	for rows.Next() {
		k, err := scanSSHKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteSSHKey removes a key unless an app still clones with it.
func (d *DB) DeleteSSHKey(ctx context.Context, teamID, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM apps WHERE ssh_key_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND team_id = ?`, id, teamID))
	})
}

// SeenDelivery records a webhook delivery id and reports whether it had
// been seen before, so a redelivered event is acted on once.
func (d *DB) SeenDelivery(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := d.ExecContext(ctx, `INSERT INTO webhook_deliveries (id, received_at) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, id, now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 0, err
}

// DeleteOldDeliveries forgets delivery ids older than the cutoff.
func (d *DB) DeleteOldDeliveries(ctx context.Context, olderThan int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE received_at < ?`, olderThan)
	return err
}
