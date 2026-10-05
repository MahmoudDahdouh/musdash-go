package deploy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
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
	return e.startDBWithin(m, 10*time.Second)
}

func (e *env) startDBWithin(m db.Database, limit time.Duration) db.Database {
	e.t.Helper()
	ctx := context.Background()
	if err := e.d.EnqueueDatabase(ctx, m, false); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(limit)
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
	// The image is on the server already, so the registry is not asked.
	if indexOf(calls, "docker pull") >= 0 {
		t.Fatal("an image that is already present was pulled again")
	}
	order := []string{
		"docker image inspect --format {{.Id}} postgres:17-alpine", "docker network inspect",
		"docker image inspect --format {{json .Config.Volumes}} postgres:17-alpine",
		"docker stop --time 60 " + container, "docker rm --force " + container,
		// Over TCP: the image's first-boot server listens on a socket only.
		"docker run", "docker exec " + container + " pg_isready -h 127.0.0.1 -U postgres -d postgres",
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

func TestDatabaseImageIsPulledWhenMissing(t *testing.T) {
	e := newEnv(t)
	pulled := false
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker image inspect") && (!pulled || strings.Contains(line, "{{.Id}}")):
			return "", runnertest.Exit("docker", 1, "Error response from daemon: No such image: postgres:17-alpine")
		case strings.HasPrefix(line, "docker pull"):
			if pulled {
				return "", runnertest.Exit("docker", 1, "toomanyrequests: You have reached your pull rate limit")
			}
			pulled = true
		case strings.HasPrefix(line, "docker inspect"):
			return running, nil
		}
		return "", nil
	}
	m := e.newDatabase("postgres", "maindb", 0)
	if got := e.startDB(m); got.Status != db.AppRunning || !pulled {
		t.Fatalf("pulled=%v %+v", pulled, got)
	}
	// A pull that fails says why.
	got := e.startDB(e.newDatabase("postgres", "second", 0))
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "pull postgres:17-alpine") || !strings.Contains(got.LastError, "pull rate limit") {
		t.Fatalf("%+v", got)
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
	// The server is given its password in a file its own shell writes, and
	// started through the image's entry point so it does not run as root.
	if !strings.Contains(run, ` redis:7-alpine sh -c umask 077 && printf 'requirepass %s`) || !strings.HasSuffix(run, `exec docker-entrypoint.sh redis-server /tmp/musdash.conf`) {
		t.Errorf("command:\n%s", run)
	}
	if strings.Contains(run, "--requirepass") {
		t.Errorf("the password would be in the server's own argument list:\n%s", run)
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
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker exec"):
			return "", runnertest.Exit("docker", 2, "no response")
		case strings.HasPrefix(line, "docker inspect"):
			return running, nil
		case strings.HasPrefix(line, "docker logs"):
			return "initdb: creating configuration files\n", nil
		}
		return "", nil
	}
	m := e.newDatabase("postgres", "maindb", 0)
	got := e.startDB(m)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "did not accept connections within") || !strings.Contains(got.LastError, "creating configuration files") {
		t.Fatalf("%+v", got)
	}
	// The container may still be working. It stays on record, so it can be
	// looked at and stopped, and it is not removed behind the person's back.
	container := DatabaseContainer(m.ID)
	if got.Container != container {
		t.Fatalf("the container of a failed start was forgotten: %q", got.Container)
	}
	calls := e.fake.Calls()
	if last := calls[len(calls)-1]; strings.HasPrefix(last, "docker rm") || strings.HasPrefix(last, "docker stop") {
		t.Fatalf("the container was removed after the wait timed out: %s", last)
	}
	if err := e.d.StopDatabase(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if indexOf(e.fake.Calls()[len(calls):], "docker rm --force "+container) < 0 {
		t.Fatal("Stop did not remove the container of a failed start")
	}
}

func TestDatabaseStartsOncePerRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.newDatabase("postgres", "maindb", 0)
	jobs := func() (n int) {
		e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'database'`).Scan(&n)
		return n
	}
	// Held back so the first start is still pending when the second comes.
	e.db.Exec(`INSERT INTO jobs (id, kind, status, lock_key, run_after, created_at) VALUES ('blocker', 'none', 'running', ?, 0, 0)`, "database:"+m.ID)
	if err := e.d.EnqueueDatabase(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.EnqueueDatabase(ctx, m, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second start while the first is pending: %v", err)
	}
	if jobs() != 1 {
		t.Fatalf("%d start jobs after two clicks, want 1", jobs())
	}
	// A change of settings asks for another start on purpose.
	if err := e.d.EnqueueDatabase(ctx, m, true); err != nil || jobs() != 2 {
		t.Fatalf("a forced restart: %v, %d jobs", err, jobs())
	}

	// A start that cannot be queued does not leave the database "starting".
	other := e.newDatabase("postgres", "other", 0)
	e.db.Exec(`CREATE TRIGGER refuse_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
	if err := e.d.EnqueueDatabase(ctx, other, false); err == nil {
		t.Fatal("no error although the job could not be stored")
	}
	if got, _ := e.db.DatabaseByID(ctx, other.ID); got.Status != db.AppFailed || !strings.Contains(got.LastError, "could not be queued") {
		t.Fatalf("after a failed enqueue: %+v", got)
	}
}

func TestDatabaseVolumeFollowsTheImageAndNeverMoves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	declared := `{"/var/lib/postgresql":{}}` // as PostgreSQL 18 declares it
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.Contains(line, "{{json .Config.Volumes}}"):
			return declared + "\n", nil
		case strings.HasPrefix(line, "docker inspect"):
			return running, nil
		}
		return "", nil
	}
	m := e.newDatabase("postgres", "maindb", 0)
	got := e.startDB(m)
	if got.Status != db.AppRunning || got.VolumePath != "/var/lib/postgresql" {
		t.Fatalf("%+v", got)
	}
	calls := e.fake.Calls()
	if run := calls[indexOf(calls, "docker run")]; !strings.Contains(run, "source="+DatabaseVolume(m.ID)+",target=/var/lib/postgresql ") {
		t.Fatalf("the volume is not mounted where the image keeps its data:\n%s", run)
	}

	// The image is changed to one that keeps its data elsewhere. Starting
	// it would show an empty database, so it is refused, and before the
	// running container is touched.
	declared = `{"/var/lib/postgresql/data":{}}`
	before := len(e.fake.Calls())
	got = e.startDB(got)
	if got.Status != db.AppFailed || !strings.Contains(got.LastError, "keeps its data in /var/lib/postgresql/data") {
		t.Fatalf("%+v", got)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.HasPrefix(c, "docker stop") || strings.HasPrefix(c, "docker rm") || strings.HasPrefix(c, "docker run") {
			t.Fatalf("the working database was touched: %s", c)
		}
	}
	if got.Container != DatabaseContainer(m.ID) || got.VolumePath != "/var/lib/postgresql" {
		t.Fatalf("after the refused start: %+v", got)
	}

	// An image that declares a path the template does not know (or none)
	// gets the template's default.
	declared = `{"/somewhere/else":{}}`
	other := e.startDB(e.newDatabase("postgres", "other", 0))
	if other.Status != db.AppRunning || other.VolumePath != "/var/lib/postgresql/data" {
		t.Fatalf("%+v", other)
	}
	_ = ctx
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
	e.d.applyEvent(ctx, docker.Client{R: e.fake}, []byte(`{"Action":"die","Actor":{"Attributes":{"musdash.kind":"database","musdash.resource":"`+keep.ID+`","name":"`+DatabaseContainer(keep.ID)+`"}}}`))
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
	e.d.applyEvent(ctx, docker.Client{R: e.fake}, ev("die"))
	if got, _ := e.db.DatabaseByID(ctx, m.ID); got.Status != db.AppExited {
		t.Fatalf("status %s after the container died", got.Status)
	}
	e.d.applyEvent(ctx, docker.Client{R: e.fake}, ev("start"))
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

func TestDatabasesStuckAfterACrashAreRepaired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	noContainer := e.newDatabase("postgres", "never-started", 0)
	withContainer := e.newDatabase("postgres", "half-started", 0)
	waiting := e.newDatabase("postgres", "still-queued", 0)
	fine := e.startDB(e.newDatabase("postgres", "fine", 0))
	// As a process that died mid-start would leave them.
	e.db.SetDatabaseState(ctx, noContainer.ID, db.AppDeploying, "", "")
	e.db.SetDatabaseState(ctx, withContainer.ID, db.AppDeploying, DatabaseContainer(withContainer.ID), "")
	e.db.SetDatabaseState(ctx, waiting.ID, db.AppDeploying, "", "")
	e.db.Exec(`INSERT INTO jobs (id, kind, status, payload, lock_key, run_after, created_at) VALUES ('later', 'database', 'queued', ?, 'x', ?, 0)`,
		`{"id":"`+waiting.ID+`"}`, time.Now().Add(time.Hour).Unix())

	if err := e.db.ResetStuckDatabases(ctx, "stopped while starting"); err != nil {
		t.Fatal(err)
	}
	status := func(id string) (string, string) {
		m, _ := e.db.DatabaseByID(ctx, id)
		return m.Status, m.LastError
	}
	if st, why := status(noContainer.ID); st != db.AppFailed || why != "stopped while starting" {
		t.Errorf("without a container: %s %q", st, why)
	}
	if st, _ := status(withContainer.ID); st != db.AppExited {
		t.Errorf("with a container: %s", st)
	}
	// A job still to run will move its database on by itself.
	if st, _ := status(waiting.ID); st != db.AppDeploying {
		t.Errorf("with a job queued: %s", st)
	}
	if st, _ := status(fine.ID); st != db.AppRunning {
		t.Errorf("a running database was touched: %s", st)
	}

	// The reconcile that follows at start-up finds the half-started one's
	// container running and says so.
	container := DatabaseContainer(withContainer.ID)
	e.fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps") {
			return container + "\trunning\tdatabase\t" + withContainer.ID + "\t\n", nil
		}
		return running, nil
	}
	r, _ := e.d.Runners.Runner(ctx, e.server)
	if err := e.d.Reconcile(ctx, e.server, dockerClient(r)); err != nil {
		t.Fatal(err)
	}
	if st, _ := status(withContainer.ID); st != db.AppRunning {
		t.Errorf("after reconcile: %s", st)
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

// TestDatabasesWithDocker runs PostgreSQL and Redis on the local Docker
// daemon: start, connect from another container by name, restart and find
// the data again, open a public port, delete with and without the data.
func TestDatabasesWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}
	e.d.healthEvery = 500 * time.Millisecond
	e.d.dbStartTimeout = 3 * time.Minute
	network := NetworkName(e.app.EnvironmentID)

	pg := e.newDatabase("postgres", "pg", 0)
	rd := e.newDatabase("redis", "cache", 0)
	t.Cleanup(func() {
		for _, id := range []string{pg.ID, rd.ID} {
			dk.Remove(ctx, DatabaseContainer(id))
			dk.RemoveVolume(ctx, DatabaseVolume(id))
		}
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", network}})
	})

	for _, m := range []*db.Database{&pg, &rd} {
		if *m = e.startDBWithin(*m, 5*time.Minute); m.Status != db.AppRunning {
			t.Fatalf("%s: %s %s", m.Name, m.Status, m.LastError)
		}
	}

	// client runs a one-off container on the environment's network, the way
	// an app would reach the database. Secrets go through its environment.
	client := func(image string, env []string, script string) (string, error) {
		args := []string{"run", "--rm", "--network", network}
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			args = append(args, "--env", name)
		}
		args = append(args, image, "sh", "-c", script)
		out, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: args, Env: env})
		return strings.TrimSpace(string(out)), err
	}
	tpl, _ := catalog.Database("postgres")
	pgURL := "DATABASE_URL=" + tpl.URL(catalog.Creds{User: pg.Username, Pass: testDBPassword, DB: pg.DBName}, pg.Name, tpl.Port)
	redisAuth := "REDISCLI_AUTH=" + testDBPassword

	// The internal connection string works from another container, by name.
	if out, err := client("postgres:17-alpine", []string{pgURL}, `psql "$DATABASE_URL" -Atc "create table t (v text); insert into t values ('kept'); select v from t"`); err != nil || !strings.HasSuffix(out, "kept") {
		t.Fatalf("postgres by name: %q %v", out, err)
	}
	if out, err := client("redis:7-alpine", []string{redisAuth}, `redis-cli -h cache set k kept && redis-cli -h cache get k`); err != nil || !strings.HasSuffix(out, "kept") {
		t.Fatalf("redis by name: %q %v", out, err)
	}
	// The generated password is really required.
	if out, _ := client("redis:7-alpine", nil, `redis-cli -h cache get k`); !strings.Contains(out, "NOAUTH") {
		t.Fatalf("redis answered without the password: %q", out)
	}
	if out, err := client("postgres:17-alpine", []string{"DATABASE_URL=" + strings.Replace(pgURL[len("DATABASE_URL="):], testDBPassword, "wrong", 1)}, `psql "$DATABASE_URL" -Atc "select 1" 2>&1`); err == nil {
		t.Fatalf("postgres accepted a wrong password: %q", out)
	}

	// Nothing is published on the server unless asked.
	for _, m := range []db.Database{pg, rd} {
		out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"port", DatabaseContainer(m.ID)}})
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("%s is published without a public port: %s", m.Name, out)
		}
	}

	// A restart replaces the container and finds the data again. Redis is
	// restarted with a public port switched on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rd.PublicPort = port
	if _, err := e.db.UpdateDatabaseSettings(ctx, e.team, rd); err != nil {
		t.Fatal(err)
	}
	rd, _ = e.db.DatabaseByID(ctx, rd.ID)
	for _, m := range []*db.Database{&pg, &rd} {
		if *m = e.startDBWithin(*m, 5*time.Minute); m.Status != db.AppRunning {
			t.Fatalf("restart %s: %s %s", m.Name, m.Status, m.LastError)
		}
	}
	if out, err := client("postgres:17-alpine", []string{pgURL}, `psql "$DATABASE_URL" -Atc "select v from t"`); err != nil || out != "kept" {
		t.Fatalf("postgres after a restart: %q %v", out, err)
	}

	// Through the public port, from outside Docker's network.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 10*time.Second)
	if err != nil {
		t.Fatalf("the public port does not accept connections: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write([]byte("AUTH " + testDBPassword + "\r\nGET k\r\n"))
	reply := bufio.NewReader(conn)
	var lines []string
	for range 3 {
		line, err := reply.ReadString('\n')
		if err != nil {
			t.Fatalf("reading from the public port: %v (so far %q)", err, lines)
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	if strings.Join(lines, " ") != "+OK $4 kept" {
		t.Fatalf("redis through the public port after a restart: %q", lines)
	}

	// The Redis server was started through the image's entry point, so it
	// does not run as root, and its password is in no argument list.
	top, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"top", DatabaseContainer(rd.ID), "-eo", "pid,user,args"}})
	if err != nil || !strings.Contains(string(top), "redis-server") {
		t.Fatalf("docker top: %v\n%s", err, top)
	}
	for _, line := range strings.Split(string(top), "\n") {
		if f := strings.Fields(line); strings.Contains(line, "redis-server") && len(f) > 1 && (f[1] == "root" || f[1] == "0") {
			t.Errorf("redis runs as root: %s", line)
		}
	}
	if strings.Contains(string(top), testDBPassword) {
		t.Errorf("the password is in a process's arguments:\n%s", top)
	}

	// PostgreSQL 18 keeps its data one directory up from where 17 did. The
	// volume goes where that image expects it, and the data survives.
	pg18 := e.newDatabase("postgres", "pg18", 0)
	pg18.Image = "postgres:18-alpine"
	if _, err := e.db.UpdateDatabaseSettings(ctx, e.team, pg18); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dk.Remove(ctx, DatabaseContainer(pg18.ID))
		dk.RemoveVolume(ctx, DatabaseVolume(pg18.ID))
	})
	pg18, _ = e.db.DatabaseByID(ctx, pg18.ID)
	if pg18 = e.startDBWithin(pg18, 5*time.Minute); pg18.Status != db.AppRunning || pg18.VolumePath != "/var/lib/postgresql" {
		t.Fatalf("postgres 18: %s %s (volume at %q)", pg18.Status, pg18.LastError, pg18.VolumePath)
	}
	pg18URL := "DATABASE_URL=" + tpl.URL(catalog.Creds{User: pg18.Username, Pass: testDBPassword, DB: pg18.DBName}, pg18.Name, tpl.Port)
	if out, err := client("postgres:18-alpine", []string{pg18URL}, `psql "$DATABASE_URL" -Atc "create table t (v text); insert into t values ('kept18')"`); err != nil {
		t.Fatalf("postgres 18 by name: %q %v", out, err)
	}
	if pg18 = e.startDBWithin(pg18, 5*time.Minute); pg18.Status != db.AppRunning {
		t.Fatalf("postgres 18 restart: %s %s", pg18.Status, pg18.LastError)
	}
	if out, err := client("postgres:18-alpine", []string{pg18URL}, `psql "$DATABASE_URL" -Atc "select v from t"`); err != nil || out != "kept18" {
		t.Fatalf("postgres 18 after a restart: %q %v", out, err)
	}
	// Pointing it at the image of another major version is refused, and
	// the running database is left alone.
	pg18.Image = "postgres:17-alpine"
	e.db.UpdateDatabaseSettings(ctx, e.team, pg18)
	pg18, _ = e.db.DatabaseByID(ctx, pg18.ID)
	if got := e.startDBWithin(pg18, time.Minute); got.Status != db.AppFailed || !strings.Contains(got.LastError, "keeps its data in /var/lib/postgresql/data") {
		t.Fatalf("a move to an image with another data path: %s %s", got.Status, got.LastError)
	}
	if out, err := client("postgres:18-alpine", []string{pg18URL}, `psql "$DATABASE_URL" -Atc "select v from t"`); err != nil || out != "kept18" {
		t.Fatalf("postgres 18 after the refused change: %q %v", out, err)
	}
	if err := e.d.DestroyDatabase(ctx, pg18.ID, true); err != nil {
		t.Fatal(err)
	}

	// Deleting keeps the data unless asked.
	volumeExists := func(id string) bool {
		return local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"volume", "inspect", DatabaseVolume(id)}}) == nil
	}
	if err := e.d.DestroyDatabase(ctx, pg.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.DestroyDatabase(ctx, rd.ID, true); err != nil {
		t.Fatal(err)
	}
	if !volumeExists(pg.ID) {
		t.Fatal("the PostgreSQL data was deleted without being asked")
	}
	if volumeExists(rd.ID) {
		t.Fatal("the Redis data is still there although its deletion was asked for")
	}
	for _, id := range []string{pg.ID, rd.ID} {
		if st, err := dk.State(ctx, DatabaseContainer(id)); !errors.Is(err, docker.ErrNoContainer) {
			t.Fatalf("container of a deleted database is still there: %+v %v", st, err)
		}
	}
	t.Logf("PostgreSQL and Redis: reached by name, data kept across a restart, public port %d, deleted", port)
}

// TestEveryEngineStartsWithDocker starts each engine of the catalogue in
// turn on the local Docker daemon and waits for its health check, which is
// what proves a template's image, environment, command and health command
// fit together. It pulls several gigabytes of images, so it has its own
// switch; images that were not there before are removed again.
func TestEveryEngineStartsWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST_ENGINES") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST_ENGINES=1 to start every database engine on the local Docker daemon")
	}
	e := newEnv(t)
	ctx := context.Background()
	local := runner.NewLocal()
	dk := docker.Client{R: local}
	e.d.Runners = fixedRunners{local}
	e.d.healthEvery = time.Second
	e.d.dbStartTimeout = 4 * time.Minute
	t.Cleanup(func() {
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", NetworkName(e.app.EnvironmentID)}})
	})

	// MUSDASH_ENGINE_IMAGES="mongodb=mongo:9,postgres=postgres:18-alpine"
	// tries other versions than the catalogue's defaults.
	images := map[string]string{}
	for _, pair := range strings.Split(os.Getenv("MUSDASH_ENGINE_IMAGES"), ",") {
		if engine, image, ok := strings.Cut(pair, "="); ok {
			images[engine] = image
		}
	}
	only := os.Getenv("MUSDASH_ENGINES") // "mongodb,redis" runs just those

	for _, tpl := range catalog.Databases() {
		if only != "" && !slices.Contains(strings.Split(only, ","), tpl.Engine) {
			continue
		}
		if image := images[tpl.Engine]; image != "" {
			tpl.Image = image
		}
		t.Run(tpl.Engine, func(t *testing.T) {
			had := local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"image", "inspect", "--format", "ok", tpl.Image}}) == nil
			m := e.newDatabase(tpl.Engine, "db-"+tpl.Engine, 0)
			if m.Image != tpl.Image {
				m.Image = tpl.Image
				if _, err := e.db.UpdateDatabaseSettings(ctx, e.team, m); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				dk.Remove(ctx, DatabaseContainer(m.ID))
				dk.RemoveVolume(ctx, DatabaseVolume(m.ID))
				if !had {
					dk.RemoveImage(ctx, tpl.Image)
				}
			})
			started := time.Now()
			got := e.startDBWithin(m, 12*time.Minute)
			if got.Status != db.AppRunning {
				t.Fatalf("%s %s", got.Status, got.LastError)
			}
			// The password is in no process's argument list, on the server
			// or inside the container.
			top, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"top", DatabaseContainer(m.ID), "-eo", "pid,user,args"}})
			if strings.Contains(string(top), testDBPassword) {
				t.Errorf("the password is in a process's arguments:\n%s", top)
			}
			// A second start is a restart onto the existing data.
			if got = e.startDBWithin(got, 6*time.Minute); got.Status != db.AppRunning {
				t.Fatalf("restart: %s %s", got.Status, got.LastError)
			}
			if err := e.d.DestroyDatabase(ctx, m.ID, true); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s (%s) started, restarted and was deleted in %s", tpl.Label, tpl.Image, time.Since(started).Round(time.Second))
		})
	}
}
