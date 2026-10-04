package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

const testDBPassword = "Zx9GeneratedPassw0rdForTests1234"

// newDatabase creates a database row in the test environment.
func (e *env) newDatabase(engine, name string, publicPort int) db.Database {
	e.t.Helper()
	tpl, ok := catalog.Database(engine)
	if !ok {
		e.t.Fatalf("no engine %q", engine)
	}
	sealed, _ := e.d.Box.SealString(testDBPassword)
	m, err := e.db.CreateDatabase(context.Background(), e.team, db.Database{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: name, Engine: engine, Image: tpl.Image,
		Username: tpl.DefaultUser, Password: sealed, DBName: tpl.DefaultDB, PublicPort: publicPort,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

// startDB queues a start and waits for the database to settle.
func (e *env) startDB(m db.Database) db.Database {
	e.t.Helper()
	ctx := context.Background()
	if err := e.d.EnqueueDatabase(ctx, m); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.db.DatabaseByID(ctx, m.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		if got.Status != db.AppDeploying {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("the database did not settle")
	return m
}

func TestStartDatabase(t *testing.T) {
	e := newEnv(t)
	m := e.newDatabase("postgres", "maindb", 0)
	got := e.startDB(m)
	container := DatabaseContainer(m.ID)
	if got.Status != db.AppRunning || got.Container != container || got.LastError != "" {
		t.Fatalf("after start: %+v", got)
	}

	calls := e.fake.Calls()
	order := []string{
		"docker pull postgres:17-alpine", "docker network inspect",
		"docker stop --time 60 " + container, "docker rm --force " + container,
		"docker run", "docker exec " + container + " pg_isready -U postgres -d postgres",
	}
	last := -1
	for _, want := range order {
		i := indexOf(calls, want)
		if i <= last {
			t.Fatalf("%q is missing or out of order in:\n%s", want, strings.Join(calls, "\n"))
		}
		last = i
	}
	run := calls[indexOf(calls, "docker run")]
	envPath := filepath.Join(e.cfg.AppDir(m.ID), "env")
	for _, want := range []string{
		"--name " + container,
		"--network " + NetworkName(m.EnvironmentID),
		"--network-alias maindb",
		"--env-file " + envPath,
		"--mount type=volume,source=" + DatabaseVolume(m.ID) + ",target=/var/lib/postgresql/data",
		"--label musdash.kind=database",
		"--label musdash.resource=" + m.ID,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run is missing %q:\n%s", want, run)
		}
	}
	// Not reachable from outside its environment unless asked.
	if strings.Contains(run, "--publish") {
		t.Errorf("a database was published without the public port being on:\n%s", run)
	}
	// The password is in the private env file and nowhere on a command line.
	if strings.Contains(strings.Join(calls, "\n"), testDBPassword) {
		t.Fatal("the database password appeared on a command line")
	}
	body, mode, ok := e.fake.File(envPath)
	if !ok || mode != 0o600 || !strings.Contains(body, "POSTGRES_PASSWORD="+testDBPassword+"\n") || !strings.Contains(body, "POSTGRES_USER=postgres\n") {
		t.Fatalf("env file: mode %o\n%s", mode, body)
	}
}

func TestDatabasePublicPortAndCommand(t *testing.T) {
	e := newEnv(t)
	m := e.newDatabase("redis", "cache", 30007)
	if got := e.startDB(m); got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	calls := e.fake.Calls()
	run := calls[indexOf(calls, "docker run")]
	if !strings.Contains(run, "--publish 0.0.0.0:30007:6379") {
		t.Errorf("public port not published:\n%s", run)
	}
	// The server reads its password from its own environment.
	if !strings.HasSuffix(run, ` redis:7-alpine sh -c exec redis-server --appendonly yes --requirepass "$REDIS_PASSWORD"`) {
		t.Errorf("command:\n%s", run)
	}
	if strings.Contains(strings.Join(calls, "\n"), testDBPassword) {
		t.Fatal("the password appeared on a command line")
	}
}

func TestDatabaseThatNeverAnswersFails(t *testing.T) {
	e := newEnv(t)
	e.d.dbStartTimeout = 150 * time.Millisecond
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker exec"):
			return "", runnertest.Exit("docker", 2, "no response")
		case strings.HasPrefix(line, "docker inspect"):
			return running, nil
		}
		return "", nil
	}
	got := e.startDB(e.newDatabase("postgres", "maindb", 0))
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "did not accept connections within") {
		t.Fatalf("%+v", got)
	}
}

func TestDatabaseThatExitsReportsItsOutput(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker inspect"):
			return `{"Status":"exited","Running":false,"ExitCode":1}`, nil
		case strings.HasPrefix(line, "docker logs"):
			return "FATAL: data directory has wrong ownership\n", nil
		}
		return "", nil
	}
	got := e.startDB(e.newDatabase("postgres", "maindb", 0))
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "exited with status 1") || !strings.Contains(got.LastError, "wrong ownership") {
		t.Fatalf("%+v", got)
	}
}

func TestDatabaseUsingTheImagesOwnHealthCheck(t *testing.T) {
	e := newEnv(t)
	health := "starting"
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker inspect") {
			state := `{"Status":"running","Running":true,"ExitCode":0,"Health":{"Status":"` + health + `"}}`
			health = "healthy" // ready on the second look
			return state, nil
		}
		return "", nil
	}
	m := e.newDatabase("dragonfly", "cache", 0)
	if got := e.startDB(m); got.Status != db.AppRunning {
		t.Fatalf("%+v", got)
	}
	if indexOf(e.fake.Calls(), "docker exec") >= 0 {
		t.Fatal("an engine without a health command was probed with docker exec")
	}
}

func TestDatabasePublicPortTaken(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker run") {
			return "", runnertest.Exit("docker", 125, "Bind for 0.0.0.0:30001 failed: port is already allocated")
		}
		return running, nil
	}
	got := e.startDB(e.newDatabase("postgres", "maindb", 30001))
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "public port 30001 is already in use") {
		t.Fatalf("%+v", got)
	}
}

func TestStopAndDestroyDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	keep := e.newDatabase("postgres", "keepdata", 0)
	drop := e.newDatabase("postgres", "dropdata", 0)
	e.startDB(keep)
	e.startDB(drop)

	if err := e.d.StopDatabase(ctx, keep.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.DatabaseByID(ctx, keep.ID); got.Status != db.AppStopped || got.Container != "" {
		t.Fatalf("after stop: %+v", got)
	}
	// A "die" event for a database stopped on purpose is not a crash.
	e.d.applyEvent(ctx, []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"database","musdash.resource":"`+keep.ID+`","name":"`+DatabaseContainer(keep.ID)+`"}}}`))
	if got, _ := e.db.DatabaseByID(ctx, keep.ID); got.Status != db.AppStopped {
		t.Fatalf("status %s", got.Status)
	}

	before := len(e.fake.Calls())
	if err := e.d.DestroyDatabase(ctx, keep.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.HasPrefix(c, "docker volume rm") {
			t.Fatal("the data volume was deleted without being asked")
		}
	}
	if _, err := e.db.DatabaseByID(ctx, keep.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("row still exists: %v", err)
	}
	if _, _, ok := e.fake.File(filepath.Join(e.cfg.AppDir(keep.ID), "env")); ok {
		t.Fatal("the env file with the password was left on the server")
	}

	if err := e.d.DestroyDatabase(ctx, drop.ID, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(e.fake.Calls(), "docker volume rm "+DatabaseVolume(drop.ID)) < 0 {
		t.Fatal("the data volume was not deleted although asked")
	}
}

func TestDatabaseEventsAndReconcile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.newDatabase("postgres", "maindb", 0)
	e.startDB(m)
	container := DatabaseContainer(m.ID)
	ev := func(action string) []byte {
		return []byte(`{"Action":"` + action + `","Actor":{"Attributes":{"musdash.kind":"database","musdash.resource":"` + m.ID + `","name":"` + container + `"}}}`)
	}
	e.d.applyEvent(ctx, ev("die"))
	if got, _ := e.db.DatabaseByID(ctx, m.ID); got.Status != db.AppExited {
		t.Fatalf("status %s after the container died", got.Status)
	}
	e.d.applyEvent(ctx, ev("start"))
	if got, _ := e.db.DatabaseByID(ctx, m.ID); got.Status != db.AppRunning {
		t.Fatalf("status %s after Docker restarted it", got.Status)
	}

	// Reconcile sees a database container that is not running, and leaves
	// the database's own container alone when looking for orphans.
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return container + "\texited\tdatabase\t" + m.ID + "\t\n", nil
		}
		return running, nil
	}
	r, _ := e.d.Runners.Runner(ctx, e.server)
	before := len(e.fake.Calls())
	if err := e.d.Reconcile(ctx, e.server, dockerClient(r)); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.DatabaseByID(ctx, m.ID); got.Status != db.AppExited {
		t.Fatalf("status %s after reconcile", got.Status)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.HasPrefix(c, "docker rm") {
			t.Fatalf("reconcile removed a database's container: %s", c)
		}
	}
}

func TestNamesAreSharedBetweenAppsAndDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// The test app is called "web".
	sealed, _ := e.d.Box.SealString("x")
	_, err := e.db.CreateDatabase(ctx, e.team, db.Database{EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "web", Engine: "postgres", Image: "postgres:17-alpine", Password: sealed})
	if !errors.Is(err, db.ErrNameTaken) {
		t.Fatalf("a database took an app's name: %v", err)
	}
	e.newDatabase("postgres", "maindb", 0)
	_, err = e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "maindb", Image: "nginx", Port: 80})
	if !errors.Is(err, db.ErrNameTaken) {
		t.Fatalf("an app took a database's name: %v", err)
	}
}
