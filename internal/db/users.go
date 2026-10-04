package db

import (
	"context"
	"database/sql"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	CreatedAt    int64
}

type Session struct {
	TokenHash string
	UserID    string
	TeamID    string
	CSRFToken string
	ExpiresAt int64
	// Joined from users and team_members for the request context.
	User User
	Role string
}

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// CountUsers reports how many accounts exist. Zero means first-run setup is
// still open.
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstUser creates the owner account with its team, but only while no
// user exists. The check and the insert share a transaction so two concurrent
// setup requests cannot both succeed.
func (d *DB) CreateFirstUser(ctx context.Context, email, name, passwordHash string) (User, string, error) {
	u := User{ID: secret.RandomID(), Email: email, Name: name, PasswordHash: passwordHash, CreatedAt: now()}
	teamID := secret.RandomID()
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupClosed
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, name, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			u.ID, u.Email, u.Name, u.PasswordHash, u.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name, created_at) VALUES (?, ?, ?)`, teamID, "Default team", u.CreatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			teamID, u.ID, RoleOwner, u.CreatedAt)
		return err
	})
	return u, teamID, err
}

func (d *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := d.QueryRowContext(ctx, `SELECT id, email, name, password_hash, created_at FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt)
	return u, notFound(err)
}

func (d *DB) UserByID(ctx context.Context, id string) (User, error) {
	var u User
	err := d.QueryRowContext(ctx, `SELECT id, email, name, password_hash, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt)
	return u, notFound(err)
}

// FirstTeamOf returns the team a user lands in after login.
func (d *DB) FirstTeamOf(ctx context.Context, userID string) (string, error) {
	var id string
	err := d.QueryRowContext(ctx, `SELECT team_id FROM team_members WHERE user_id = ? ORDER BY created_at, rowid LIMIT 1`, userID).Scan(&id)
	return id, notFound(err)
}

func (d *DB) UpdateUserProfile(ctx context.Context, id, name, email string) error {
	return affected(d.ExecContext(ctx, `UPDATE users SET name = ?, email = ? WHERE id = ?`, name, email, id))
}

// SetPassword stores a new hash and ends every session of the user except
// keepTokenHash, so a changed password locks out anyone else who was signed in.
func (d *DB) SetPassword(ctx context.Context, userID, passwordHash, keepTokenHash string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepTokenHash); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = ?`, userID)
		return err
	})
}

func (d *DB) CreateSession(ctx context.Context, s Session) error {
	t := now()
	_, err := d.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, team_id, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.TokenHash, s.UserID, s.TeamID, s.CSRFToken, t, t, s.ExpiresAt)
	return err
}

// SessionByHash loads a live session with its user and role.
func (d *DB) SessionByHash(ctx context.Context, tokenHash string) (Session, error) {
	var s Session
	err := d.QueryRowContext(ctx, `
		SELECT s.token_hash, s.user_id, s.team_id, s.csrf_token, s.expires_at,
		       u.id, u.email, u.name, u.password_hash, u.created_at, m.role
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN team_members m ON m.team_id = s.team_id AND m.user_id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, now()).
		Scan(&s.TokenHash, &s.UserID, &s.TeamID, &s.CSRFToken, &s.ExpiresAt,
			&s.User.ID, &s.User.Email, &s.User.Name, &s.User.PasswordHash, &s.User.CreatedAt, &s.Role)
	return s, notFound(err)
}

// TouchSession slides the expiry forward.
func (d *DB) TouchSession(ctx context.Context, tokenHash string, expiresAt int64) error {
	_, err := d.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`, now(), expiresAt, tokenHash)
	return err
}

func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpired removes expired sessions and reset tokens.
func (d *DB) DeleteExpired(ctx context.Context) error {
	t := now()
	if _, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, t); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `DELETE FROM password_resets WHERE expires_at <= ?`, t)
	return err
}

func (d *DB) CreatePasswordReset(ctx context.Context, tokenHash, userID string, expiresAt int64) error {
	_, err := d.ExecContext(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, expiresAt)
	return err
}

// PasswordResetUser returns the user a live reset token belongs to.
func (d *DB) PasswordResetUser(ctx context.Context, tokenHash string) (string, error) {
	var userID string
	err := d.QueryRowContext(ctx, `SELECT user_id FROM password_resets WHERE token_hash = ? AND expires_at > ?`, tokenHash, now()).Scan(&userID)
	return userID, notFound(err)
}
