package db

import (
	"context"
	"errors"
	"testing"
)

// invite stores an invitation and accepts it, returning the new member.
func invite(t *testing.T, d *DB, team, email, role string) User {
	t.Helper()
	ctx := context.Background()
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: email, Role: role, TokenHash: "h-" + email, ExpiresAt: now() + 60}); err != nil {
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
	_, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")

	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "OWNER@example.com", Role: RoleMember, TokenHash: "x", ExpiresAt: now() + 60}); !errors.Is(err, ErrHasAccount) {
		t.Fatalf("inviting a member: want ErrHasAccount, got %v", err)
	}
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "new@example.com", Role: RoleOwner, TokenHash: "x", ExpiresAt: now() + 60}); err == nil {
		t.Fatal("an invitation cannot make an owner")
	}
	inv, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "new@example.com", Role: RoleAdmin, TokenHash: "first", InvitedBy: "Owner", ExpiresAt: now() + 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "New@Example.com", Role: RoleMember, TokenHash: "second", ExpiresAt: now() + 60}); !IsUnique(err) {
		t.Fatalf("inviting twice: want a unique violation, got %v", err)
	}
	list, _ := d.ListInvitations(ctx, team)
	if len(list) != 1 || list[0].ID != inv.ID || list[0].InvitedBy != "Owner" {
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
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "late@example.com", Role: RoleMember, TokenHash: "late", ExpiresAt: now() - 1}); err != nil {
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
	if _, err := d.CreateInvitation(ctx, Invitation{TeamID: team, Email: "taken@example.com", Role: RoleMember, TokenHash: "taken", ExpiresAt: now() + 60}); err != nil {
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
