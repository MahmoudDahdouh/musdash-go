package db

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
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

func TestMigrateRefusesDuplicateNumbers(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	files := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (v INTEGER);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (v INTEGER);`)},
		"0002_c.sql": {Data: []byte(`CREATE TABLE c (v INTEGER);`)},
	}
	err = d.Migrate(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "share the number 2") {
		t.Fatalf("want an error naming the duplicate, got %v", err)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('a', 'b', 'c')`).Scan(&n)
	if n != 0 {
		t.Fatal("migrations ran although two share a number")
	}
}

// The embedded migrations themselves must be numbered 1..n without gaps or
// repeats, and the local-server index from the recovery migration must
// really exist afterwards.
func TestEmbeddedMigrations(t *testing.T) {
	d := openTest(t)
	rows, err := d.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var v int
		rows.Scan(&v)
		if v != want {
			t.Fatalf("migration numbers are not consecutive: got %d, want %d", v, want)
		}
		want++
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'servers_one_local'`).Scan(&n)
	if n != 1 {
		t.Fatal("the unique local-server index was not created")
	}
	if _, err := d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('t', 'T', 1)`); err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('s1', 't', 'a', 'local', 1)`)
	if _, err := d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('s2', 't', 'b', 'local', 1)`); !IsUnique(err) {
		t.Fatalf("a second local server was allowed: %v", err)
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
	if _, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx", Port: 80}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name in one environment: want ErrNameTaken, got %v", err)
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
	shop := Domain{ResourceKind: KindApp, ResourceID: app.ID, Host: "shop.example.com", TLS: true}
	if _, err := d.AddDomain(ctx, team, server.ID, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddDomain(ctx, team, server.ID, shop); !IsUnique(err) {
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

func TestResetStuckDeployingAndPrune(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	server, _ := d.EnsureLocalServer(ctx, team, "")
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	mk := func(name, container string) App {
		a, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: name, Image: "nginx", Port: 80})
		if err != nil {
			t.Fatal(err)
		}
		d.Exec(`UPDATE apps SET status = 'deploying', container = ? WHERE id = ?`, container, a.ID)
		return a
	}
	hadContainer := mk("had", "musdash-had-1")
	neverRan := mk("never", "")
	stillDeploying := mk("busy", "")
	if _, err := d.CreateDeployment(ctx, Deployment{AppID: stillDeploying.ID, Image: "nginx"}); err != nil {
		t.Fatal(err)
	}

	if err := d.ResetStuckDeploying(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{hadContainer.ID: AppRunning, neverRan.ID: AppFailed, stillDeploying.ID: AppDeploying} {
		if got, _ := d.AppByID(ctx, id); got.Status != want {
			t.Errorf("app %s: status %s, want %s", got.Name, got.Status, want)
		}
	}

	// Pruning keeps the newest finished deployments and never a live one.
	var ids []string
	for i := range 6 {
		dep, _ := d.CreateDeployment(ctx, Deployment{AppID: hadContainer.ID, Image: "nginx"})
		d.Exec(`UPDATE deployments SET status = 'success', created_at = ? WHERE id = ?`, 1000+i, dep.ID)
		ids = append(ids, dep.ID)
	}
	live, _ := d.CreateDeployment(ctx, Deployment{AppID: hadContainer.ID, Image: "nginx"})
	d.Exec(`UPDATE deployments SET created_at = 1 WHERE id = ?`, live.ID)
	removed, err := d.PruneDeployments(ctx, hadContainer.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || (removed[0] != ids[1] && removed[0] != ids[0]) {
		t.Fatalf("removed %v, want the two oldest finished of %v", removed, ids)
	}
	if _, err := d.DeploymentByID(ctx, live.ID); err != nil {
		t.Fatal("a queued deployment was pruned")
	}

	// Stopped apps are not routed, and an event cannot un-stop them.
	d.AddDomain(ctx, team, server.ID, Domain{ResourceKind: KindApp, ResourceID: hadContainer.ID, Host: "had.example.com", TLS: true})
	d.Exec(`UPDATE apps SET host_port = 20001 WHERE id = ?`, hadContainer.ID)
	if rows, _ := d.RoutesForServer(ctx, server.ID); len(rows) != 1 {
		t.Fatalf("%d routes for a running app", len(rows))
	}
	if err := d.SetAppStopping(ctx, hadContainer.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := d.RoutesForServer(ctx, server.ID); len(rows) != 0 {
		t.Fatal("a stopped app is still routed")
	}
	d.SetAppStatusIf(ctx, server.ID, hadContainer.ID, "musdash-had-1", AppExited)
	if got, _ := d.AppByID(ctx, hadContainer.ID); got.Status != AppStopped || got.Container != "musdash-had-1" {
		t.Fatalf("after stop: %+v", got)
	}
	if err := d.SetAppRuntime(ctx, "no-such-app", AppRunning, "c", 1, "i"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAppRuntime on a deleted app: %v", err)
	}
}

// A host can be routed more than once, by path, but only by one team and
// on one server.
func TestAHostIsSharedByPathWithinOneTeamAndServer(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	server, _ := d.EnsureLocalServer(ctx, team, "203.0.113.7")
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	newApp := func(teamID, envID, serverID, name string) App {
		t.Helper()
		a, err := d.CreateApp(ctx, teamID, App{EnvironmentID: envID, ServerID: serverID, Name: name, Image: "nginx", Port: 80})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	web, api := newApp(team, envs[0].ID, server.ID, "web"), newApp(team, envs[0].ID, server.ID, "api")
	add := func(teamID, serverID string, m Domain) error {
		_, err := d.AddDomain(ctx, teamID, serverID, m)
		return err
	}
	of := func(app App, host, path string) Domain {
		return Domain{ResourceKind: KindApp, ResourceID: app.ID, Host: host, Path: path, TLS: true}
	}
	if err := add(team, server.ID, of(web, "shop.example.com", "")); err != nil {
		t.Fatal(err)
	}
	guarded := of(api, "shop.example.com", "/api")
	guarded.StripPrefix, guarded.AuthUser, guarded.AuthHash = true, "ada", "$2a$10$hash"
	if err := add(team, server.ID, guarded); err != nil {
		t.Fatalf("another path of the team's own host: %v", err)
	}
	if err := add(team, server.ID, of(web, "shop.example.com", "/api")); !IsUnique(err) {
		t.Fatalf("the same host and path twice: %v", err)
	}
	list, _ := d.ListDomains(ctx, KindApp, api.ID)
	if len(list) != 1 || list[0].Path != "/api" || !list[0].StripPrefix || list[0].AuthUser != "ada" || list[0].AuthHash != "$2a$10$hash" {
		t.Fatalf("stored: %+v", list)
	}

	// Another team cannot take a path of the host, whichever path.
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('teamb', 'B', 1)`)
	d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('srvb', 'teamb', 'theirs', 'ssh', 1)`)
	pb, _ := d.CreateProject(ctx, "teamb", "Other", "")
	envsB, _ := d.ListEnvironments(ctx, pb.ID)
	theirs := newApp("teamb", envsB[0].ID, "srvb", "theirs")
	for _, path := range []string{"/admin", "/api/v2", ""} {
		if err := add("teamb", "srvb", of(theirs, "shop.example.com", path)); !errors.Is(err, ErrHostTaken) && !IsUnique(err) {
			t.Fatalf("another team on the host at %q: %v", path, err)
		}
	}
	if err := add("teamb", "srvb", of(theirs, "shop.example.com", "/admin")); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("another team on a free path of the host: %v", err)
	}
	if err := add("teamb", "srvb", of(theirs, "theirs.example.com", "")); err != nil {
		t.Fatalf("a host of their own: %v", err)
	}

	// Nor can the same team route it on a second server.
	d.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('srv2', ?, 'second', 'ssh', 1)`, team)
	far := newApp(team, envs[0].ID, "srv2", "far")
	if err := add(team, "srv2", of(far, "shop.example.com", "/far")); !errors.Is(err, ErrHostElsewhere) {
		t.Fatalf("the host on a second server: %v", err)
	}

	// A service's endpoint counts as its team's.
	d.Exec(`INSERT INTO services (id, environment_id, server_id, name, created_at, updated_at) VALUES ('svc1', ?, ?, 'stack', 1, 1)`, envs[0].ID, server.ID)
	d.Exec(`INSERT INTO service_endpoints (id, service_id, name) VALUES ('ep1', 'svc1', 'WEB')`)
	if err := d.SetEndpointDomain(ctx, "svc1", "ep1", "stack.example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := add(team, server.ID, of(web, "stack.example.com", "/app")); err != nil {
		t.Fatalf("a path beside the team's own service: %v", err)
	}
	if err := add("teamb", "srvb", of(theirs, "stack.example.com", "/x")); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("another team beside a service: %v", err)
	}

	// A row nobody can be found for shares its host with no one.
	d.Exec(`INSERT INTO domains (id, resource_kind, resource_id, host, created_at) VALUES ('orphan', 'app', 'gone', 'old.example.com', 1)`)
	if err := add(team, server.ID, of(web, "old.example.com", "/x")); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("a host with a row of unknown owner: %v", err)
	}

	// Routes carry what the proxy needs.
	d.Exec(`UPDATE apps SET host_port = 20001, container = 'c1', status = 'running' WHERE id = ?`, web.ID)
	d.Exec(`UPDATE apps SET host_port = 20002, container = 'c2', status = 'running' WHERE id = ?`, api.ID)
	rows, err := d.RoutesForServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.Host+r.Path+"|"+r.AuthUser)
	}
	want := "shop.example.com| shop.example.com/api|ada stack.example.com/app|"
	if strings.Join(got, " ") != want {
		t.Fatalf("routes %q, want %q", got, want)
	}
}

// The domains of an install made before paths existed are kept as they
// were by the migration that rebuilds the table.
func TestDomainsSurviveTheirTableBeingRebuilt(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Everything before the rebuild, then a row, then the rest.
	entries, _ := fs.ReadDir(migrations.FS, ".")
	before, after := fstest.MapFS{}, fstest.MapFS{}
	for _, e := range entries {
		raw, _ := fs.ReadFile(migrations.FS, e.Name())
		after[e.Name()] = &fstest.MapFile{Data: raw}
		if e.Name() < "0012" {
			before[e.Name()] = &fstest.MapFile{Data: raw}
		}
	}
	if err := d.Migrate(ctx, before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at) VALUES ('d1', 'app', 'a1', 'old.example.com', 1, 1, 42)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(ctx, after); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListDomains(ctx, KindApp, "a1")
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	if m := list[0]; m.ID != "d1" || m.Host != "old.example.com" || !m.TLS || !m.RedirectWWW || m.CreatedAt != 42 || m.Path != "" || m.AuthUser != "" {
		t.Fatalf("after the rebuild: %+v", m)
	}
}
