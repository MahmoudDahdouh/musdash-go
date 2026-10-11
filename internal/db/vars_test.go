package db

import (
	"context"
	"errors"
	"testing"
)

// One variable of a resource is added, changed, renamed and deleted by its
// name, and nothing of another resource is touched by it.
func TestEnvVarsOneAtATime(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	keys := func(id string) string {
		t.Helper()
		list, err := d.ListEnvVars(ctx, KindApp, id)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, v := range list {
			out += v.Key + "=" + v.Value + ";"
		}
		return out
	}

	if err := d.AddEnvVar(ctx, KindApp, "app1", EnvVar{Key: "A", Value: "sealed-a"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := d.AddEnvVar(ctx, KindApp, "app1", EnvVar{Key: "B", Value: "sealed-b", BuildTime: true}, 2); err != nil {
		t.Fatal(err)
	}
	// Another app's variables are not counted against this one's, and its
	// names are its own.
	if err := d.AddEnvVar(ctx, KindApp, "app2", EnvVar{Key: "A", Value: "other"}, 1); err != nil {
		t.Fatalf("the same name on another app: %v", err)
	}
	if err := d.AddEnvVar(ctx, KindApp, "app1", EnvVar{Key: "A", Value: "again"}, 3); !errors.Is(err, ErrVarExists) {
		t.Fatalf("a name that is taken: %v", err)
	}
	if err := d.AddEnvVar(ctx, KindApp, "app1", EnvVar{Key: "C", Value: "x"}, 2); !errors.Is(err, ErrTooManyVars) {
		t.Fatalf("one more than the limit: %v", err)
	}

	got, err := d.EnvVarByKey(ctx, KindApp, "app1", "B")
	if err != nil || got.Value != "sealed-b" || !got.BuildTime {
		t.Fatalf("B: %+v %v", got, err)
	}
	if _, err := d.EnvVarByKey(ctx, KindApp, "app2", "B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another app's variable was found: %v", err)
	}
	if _, err := d.EnvVarByKey(ctx, KindService, "app1", "B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another kind's variable was found: %v", err)
	}

	// A change keeps the name or gives another, but never one that is taken.
	if err := d.UpdateEnvVar(ctx, KindApp, "app1", "A", EnvVar{Key: "A", Value: "sealed-a2"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateEnvVar(ctx, KindApp, "app1", "A", EnvVar{Key: "B", Value: "x"}); !errors.Is(err, ErrVarExists) {
		t.Fatalf("renamed onto a taken name: %v", err)
	}
	if err := d.UpdateEnvVar(ctx, KindApp, "app1", "B", EnvVar{Key: "C", Value: "sealed-c"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateEnvVar(ctx, KindApp, "app1", "NOPE", EnvVar{Key: "NOPE", Value: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name that is not there: %v", err)
	}
	if got := keys("app1"); got != "A=sealed-a2;C=sealed-c;" {
		t.Fatalf("after the changes: %s", got)
	}
	if c, _ := d.EnvVarByKey(ctx, KindApp, "app1", "C"); c.BuildTime {
		t.Fatal("a change that said runtime left the variable build-time")
	}

	if err := d.DeleteEnvVar(ctx, KindApp, "app1", "A"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteEnvVar(ctx, KindApp, "app1", "A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	if got := keys("app1"); got != "C=sealed-c;" {
		t.Fatalf("after the delete: %s", got)
	}
	if got := keys("app2"); got != "A=other;" {
		t.Fatalf("another app's variables changed: %s", got)
	}
}

// The same for a shared variable, which is a team's: a row of another team
// is never found, changed or counted, whatever the scope id says.
func TestSharedVarsOneAtATime(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	p, _ := d.CreateProject(ctx, team, "Shop", "")

	add := func(teamID, scope, id, key string, limit int) error {
		return d.AddSharedVar(ctx, teamID, scope, id, EnvVar{Key: key, Value: "sealed-" + key}, limit)
	}
	if err := add(team, ScopeProject, p.ID, "A", 2); err != nil {
		t.Fatal(err)
	}
	if err := add(team, ScopeProject, p.ID, "A", 2); !errors.Is(err, ErrVarExists) {
		t.Fatalf("a name that is taken: %v", err)
	}
	if err := add(team, ScopeTeam, team, "A", 2); err != nil {
		t.Fatalf("the same name in another scope: %v", err)
	}
	if err := add(team, ScopeProject, p.ID, "B", 2); err != nil {
		t.Fatal(err)
	}
	if err := add(team, ScopeProject, p.ID, "C", 2); !errors.Is(err, ErrTooManyVars) {
		t.Fatalf("one more than the limit: %v", err)
	}

	if v, err := d.SharedVar(ctx, team, ScopeProject, p.ID, "A"); err != nil || v.Value != "sealed-A" {
		t.Fatalf("A: %+v %v", v, err)
	}
	// Another team that names this team's project finds and changes nothing.
	if _, err := d.SharedVar(ctx, "otherteam", ScopeProject, p.ID, "A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team found it: %v", err)
	}
	if err := d.UpdateSharedVar(ctx, "otherteam", ScopeProject, p.ID, "A", EnvVar{Key: "A", Value: "theirs"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team changed it: %v", err)
	}
	if err := d.DeleteSharedVar(ctx, "otherteam", ScopeProject, p.ID, "A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team deleted it: %v", err)
	}

	if err := d.UpdateSharedVar(ctx, team, ScopeProject, p.ID, "A", EnvVar{Key: "B", Value: "x"}); !errors.Is(err, ErrVarExists) {
		t.Fatalf("renamed onto a taken name: %v", err)
	}
	if err := d.UpdateSharedVar(ctx, team, ScopeProject, p.ID, "A", EnvVar{Key: "Z", Value: "sealed-z"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSharedVar(ctx, team, ScopeProject, p.ID, "B"); err != nil {
		t.Fatal(err)
	}
	list, err := d.SharedVars(ctx, team, ScopeProject, p.ID)
	if err != nil || len(list) != 1 || list[0].Key != "Z" || list[0].Value != "sealed-z" {
		t.Fatalf("the project's variables: %+v %v", list, err)
	}
	if v, err := d.SharedVar(ctx, team, ScopeTeam, team, "A"); err != nil || v.Value != "sealed-A" {
		t.Fatalf("the team's variable changed with the project's: %+v %v", v, err)
	}
}
