package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// SetTOTPPending stores a key that was shown to the person and waits for
// a code to confirm it. It replaces one that was never confirmed.
func (d *DB) SetTOTPPending(ctx context.Context, userID, sealedKey string) error {
	return affected(d.ExecContext(ctx, `UPDATE users SET totp_pending = ? WHERE id = ?`, sealedKey, userID))
}

// EnableTOTP makes the pending key the person's second step. step is the
// step of the code that confirmed it, so that code cannot be used again
// to sign in. Every other session ends, and the recovery codes are
// replaced by the given ones.
func (d *DB) EnableTOTP(ctx context.Context, userID, sealedKey string, step int64, keepTokenHash string, codeHashes []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET totp_secret = ?, totp_pending = '', totp_step = ? WHERE id = ? AND totp_pending = ?`,
			sealedKey, step, userID, sealedKey)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepTokenHash); err != nil {
			return err
		}
		return replaceRecoveryCodes(ctx, tx, userID, codeHashes)
	})
}

// DisableTOTP removes a person's second step and its recovery codes.
func (d *DB) DisableTOTP(ctx context.Context, userID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_pending = '', totp_step = 0 WHERE id = ?`, userID)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
		return err
	})
}

// UseTOTPStep records that a code of this step was accepted. It returns
// ErrNotFound when a code of this step or a later one was accepted
// before: the check and the write are one statement, so two requests with
// the same code cannot both pass.
func (d *DB) UseTOTPStep(ctx context.Context, userID string, step int64) error {
	return affected(d.ExecContext(ctx, `UPDATE users SET totp_step = ? WHERE id = ? AND totp_secret <> '' AND totp_step < ?`, step, userID, step))
}

// UseRecoveryCode uses a recovery code up. It returns ErrNotFound for a
// code that is not the person's or was used.
func (d *DB) UseRecoveryCode(ctx context.Context, userID, codeHash string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?`, userID, codeHash))
}

// ReplaceRecoveryCodes gives a person a new set of recovery codes.
func (d *DB) ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error { return replaceRecoveryCodes(ctx, tx, userID, codeHashes) })
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, userID string, codeHashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range codeHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// CountRecoveryCodes reports how many recovery codes a person has left.
func (d *DB) CountRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// What an API token may do. A token that may deploy may also read.
const (
	AbilityRead   = "read"
	AbilityDeploy = "deploy"
)

// MaxAPITokens is how many tokens one person may have.
const MaxAPITokens = 20

// ErrTooMany is returned when a person has as many of something as they may.
var ErrTooMany = errors.New("no more can be added")

// APIToken is a person's token for the API. It acts as that person in
// that team.
type APIToken struct {
	ID         string
	UserID     string
	TeamID     string
	Name       string
	TokenHash  string
	Ability    string
	CreatedAt  int64
	LastUsedAt int64
	ExpiresAt  int64 // 0 for one that does not expire
	// Filled by TokenByHash: who the token acts as, and their role now.
	UserName  string
	UserEmail string
	Role      string
}

// MayDeploy reports whether the token may do more than read.
func (t APIToken) MayDeploy() bool { return t.Ability == AbilityDeploy }

// CreateAPIToken stores a token by its hash.
func (d *DB) CreateAPIToken(ctx context.Context, t APIToken) (APIToken, error) {
	t.ID = secret.RandomID()
	t.CreatedAt = now()
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = ?`, t.UserID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxAPITokens {
			return ErrTooMany
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, team_id, name, token_hash, ability, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.UserID, t.TeamID, t.Name, t.TokenHash, t.Ability, t.CreatedAt, t.ExpiresAt)
		return err
	})
	return t, err
}

// ListAPITokens returns a person's tokens for a team, newest first. The
// hashes are not read: no page has a use for them.
func (d *DB) ListAPITokens(ctx context.Context, userID, teamID string) ([]APIToken, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, user_id, team_id, name, ability, created_at, last_used_at, expires_at
		FROM api_tokens WHERE user_id = ? AND team_id = ? ORDER BY created_at DESC, id`, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.TeamID, &t.Name, &t.Ability, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes one of a person's own tokens.
func (d *DB) DeleteAPIToken(ctx context.Context, userID, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID))
}

// TokenByHash loads a live token with the role its person holds now. A
// token whose person is no longer in the team is not found: the join on
// the membership is what makes a token end with it.
func (d *DB) TokenByHash(ctx context.Context, hash string) (APIToken, error) {
	var t APIToken
	err := d.QueryRowContext(ctx, `
		SELECT t.id, t.user_id, t.team_id, t.name, t.ability, t.created_at, t.last_used_at, t.expires_at, u.name, u.email, m.role
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		JOIN team_members m ON m.team_id = t.team_id AND m.user_id = t.user_id
		WHERE t.token_hash = ? AND (t.expires_at = 0 OR t.expires_at > ?)`, hash, now()).
		Scan(&t.ID, &t.UserID, &t.TeamID, &t.Name, &t.Ability, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.UserName, &t.UserEmail, &t.Role)
	return t, notFound(err)
}

// TouchAPIToken records that a token was used just now.
func (d *DB) TouchAPIToken(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now(), id)
	return err
}
