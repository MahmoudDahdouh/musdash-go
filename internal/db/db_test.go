package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

// openTest returns a migrated database in a temp directory.
func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPragmas(t *testing.T) {
	d := openTest(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1",
		"cache_size":   "-2000",
		"busy_timeout": "5000",
		"foreign_keys": "1",
	} {
		var got string
		if err := d.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", pragma, got, want)
		}
	}
	if n := d.Stats().MaxOpenConnections; n != 2 {
		t.Errorf("max open connections = %d, want 2", n)
	}
}

func TestMigrateIsIdempotentAndOrdered(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	files := fstest.MapFS{
		"0002_b.sql": {Data: []byte(`INSERT INTO a (v) VALUES (2);`)},
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (v INTEGER); INSERT INTO a (v) VALUES (1);`)},
	}
	for range 2 {
		if err := d.Migrate(ctx, files); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (each migration exactly once)", n)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	bad := fstest.MapFS{"0001_bad.sql": {Data: []byte(`CREATE TABLE ok (v INTEGER); CREATE TABLE broken (;`)}}
	if err := d.Migrate(ctx, bad); err == nil {
		t.Fatal("want error")
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'ok'`).Scan(&n)
	if n != 0 {
		t.Fatal("a failed migration left a table behind")
	}
}

func TestFirstUserOnlyOnce(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u, teamID, err := d.CreateFirstUser(ctx, "Owner@Example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateFirstUser(ctx, "second@example.com", "Second", "hash"); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("second setup: want ErrSetupClosed, got %v", err)
	}
	got, err := d.UserByEmail(ctx, "owner@example.com")
	if err != nil || got.ID != u.ID {
		t.Fatalf("email lookup must ignore case: %v", err)
	}
	if id, err := d.FirstTeamOf(ctx, u.ID); err != nil || id != teamID {
		t.Fatalf("team = %q, %v", id, err)
	}
}

func TestProjectsAreTeamScoped(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, teamA, err := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	if err != nil {
		t.Fatal(err)
	}
	// A second team, inserted directly: team management arrives in phase 8.
	if _, err := d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('teamb', 'B', 1)`); err != nil {
		t.Fatal(err)
	}

	p, err := d.CreateProject(ctx, teamA, "Shop", "")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := d.ListEnvironments(ctx, p.ID)
	if err != nil || len(envs) != 1 || envs[0].Name != DefaultEnvironment {
		t.Fatalf("new project must have a production environment: %v %v", envs, err)
	}

	if _, err := d.Project(ctx, "teamb", p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team read: want ErrNotFound, got %v", err)
	}
	if err := d.UpdateProject(ctx, "teamb", p.ID, "x", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team update: want ErrNotFound, got %v", err)
	}
	if err := d.DeleteProject(ctx, "teamb", p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team delete: want ErrNotFound, got %v", err)
	}
	if _, err := d.CreateEnvironment(ctx, "teamb", p.ID, "staging"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team add environment: want ErrNotFound, got %v", err)
	}
	if _, err := d.Environment(ctx, "teamb", envs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team environment read: want ErrNotFound, got %v", err)
	}
}

func TestEnvironmentRules(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)

	if err := d.DeleteEnvironment(ctx, team, envs[0].ID); !errors.Is(err, ErrLastEnvironment) {
		t.Fatalf("deleting the only environment: want ErrLastEnvironment, got %v", err)
	}
	staging, err := d.CreateEnvironment(ctx, team, p.ID, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateEnvironment(ctx, team, p.ID, "staging"); !IsUnique(err) {
		t.Fatalf("duplicate name: want unique violation, got %v", err)
	}
	if err := d.DeleteEnvironment(ctx, team, staging.ID); err != nil {
		t.Fatal(err)
	}

	// Deleting the project removes its environments (foreign keys enforced).
	if err := d.DeleteProject(ctx, team, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM environments`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d environments survived their project", n)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")

	live := Session{TokenHash: "live", UserID: u.ID, TeamID: team, CSRFToken: "c", ExpiresAt: now() + 60}
	dead := Session{TokenHash: "dead", UserID: u.ID, TeamID: team, CSRFToken: "c", ExpiresAt: now() - 1}
	for _, s := range []Session{live, dead} {
		if err := d.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.SessionByHash(ctx, "live")
	if err != nil || got.User.Email != "a@example.com" || got.Role != RoleOwner {
		t.Fatalf("live session: %+v %v", got, err)
	}
	if _, err := d.SessionByHash(ctx, "dead"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session must not load, got %v", err)
	}

	// Changing the password ends every other session.
	other := Session{TokenHash: "other", UserID: u.ID, TeamID: team, CSRFToken: "c", ExpiresAt: now() + 60}
	d.CreateSession(ctx, other)
	if err := d.SetPassword(ctx, u.ID, "newhash", "live"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SessionByHash(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("other session survived a password change")
	}
	if _, err := d.SessionByHash(ctx, "live"); err != nil {
		t.Fatal("the current session must survive a password change")
	}
}

func TestAppsAreTeamScopedAndBlockDeletion(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('teamb', 'B', 1)`)
	server, err := d.EnsureLocalServer(ctx, team, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := d.EnsureLocalServer(ctx, team, "ignored"); again.ID != server.ID {
		t.Fatal("a second local server was created")
	}
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	otherProject, _ := d.CreateProject(ctx, "teamb", "Other", "")
	otherEnvs, _ := d.ListEnvironments(ctx, otherProject.ID)

	app, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx", Port: 80}); !IsUnique(err) {
		t.Fatalf("duplicate name in one environment: want unique violation, got %v", err)
	}
	// Another team cannot place an app in this environment, on this server,
	// or read or change the app.
	if _, err := d.CreateApp(ctx, "teamb", App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "x", Image: "nginx", Port: 80}); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team create in our environment: %v", err)
	}
	if _, err := d.CreateApp(ctx, "teamb", App{EnvironmentID: otherEnvs[0].ID, ServerID: server.ID, Name: "x", Image: "nginx", Port: 80}); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team create on our server: %v", err)
	}
	if _, err := d.App(ctx, "teamb", app.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team read: %v", err)
	}
	app.Name = "hijacked"
	if err := d.UpdateAppSettings(ctx, "teamb", app); !errors.Is(err, ErrNotFound) {
		t.Errorf("other team update: %v", err)
	}

	// A project or environment with something deployed in it cannot be deleted.
	if err := d.DeleteProject(ctx, team, p.ID); !IsForeignKey(err) {
		t.Fatalf("deleting a project with an app: want a foreign-key error, got %v", err)
	}
	if _, err := d.AddDomain(ctx, KindApp, app.ID, "shop.example.com", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddDomain(ctx, KindApp, "another-app", "shop.example.com", true, false); !IsUnique(err) {
		t.Fatalf("a host used twice: want unique violation, got %v", err)
	}
	if err := d.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if used, _ := d.HostInUse(ctx, "shop.example.com"); used {
		t.Fatal("the domain outlived its app")
	}
	if err := d.DeleteProject(ctx, team, p.ID); err != nil {
		t.Fatalf("deleting the emptied project: %v", err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	if v, err := d.Setting(ctx, SettingInstanceDomain); err != nil || v != "" {
		t.Fatalf("unset setting: %q %v", v, err)
	}
	d.SetSetting(ctx, SettingInstanceDomain, "a.example.com")
	d.SetSetting(ctx, SettingInstanceDomain, "b.example.com")
	if v, _ := d.Setting(ctx, SettingInstanceDomain); v != "b.example.com" {
		t.Fatalf("got %q", v)
	}
}
