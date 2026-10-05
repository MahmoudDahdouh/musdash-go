package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// ErrLastOwner is returned when a change would leave the team without an
// Owner: nobody could manage the install after it.
var ErrLastOwner = errors.New("the team needs at least one owner")

// ErrNotAllowed is returned when the person asking may not do this to this
// member: an Admin to anybody above a Member, anybody to themselves.
var ErrNotAllowed = errors.New("your role does not allow this")

// ErrHasAccount is returned when an invitation names an email that already
// has an account. With one team per install, that person is a member.
var ErrHasAccount = errors.New("an account already uses this email")

type Team struct {
	ID   string
	Name string
}

// Member is a person in the team.
type Member struct {
	UserID   string
	Email    string
	Name     string
	Role     string
	JoinedAt int64
	// TwoStep reports whether the person signs in with a second step.
	TwoStep bool
}

// Invitation is a link that has not been used yet.
type Invitation struct {
	ID        string
	TeamID    string
	Email     string
	Role      string
	TokenHash string
	// InvitedBy is the id of the member who made it. An invitation goes
	// with its maker: when they are removed, or given a lower role, what
	// they handed out while they held the higher one stops working.
	InvitedBy string
	CreatedAt int64
	ExpiresAt int64
	// InviterName is filled by ListInvitations.
	InviterName string
}

// RoleRank orders the roles: a higher rank may do everything a lower one
// may. An unknown role ranks below all of them.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	}
	return 0
}

// MayInvite reports whether a person with actorRole may invite somebody
// as role: an Admin a Member, an Owner a Member or an Admin. Nobody is
// invited as an Owner: an Owner is made by an Owner changing a role.
func MayInvite(actorRole, role string) bool {
	switch role {
	case RoleMember:
		return RoleRank(actorRole) >= RoleRank(RoleAdmin)
	case RoleAdmin:
		return actorRole == RoleOwner
	}
	return false
}

// CanManage reports whether a person with actorRole may remove a member,
// or reset how they sign in: an Owner anybody else, an Admin a Member.
// Nobody does either to themselves here.
func CanManage(actorRole, actorID string, m Member) bool {
	if m.UserID == actorID {
		return false
	}
	return actorRole == RoleOwner || (actorRole == RoleAdmin && m.Role == RoleMember)
}

func (d *DB) Team(ctx context.Context, id string) (Team, error) {
	var t Team
	err := d.QueryRowContext(ctx, `SELECT id, name FROM teams WHERE id = ?`, id).Scan(&t.ID, &t.Name)
	return t, notFound(err)
}

func (d *DB) RenameTeam(ctx context.Context, id, name string) error {
	return affected(d.ExecContext(ctx, `UPDATE teams SET name = ? WHERE id = ?`, name, id))
}

// ListMembers returns the team's people, owners first.
func (d *DB) ListMembers(ctx context.Context, teamID string) ([]Member, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT u.id, u.email, u.name, m.role, m.created_at, u.totp_secret <> ''
		FROM team_members m JOIN users u ON u.id = m.user_id
		WHERE m.team_id = ?
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, m.created_at, u.id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt, &m.TwoStep); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Member loads one person of the team.
func (d *DB) Member(ctx context.Context, teamID, userID string) (Member, error) {
	var m Member
	err := d.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, m.role, m.created_at, u.totp_secret <> ''
		FROM team_members m JOIN users u ON u.id = m.user_id
		WHERE m.team_id = ? AND m.user_id = ?`, teamID, userID).
		Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt, &m.TwoStep)
	return m, notFound(err)
}

// keepsAnOwner fails with ErrLastOwner when userID is the team's only
// owner. It runs inside the transaction that is about to change them.
func keepsAnOwner(ctx context.Context, tx *sql.Tx, teamID, userID string) error {
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&role); err != nil {
		return notFound(err)
	}
	if role != RoleOwner {
		return nil
	}
	var owners int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_members WHERE team_id = ? AND role = ?`, teamID, RoleOwner).Scan(&owners); err != nil {
		return err
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

// SetRole gives a member another role. The team's only owner keeps theirs.
//
// A role is authority over an account, and whoever could act on the
// account under its old role must not carry that over:
//   - Raised: the person's sessions, reset links and API tokens end. An
//     Admin may make a reset link for a Member; held back until that
//     Member is made an Owner, it would be a reset link for an Owner.
//   - Lowered: the invitations they made are withdrawn. An Owner may
//     invite an Admin; an Owner who was made a Member may not have one
//     waiting.
func (d *DB) SetRole(ctx context.Context, teamID, userID, role string) error {
	if RoleRank(role) == 0 {
		return errors.New("unknown role")
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&old); err != nil {
			return notFound(err)
		}
		if role == old {
			return nil
		}
		if role != RoleOwner {
			if err := keepsAnOwner(ctx, tx, teamID, userID); err != nil {
				return err
			}
		}
		if err := affected(tx.ExecContext(ctx, `UPDATE team_members SET role = ? WHERE team_id = ? AND user_id = ?`, role, teamID, userID)); err != nil {
			return err
		}
		if RoleRank(role) > RoleRank(old) {
			for _, table := range []string{"sessions", "password_resets", "api_tokens"} {
				if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE user_id = ?`, userID); err != nil {
					return err
				}
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM invitations WHERE team_id = ? AND invited_by = ?`, teamID, userID)
		return err
	})
}

// managing checks, inside the transaction that is about to act, that
// actorID may still manage userID. The check a page made a moment ago is
// not enough: a role can change between the page and the write.
func managing(ctx context.Context, tx *sql.Tx, teamID, actorID, userID string) error {
	var actorRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, actorID).Scan(&actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotAllowed
		}
		return err
	}
	m := Member{UserID: userID}
	if err := tx.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&m.Role); err != nil {
		return notFound(err)
	}
	if !CanManage(actorRole, actorID, m) {
		return ErrNotAllowed
	}
	return nil
}

// removeMember takes a person out of the team inside a transaction.
func removeMember(ctx context.Context, tx *sql.Tx, teamID, userID string) error {
	if err := keepsAnOwner(ctx, tx, teamID, userID); err != nil {
		return err
	}
	if err := affected(tx.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)); err != nil {
		return err
	}
	// What they invited goes with them: a link made before leaving must
	// not be a way back in.
	if _, err := tx.ExecContext(ctx, `DELETE FROM invitations WHERE team_id = ? AND invited_by = ?`, teamID, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND NOT EXISTS (SELECT 1 FROM team_members WHERE user_id = ?)`, userID, userID)
	return err
}

// RemoveMember takes a person out of the team. An account is a member of
// the team and nothing else, so it is deleted with its sessions, tokens
// and second step: one left behind could not sign in, and could not be
// invited again while its email was taken.
func (d *DB) RemoveMember(ctx context.Context, teamID, userID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error { return removeMember(ctx, tx, teamID, userID) })
}

// RemoveMemberBy is RemoveMember for a person who asks through the Team
// page. It returns ErrNotAllowed when they may not, as things are now.
func (d *DB) RemoveMemberBy(ctx context.Context, teamID, actorID, userID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := managing(ctx, tx, teamID, actorID, userID); err != nil {
			return err
		}
		return removeMember(ctx, tx, teamID, userID)
	})
}

// CreatePasswordResetBy stores a reset link that actorID makes for another
// member, when they may.
func (d *DB) CreatePasswordResetBy(ctx context.Context, teamID, actorID, userID, tokenHash string, expiresAt int64) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := managing(ctx, tx, teamID, actorID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, expiresAt)
		return err
	})
}

// DisableTOTPBy turns off another member's second step, when actorID may.
func (d *DB) DisableTOTPBy(ctx context.Context, teamID, actorID, userID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := managing(ctx, tx, teamID, actorID, userID); err != nil {
			return err
		}
		return disableTOTP(ctx, tx, userID)
	})
}

// CreateInvitation stores an invitation. An email that already has an
// account is refused with ErrHasAccount; one that is already invited fails
// the unique index (IsUnique). An invitation that ran out is replaced.
func (d *DB) CreateInvitation(ctx context.Context, m Invitation) (Invitation, error) {
	m.ID = secret.RandomID()
	m.CreatedAt = now()
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = ?`, m.Email).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrHasAccount
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM invitations WHERE team_id = ? AND email = ? AND expires_at <= ?`, m.TeamID, m.Email, m.CreatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO invitations (id, team_id, email, role, token_hash, invited_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.TeamID, m.Email, m.Role, m.TokenHash, m.InvitedBy, m.CreatedAt, m.ExpiresAt)
		return err
	})
	return m, err
}

const invitationColumns = `id, team_id, email, role, token_hash, invited_by, created_at, expires_at`

func scanInvitation(row interface{ Scan(...any) error }) (Invitation, error) {
	var m Invitation
	err := row.Scan(&m.ID, &m.TeamID, &m.Email, &m.Role, &m.TokenHash, &m.InvitedBy, &m.CreatedAt, &m.ExpiresAt)
	return m, notFound(err)
}

// ListInvitations returns the team's invitations that can still be used,
// each with the name of who made it.
func (d *DB) ListInvitations(ctx context.Context, teamID string) ([]Invitation, error) {
	rows, err := d.QueryContext(ctx, `SELECT i.id, i.team_id, i.email, i.role, i.token_hash, i.invited_by, i.created_at, i.expires_at, coalesce(u.name, '')
		FROM invitations i LEFT JOIN users u ON u.id = i.invited_by
		WHERE i.team_id = ? AND i.expires_at > ? ORDER BY i.created_at, i.id`, teamID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var m Invitation
		if err := rows.Scan(&m.ID, &m.TeamID, &m.Email, &m.Role, &m.TokenHash, &m.InvitedBy, &m.CreatedAt, &m.ExpiresAt, &m.InviterName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InvitationByHash loads a live invitation by the hash of its token.
func (d *DB) InvitationByHash(ctx context.Context, tokenHash string) (Invitation, error) {
	return scanInvitation(d.QueryRowContext(ctx, `SELECT `+invitationColumns+` FROM invitations
		WHERE token_hash = ? AND expires_at > ?`, tokenHash, now()))
}

func (d *DB) DeleteInvitation(ctx context.Context, teamID, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM invitations WHERE id = ? AND team_id = ?`, id, teamID))
}

// AcceptInvitation creates the account an invitation is for and makes it a
// member with the invitation's role. The invitation is used up in the same
// transaction, so a link opened twice creates one account.
func (d *DB) AcceptInvitation(ctx context.Context, tokenHash, name, passwordHash string) (User, string, error) {
	var u User
	var teamID string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		inv, err := scanInvitation(tx.QueryRowContext(ctx, `SELECT `+invitationColumns+` FROM invitations
			WHERE token_hash = ? AND expires_at > ?`, tokenHash, now()))
		if err != nil {
			return err
		}
		// Asked again where the account is made: the maker must still be
		// somebody who may give this role. Removal and demotion withdraw
		// their invitations already; this holds whatever else changes.
		var makerRole string
		err = tx.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, inv.TeamID, inv.InvitedBy).Scan(&makerRole)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !MayInvite(makerRole, inv.Role)) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		u = User{ID: secret.RandomID(), Email: inv.Email, Name: name, PasswordHash: passwordHash, CreatedAt: now()}
		teamID = inv.TeamID
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, name, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			u.ID, u.Email, u.Name, u.PasswordHash, u.CreatedAt); err != nil {
			if IsUnique(err) {
				return ErrHasAccount
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			teamID, u.ID, inv.Role, u.CreatedAt); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM invitations WHERE id = ?`, inv.ID)
		return err
	})
	return u, teamID, err
}
