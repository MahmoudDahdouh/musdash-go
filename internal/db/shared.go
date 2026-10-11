package db

import (
	"context"
	"database/sql"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// The scopes of shared variables. A variable's scope_id is the id of the
// team, project, environment or server it belongs to.
const (
	ScopeTeam        = "team"
	ScopeProject     = "project"
	ScopeEnvironment = "environment"
	ScopeServer      = "server"
)

// SharedScope says where a resource is: the team, project, environment and
// server whose shared variables it may name.
type SharedScope struct {
	TeamID        string
	ProjectID     string
	EnvironmentID string
	ServerID      string
}

// ScopeOf works out the scope of something in an environment that runs on
// a server. The team is the environment's own, read from its project; the
// server counts only when it is that team's.
func (d *DB) ScopeOf(ctx context.Context, environmentID, serverID string) (SharedScope, error) {
	sc := SharedScope{EnvironmentID: environmentID}
	err := d.QueryRowContext(ctx, `SELECT p.id, p.team_id FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = ?`, environmentID).
		Scan(&sc.ProjectID, &sc.TeamID)
	if err != nil {
		return sc, notFound(err)
	}
	var id string
	err = d.QueryRowContext(ctx, `SELECT id FROM servers WHERE id = ? AND team_id = ?`, serverID, sc.TeamID).Scan(&id)
	if err == nil {
		sc.ServerID = id
	} else if err != sql.ErrNoRows {
		return sc, err
	}
	return sc, nil
}

// SharedFor returns the shared variables of a scope, by scope and name,
// with values still sealed. The team id filters every row: a variable of
// another team is never returned, whatever ids the scope holds.
func (d *DB) SharedFor(ctx context.Context, sc SharedScope) (map[string]map[string]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT scope, key, value FROM shared_vars WHERE team_id = ?1 AND (
		(scope = 'team' AND scope_id = ?1) OR (scope = 'project' AND scope_id = ?2) OR
		(scope = 'environment' AND scope_id = ?3) OR (scope = 'server' AND scope_id = ?4 AND ?4 <> ''))`,
		sc.TeamID, sc.ProjectID, sc.EnvironmentID, sc.ServerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var scope, key, value string
		if err := rows.Scan(&scope, &key, &value); err != nil {
			return nil, err
		}
		if out[scope] == nil {
			out[scope] = map[string]string{}
		}
		out[scope][key] = value
	}
	return out, rows.Err()
}

// SharedVars lists the shared variables of one team, project, environment
// or server, with values still sealed. Call it with a scope id a loader
// returned for the team.
func (d *DB) SharedVars(ctx context.Context, teamID, scope, scopeID string) ([]EnvVar, error) {
	rows, err := d.QueryContext(ctx, `SELECT key, value FROM shared_vars WHERE team_id = ? AND scope = ? AND scope_id = ? ORDER BY key`, teamID, scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var v EnvVar
		if err := rows.Scan(&v.Key, &v.Value); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReplaceSharedVars swaps the whole set of one scope in one transaction.
// Values must already be sealed.
func (d *DB) ReplaceSharedVars(ctx context.Context, teamID, scope, scopeID string, vars []EnvVar) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM shared_vars WHERE team_id = ? AND scope = ? AND scope_id = ?`, teamID, scope, scopeID); err != nil {
			return err
		}
		for _, v := range vars {
			if _, err := tx.ExecContext(ctx, `INSERT INTO shared_vars (id, team_id, scope, scope_id, key, value) VALUES (?, ?, ?, ?, ?, ?)`,
				secret.RandomID(), teamID, scope, scopeID, v.Key, v.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

// SharedVar returns one variable of a scope, its value still sealed. Like
// every query here it filters on the team: a scope id of another team's
// project, environment or server finds nothing.
func (d *DB) SharedVar(ctx context.Context, teamID, scope, scopeID, key string) (EnvVar, error) {
	v := EnvVar{Key: key}
	err := d.QueryRowContext(ctx, `SELECT value FROM shared_vars WHERE team_id = ? AND scope = ? AND scope_id = ? AND key = ?`,
		teamID, scope, scopeID, key).Scan(&v.Value)
	return v, notFound(err)
}

// AddSharedVar gives a scope one more variable, unless it has limit of
// them already; the count and the insert are one transaction. The value
// must already be sealed. Call it with a scope id a loader returned for
// the team: the row is written under both.
func (d *DB) AddSharedVar(ctx context.Context, teamID, scope, scopeID string, v EnvVar, limit int) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM shared_vars WHERE team_id = ? AND scope = ? AND scope_id = ?`, teamID, scope, scopeID).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return ErrTooManyVars
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO shared_vars (id, team_id, scope, scope_id, key, value) VALUES (?, ?, ?, ?, ?, ?)`,
			secret.RandomID(), teamID, scope, scopeID, v.Key, v.Value)
		if IsUnique(err) {
			return ErrVarExists
		}
		return err
	})
}

// UpdateSharedVar changes the variable a scope has under key into v, which
// may give it another name. The value must already be sealed.
func (d *DB) UpdateSharedVar(ctx context.Context, teamID, scope, scopeID, key string, v EnvVar) error {
	res, err := d.ExecContext(ctx, `UPDATE shared_vars SET key = ?, value = ? WHERE team_id = ? AND scope = ? AND scope_id = ? AND key = ?`,
		v.Key, v.Value, teamID, scope, scopeID, key)
	if IsUnique(err) {
		return ErrVarExists
	}
	return affected(res, err)
}

// DeleteSharedVar removes one variable of a scope.
func (d *DB) DeleteSharedVar(ctx context.Context, teamID, scope, scopeID, key string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM shared_vars WHERE team_id = ? AND scope = ? AND scope_id = ? AND key = ?`, teamID, scope, scopeID, key))
}
