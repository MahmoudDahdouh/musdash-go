package db

import (
	"context"
	"errors"
	"testing"
)

func TestSharedVarsGoWithWhatTheyBelongTo(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	staging, _ := d.CreateEnvironment(ctx, team, p.ID, "staging")
	d.Exec(`INSERT INTO ssh_keys (id, team_id, name, private_key, public_key, created_at) VALUES ('key1', ?, 'k', 'x', 'y', 1)`, team)
	server, err := d.CreateServer(ctx, Server{TeamID: team, Name: "fra", Host: "203.0.113.9", Port: 22, SSHUser: "root", SSHKeyID: "key1"})
	if err != nil {
		t.Fatal(err)
	}

	set := func(teamID, scope, id, key string) {
		t.Helper()
		if err := d.ReplaceSharedVars(ctx, teamID, scope, id, []EnvVar{{Key: key, Value: "sealed-" + key}}); err != nil {
			t.Fatal(err)
		}
	}
	set(team, ScopeTeam, team, "T")
	set(team, ScopeProject, p.ID, "P")
	set(team, ScopeEnvironment, envs[0].ID, "E")
	set(team, ScopeEnvironment, staging.ID, "STAGING_ONLY")
	set(team, ScopeServer, server.ID, "S")
	set("otherteam", ScopeTeam, "otherteam", "THEIRS")

	// A scope is the environment's own project and team, and the server
	// only when it is that team's.
	sc, err := d.ScopeOf(ctx, envs[0].ID, server.ID)
	if err != nil || sc.TeamID != team || sc.ProjectID != p.ID || sc.ServerID != server.ID {
		t.Fatalf("scope: %+v %v", sc, err)
	}
	if sc, _ := d.ScopeOf(ctx, envs[0].ID, "nosuchserver"); sc.ServerID != "" {
		t.Fatalf("an unknown server joined the scope: %+v", sc)
	}
	if _, err := d.ScopeOf(ctx, "nosuchenv", server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown environment: %v", err)
	}
	got, err := d.SharedFor(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if got[ScopeTeam]["T"] != "sealed-T" || got[ScopeProject]["P"] != "sealed-P" || got[ScopeEnvironment]["E"] != "sealed-E" || got[ScopeServer]["S"] != "sealed-S" {
		t.Fatalf("shared: %v", got)
	}
	if _, ok := got[ScopeEnvironment]["STAGING_ONLY"]; ok {
		t.Fatal("another environment's variable was returned")
	}
	if _, ok := got[ScopeTeam]["THEIRS"]; ok {
		t.Fatal("another team's variable was returned")
	}
	// A scope without a server has no server variables.
	sc.ServerID = ""
	if got, _ := d.SharedFor(ctx, sc); len(got[ScopeServer]) != 0 {
		t.Fatalf("server variables without a server: %v", got)
	}

	// One scope's list, and replacing it.
	if err := d.ReplaceSharedVars(ctx, team, ScopeProject, p.ID, []EnvVar{{Key: "B", Value: "2"}, {Key: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	list, _ := d.SharedVars(ctx, team, ScopeProject, p.ID)
	if len(list) != 2 || list[0].Key != "A" || list[1].Key != "B" {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := d.SharedVars(ctx, "otherteam", ScopeProject, p.ID); len(list) != 0 {
		t.Fatalf("another team read a project's variables: %+v", list)
	}
	if err := d.ReplaceSharedVars(ctx, team, ScopeProject, p.ID, []EnvVar{{Key: "A", Value: "1"}, {Key: "A", Value: "2"}}); !IsUnique(err) {
		t.Fatalf("a name twice: %v", err)
	}

	count := func(scope string) (n int) {
		d.QueryRow(`SELECT count(*) FROM shared_vars WHERE scope = ?`, scope).Scan(&n)
		return n
	}
	// An environment's variables go with it, a server's with it, and a
	// project's with it — its environments' too, which the database
	// deletes by cascade.
	if err := d.DeleteEnvironment(ctx, team, staging.ID); err != nil {
		t.Fatal(err)
	}
	if count(ScopeEnvironment) != 1 {
		t.Fatalf("%d environment variables after deleting one environment", count(ScopeEnvironment))
	}
	if err := d.DeleteServer(ctx, team, server.ID); err != nil {
		t.Fatal(err)
	}
	if count(ScopeServer) != 0 {
		t.Fatal("a deleted server's variables are still there")
	}
	if err := d.DeleteProject(ctx, team, p.ID); err != nil {
		t.Fatal(err)
	}
	if count(ScopeProject) != 0 || count(ScopeEnvironment) != 0 {
		t.Fatalf("after deleting the project: %d project, %d environment variables", count(ScopeProject), count(ScopeEnvironment))
	}
	if count(ScopeTeam) != 2 {
		t.Fatalf("team variables were touched: %d", count(ScopeTeam))
	}
}
