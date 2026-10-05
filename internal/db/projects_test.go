package db

import (
	"context"
	"testing"
)

// Whether an environment's network is still wanted on a server is asked of
// the records: every kind of resource counts, on that server only, and so
// does a preview, which the lists of apps leave out.
func TestEnvironmentUsesServer(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	server, _ := d.EnsureLocalServer(ctx, team, "")
	d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', ?, 'elsewhere', 'ssh', 1)`, team)
	project, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, project.ID)
	env := envs[0].ID
	other, err := d.CreateEnvironment(ctx, team, project.ID, "staging")
	if err != nil {
		t.Fatal(err)
	}

	uses := func(environment, server string) bool {
		t.Helper()
		used, err := d.EnvironmentUsesServer(ctx, environment, server)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}
	if uses(env, server.ID) {
		t.Fatal("an empty environment uses a server")
	}

	app, err := d.CreateApp(ctx, team, App{EnvironmentID: env, ServerID: server.ID, Name: "web", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	if !uses(env, server.ID) || uses(env, "othersrv") || uses(other.ID, server.ID) {
		t.Fatal("an app counts for its own environment on its own server, and nowhere else")
	}
	// Only a preview is left.
	d.Exec(`UPDATE apps SET source = 'git' WHERE id = ?`, app.ID)
	app, _ = d.AppByID(ctx, app.ID)
	if _, err := d.CreatePreview(ctx, app, 7, "web-pr-7", "feature"); err != nil {
		t.Fatal(err)
	}
	d.Exec(`UPDATE apps SET server_id = 'othersrv' WHERE id = ?`, app.ID)
	if !uses(env, server.ID) {
		t.Fatal("a preview does not count")
	}
	d.Exec(`DELETE FROM apps`)
	if uses(env, server.ID) {
		t.Fatal("still used after its apps are gone")
	}

	if _, err := d.CreateDatabase(ctx, team, Database{EnvironmentID: env, ServerID: server.ID, Name: "pg", Engine: "postgres", Image: "postgres:17-alpine"}); err != nil {
		t.Fatal(err)
	}
	if !uses(env, server.ID) {
		t.Fatal("a database does not count")
	}
	d.Exec(`DELETE FROM databases`)
	if _, err := d.CreateService(ctx, team, Service{EnvironmentID: env, ServerID: server.ID, Name: "site", Template: TemplateCustom}); err != nil {
		t.Fatal(err)
	}
	if !uses(env, server.ID) || uses(env, "othersrv") {
		t.Fatal("a service counts on its own server only")
	}

	for id, want := range map[string]bool{env: true, other.ID: true, "nosuchenviro": false, "": false} {
		if got, err := d.EnvironmentExists(ctx, id); err != nil || got != want {
			t.Errorf("EnvironmentExists(%q) = %v, %v", id, got, err)
		}
	}
}
