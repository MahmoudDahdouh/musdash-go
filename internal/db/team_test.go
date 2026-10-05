package db

import (
	"context"
	"errors"
	"testing"
)

// firstOwner returns the id of the team's first owner, who makes the
// invitations in these tests.
func firstOwner(t *testing.T, d *DB, team string) string {
	t.Helper()
	var id string
	if err := d.QueryRow(`SELECT user_id FROM team_members WHERE team_id = ? AND role = 'owner' ORDER BY created_at, rowid LIMIT 1`, team).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// invite stores an invitation and accepts it, returning the new member.
func invite(t *testing.T, d *DB, team, email, role string) User {
	t.Helper()
	ctx := context.Background()
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: email, Role: role, TokenHash: "h-" + email, InvitedBy: firstOwner(t, d, team), ExpiresAt: now() + 60}); err != nil {
		t.Fatal(err)
	}
	u, got, err := d.AcceptInvitation(ctx, "h-"+email, "Name", "hash")
	if err != nil || got != team {
		t.Fatalf("accept: %v (team %q)", err, got)
	}
	return u
}

func TestInvitations(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	owner, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")

	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "OWNER@example.com", Role: RoleMember, TokenHash: "x", ExpiresAt: now() + 60}); !errors.Is(err, ErrHasAccount) {
		t.Fatalf("inviting a member: want ErrHasAccount, got %v", err)
	}
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "new@example.com", Role: RoleOwner, TokenHash: "x", ExpiresAt: now() + 60}); err == nil {
		t.Fatal("an invitation cannot make an owner")
	}
	inv, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "new@example.com", Role: RoleAdmin, TokenHash: "first", InvitedBy: owner.ID, ExpiresAt: now() + 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "New@Example.com", Role: RoleMember, TokenHash: "second", ExpiresAt: now() + 60}); !IsUnique(err) {
		t.Fatalf("inviting twice: want a unique violation, got %v", err)
	}
	list, _ := d.ListInvitations(ctx, team)
	if len(list) != 1 || list[0].ID != inv.ID || list[0].InvitedBy != owner.ID || list[0].InviterName != "Owner" {
		t.Fatalf("list: %+v", list)
	}
	if _, err := d.InvitationByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := d.DeleteInvitation(ctx, "otherteam", inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team cancelling: %v", err)
	}

	u, got, err := d.AcceptInvitation(ctx, "first", "New Person", "hash2")
	if err != nil || got != team || u.Email != "new@example.com" {
		t.Fatalf("accept: %+v %q %v", u, got, err)
	}
	m, err := d.Member(ctx, team, u.ID)
	if err != nil || m.Role != RoleAdmin || m.Name != "New Person" {
		t.Fatalf("member: %+v %v", m, err)
	}
	// The link works once.
	if _, _, err := d.AcceptInvitation(ctx, "first", "Again", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second use: %v", err)
	}

	// An invitation that ran out cannot be used, is not listed, does not
	// stand in the way of a new one, and is cleaned up.
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "late@example.com", Role: RoleMember, TokenHash: "late", InvitedBy: owner.ID, ExpiresAt: now() - 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AcceptInvitation(ctx, "late", "Late", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	if list, _ := d.ListInvitations(ctx, team); len(list) != 0 {
		t.Fatalf("expired invitation listed: %+v", list)
	}
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "late@example.com", Role: RoleMember, TokenHash: "late2", ExpiresAt: now() - 1}); err != nil {
		t.Fatalf("inviting again after the first ran out: %v", err)
	}
	if err := d.DeleteExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM invitations`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d invitations left after clean-up", n)
	}

	// Somebody took the email in the meantime.
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "taken@example.com", Role: RoleMember, TokenHash: "taken", InvitedBy: owner.ID, ExpiresAt: now() + 60}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateUserProfile(ctx, u.ID, "New Person", "taken@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AcceptInvitation(ctx, "taken", "T", "hash"); !errors.Is(err, ErrHasAccount) {
		t.Fatalf("email taken meanwhile: %v", err)
	}
}

func TestRolesAndRemoval(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	owner, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	admin := invite(t, d, team, "admin@example.com", RoleAdmin)
	member := invite(t, d, team, "member@example.com", RoleMember)

	list, err := d.ListMembers(ctx, team)
	if err != nil || len(list) != 3 || list[0].UserID != owner.ID || list[1].UserID != admin.ID || list[2].UserID != member.ID {
		t.Fatalf("members, owners first: %+v %v", list, err)
	}

	// The only owner keeps the role and the account.
	if err := d.SetRole(ctx, team, owner.ID, RoleAdmin); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting the last owner: %v", err)
	}
	if err := d.RemoveMember(ctx, team, owner.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last owner: %v", err)
	}
	if err := d.SetRole(ctx, team, member.ID, "root"); err == nil {
		t.Fatal("an unknown role was accepted")
	}
	if err := d.SetRole(ctx, "otherteam", member.ID, RoleAdmin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team's member: %v", err)
	}

	// With a second owner, the first can step down.
	if err := d.SetRole(ctx, team, admin.ID, RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRole(ctx, team, owner.ID, RoleMember); err != nil {
		t.Fatal(err)
	}

	// Removing a member deletes the account and what hangs from it.
	if err := d.CreateSession(ctx, Session{TokenHash: "s", UserID: member.ID, TeamID: team, CSRFToken: "c", ExpiresAt: now() + 60}); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveMember(ctx, "otherteam", member.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team removing: %v", err)
	}
	if err := d.RemoveMember(ctx, team, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UserByID(ctx, member.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the account survived: %v", err)
	}
	if _, err := d.SessionByHash(ctx, "s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the session survived: %v", err)
	}
	// The email is free to be invited again.
	invite(t, d, team, "member@example.com", RoleMember)
}

func TestRoleRank(t *testing.T) {
	if !(RoleRank(RoleOwner) > RoleRank(RoleAdmin) && RoleRank(RoleAdmin) > RoleRank(RoleMember) && RoleRank(RoleMember) > RoleRank("")) {
		t.Fatal("roles are out of order")
	}
}

func TestSecondStepStorage(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	for _, h := range []string{"here", "elsewhere"} {
		if err := d.CreateSession(ctx, Session{TokenHash: h, UserID: u.ID, TeamID: team, CSRFToken: "c", ExpiresAt: now() + 60}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing is on until a pending key is confirmed, and only that key.
	if err := d.UseTOTPStep(ctx, u.ID, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a step was taken without a second step: %v", err)
	}
	if err := d.SetTOTPPending(ctx, u.ID, "sealed-key"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.UserByID(ctx, u.ID); got.TwoStep() || got.TOTPPending != "sealed-key" {
		t.Fatalf("pending: %+v", got)
	}
	if err := d.EnableTOTP(ctx, u.ID, "another-key", 100, "here", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a key that was not the pending one was enabled: %v", err)
	}
	if err := d.EnableTOTP(ctx, u.ID, "sealed-key", 100, "here", []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.UserByID(ctx, u.ID)
	if !got.TwoStep() || got.TOTPPending != "" || got.TOTPStep != 100 {
		t.Fatalf("enabled: %+v", got)
	}
	// Other sessions ended; this one carries the new state.
	if _, err := d.SessionByHash(ctx, "elsewhere"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another session survived: %v", err)
	}
	if s, err := d.SessionByHash(ctx, "here"); err != nil || !s.User.TwoStep() {
		t.Fatalf("this session: %+v %v", s.User, err)
	}
	if list, _ := d.ListMembers(ctx, team); len(list) != 1 || !list[0].TwoStep {
		t.Fatalf("members: %+v", list)
	}

	// A step is taken once, and never an earlier one after it.
	if err := d.UseTOTPStep(ctx, u.ID, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the confirming code's step was taken again: %v", err)
	}
	if err := d.UseTOTPStep(ctx, u.ID, 102); err != nil {
		t.Fatal(err)
	}
	for _, step := range []int64{101, 102} {
		if err := d.UseTOTPStep(ctx, u.ID, step); !errors.Is(err, ErrNotFound) {
			t.Fatalf("step %d after 102: %v", step, err)
		}
	}

	// Recovery codes: the person's own, once.
	if n, _ := d.CountRecoveryCodes(ctx, u.ID); n != 2 {
		t.Fatalf("%d codes", n)
	}
	if err := d.UseRecoveryCode(ctx, "someoneelse", "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another person's code: %v", err)
	}
	if err := d.UseRecoveryCode(ctx, u.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := d.UseRecoveryCode(ctx, u.ID, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code twice: %v", err)
	}
	if err := d.ReplaceRecoveryCodes(ctx, u.ID, []string{"n1", "n2", "n3"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UseRecoveryCode(ctx, u.ID, "c2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an old code after new ones were made: %v", err)
	}

	if err := d.DisableTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = d.UserByID(ctx, u.ID)
	if n, _ := d.CountRecoveryCodes(ctx, u.ID); got.TwoStep() || got.TOTPStep != 0 || n != 0 {
		t.Fatalf("disabled: %+v, %d codes", got, n)
	}
}

// Authority over an account does not outlive the role it came from.
func TestWhatARoleChangeEnds(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	owner, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	admin := invite(t, d, team, "admin@example.com", RoleAdmin)
	member := invite(t, d, team, "member@example.com", RoleMember)
	count := func(table, userID string) (n int) {
		d.QueryRow(`SELECT count(*) FROM `+table+` WHERE user_id = ?`, userID).Scan(&n)
		return n
	}
	give := func(userID string) {
		t.Helper()
		if err := d.CreateSession(ctx, Session{TokenHash: "s-" + userID, UserID: userID, TeamID: team, CSRFToken: "c", ExpiresAt: now() + 60}); err != nil {
			t.Fatal(err)
		}
		if err := d.CreatePasswordReset(ctx, "r-"+userID, userID, now()+60); err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateAPIToken(ctx, APIToken{UserID: userID, TeamID: team, Name: "ci", TokenHash: "t-" + userID, Ability: AbilityRead}); err != nil {
			t.Fatal(err)
		}
	}

	// Raised: what was made for or by the account under the lower role ends.
	give(member.ID)
	if err := d.SetRole(ctx, team, member.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "password_resets", "api_tokens"} {
		if n := count(table, member.ID); n != 0 {
			t.Errorf("%d rows of %s survived the promotion", n, table)
		}
	}
	// The same role again, or a lower one, ends none of it.
	give(member.ID)
	for _, role := range []string{RoleAdmin, RoleMember} {
		if err := d.SetRole(ctx, team, member.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"sessions", "password_resets", "api_tokens"} {
		if n := count(table, member.ID); n != 1 {
			t.Errorf("%s after a demotion: %d rows, want 1", table, n)
		}
	}

	// Lowered: the invitations they made are withdrawn. So are those of a
	// member who is removed.
	second := invite(t, d, team, "second@example.com", RoleAdmin)
	if err := d.SetRole(ctx, team, second.ID, RoleOwner); err != nil {
		t.Fatal(err)
	}
	for hash, maker := range map[string]string{"by-second": second.ID, "by-admin": admin.ID, "by-owner": owner.ID} {
		role := RoleMember
		if maker == second.ID {
			role = RoleAdmin
		}
		if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: hash + "@example.com", Role: role, TokenHash: hash, InvitedBy: maker, ExpiresAt: now() + 60}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetRole(ctx, team, second.ID, RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AcceptInvitation(ctx, "by-second", "X", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an invitation outlived its maker's demotion: %v", err)
	}
	if err := d.RemoveMember(ctx, team, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AcceptInvitation(ctx, "by-admin", "X", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an invitation outlived its maker's removal: %v", err)
	}
	if list, _ := d.ListInvitations(ctx, team); len(list) != 1 || list[0].TokenHash != "by-owner" {
		t.Fatalf("invitations left: %+v", list)
	}
	// Even a row that stayed behind is refused when the account is made:
	// its maker must still be somebody who may give that role.
	d.Exec(`UPDATE invitations SET role = 'admin', invited_by = ? WHERE token_hash = 'by-owner'`, second.ID)
	if _, _, err := d.AcceptInvitation(ctx, "by-owner", "X", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a Member's invitation for an Admin was accepted: %v", err)
	}
}

func TestManagingIsCheckedWhereItIsWritten(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	owner, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	admin := invite(t, d, team, "admin@example.com", RoleAdmin)
	other := invite(t, d, team, "other@example.com", RoleAdmin)
	member := invite(t, d, team, "member@example.com", RoleMember)
	d.Exec(`UPDATE users SET totp_secret = 'sealed' WHERE id IN (?, ?)`, other.ID, member.ID)

	// An Admin and an Admin, an Admin and an Owner, anybody and themselves,
	// a Member and anybody, somebody who is not in the team.
	for name, pair := range map[string][2]string{
		"admin on admin": {admin.ID, other.ID}, "admin on owner": {admin.ID, owner.ID}, "admin on self": {admin.ID, admin.ID},
		"owner on self": {owner.ID, owner.ID}, "member on admin": {member.ID, admin.ID}, "member on member": {member.ID, member.ID},
		"a stranger": {"nosuchuser", member.ID},
	} {
		if err := d.RemoveMemberBy(ctx, team, pair[0], pair[1]); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s, remove: %v", name, err)
		}
		if err := d.DisableTOTPBy(ctx, team, pair[0], pair[1]); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s, second step off: %v", name, err)
		}
		if err := d.CreatePasswordResetBy(ctx, team, pair[0], pair[1], "reset-"+name, now()+60); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s, reset link: %v", name, err)
		}
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM password_resets`).Scan(&n)
	if list, _ := d.ListMembers(ctx, team); len(list) != 4 || n != 0 {
		t.Fatalf("something was done: %d members, %d reset links", len(list), n)
	}
	if err := d.RemoveMemberBy(ctx, team, admin.ID, "nosuchuser"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown member: %v", err)
	}

	// What is allowed: an Admin for a Member, an Owner for an Admin.
	if err := d.CreatePasswordResetBy(ctx, team, admin.ID, member.ID, "reset-ok", now()+60); err != nil {
		t.Fatal(err)
	}
	if err := d.DisableTOTPBy(ctx, team, admin.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveMemberBy(ctx, team, admin.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveMemberBy(ctx, team, owner.ID, other.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordAndSecondStepEndAPITokens(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	tokens := func() (n int) {
		d.QueryRow(`SELECT count(*) FROM api_tokens WHERE user_id = ?`, u.ID).Scan(&n)
		return n
	}
	make := func() {
		t.Helper()
		if _, err := d.CreateAPIToken(ctx, APIToken{UserID: u.ID, TeamID: team, Name: "ci", TokenHash: secretish(), Ability: AbilityDeploy}); err != nil {
			t.Fatal(err)
		}
	}
	make()
	if err := d.SetPassword(ctx, u.ID, "newhash", "keep"); err != nil {
		t.Fatal(err)
	}
	if tokens() != 0 {
		t.Fatal("an API token survived a password change")
	}
	make()
	d.SetTOTPPending(ctx, u.ID, "sealed")
	if err := d.EnableTOTP(ctx, u.ID, "sealed", 1, "keep", nil); err != nil {
		t.Fatal(err)
	}
	if tokens() != 0 {
		t.Fatal("an API token survived turning the second step on")
	}
}

var secretishN int

func secretish() string {
	secretishN++
	return "hash-" + string(rune('a'+secretishN))
}
