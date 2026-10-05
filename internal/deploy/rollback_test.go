package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

// since returns the commands run after the first n.
func (e *env) since(n int) []string { return e.fake.Calls()[n:] }

func hasPrefix(calls []string, prefix string) bool { return indexOf(calls, prefix) >= 0 }

func TestEveryDeploymentKeepsItsImageUnderTheAppsOwnName(t *testing.T) {
	e := newEnv(t)
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	kept := ImageRepository(e.app.ID) + ":d-" + dep.ID
	if dep.KeptImage != kept {
		t.Fatalf("kept image %q, want %q", dep.KeptImage, kept)
	}
	calls := e.fake.Calls()
	pull, tag, run := indexOf(calls, "docker pull nginx:alpine"), indexOf(calls, "docker tag nginx:alpine "+kept), indexOf(calls, "docker run")
	if pull < 0 || tag < pull || run < tag {
		t.Fatalf("pull, tag and run at %d, %d, %d:\n%s", pull, tag, run, strings.Join(calls, "\n"))
	}
	// The container runs under the name a person gave, not the kept one.
	if app := e.reload(); app.DeployedImage != "nginx:alpine" {
		t.Fatalf("deployed image %q", app.DeployedImage)
	}
}

func TestRollbackRunsTheKeptImageAndPullsNothing(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	// The tag has moved on since: what the app pulls now is another image.
	e.db.Exec(`UPDATE apps SET image = 'nginx:1.99' WHERE id = ?`, e.app.ID)
	second := e.deploy()
	if first.Status != db.DeploySuccess || second.Status != db.DeploySuccess {
		t.Fatalf("%s, %s", first.Status, second.Status)
	}
	serving := e.reload().Container

	before := len(e.fake.Calls())
	back := e.rollback(first)
	if back.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", back.Status, back.Error, e.log(back))
	}
	calls := e.since(before)
	for _, never := range []string{"docker pull", "docker build", "git ", "docker tag"} {
		if hasPrefix(calls, never) {
			t.Errorf("a rollback ran %q:\n%s", never, strings.Join(calls, "\n"))
		}
	}
	run := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "docker run") {
			run = c
		}
	}
	if !strings.Contains(run, " "+first.KeptImage) || strings.Contains(run, "nginx:1.99") {
		t.Fatalf("the rollback did not run the kept image:\n%s", run)
	}
	if back.Trigger != TriggerRollback || back.RollbackOf != first.ID || back.KeptImage != first.KeptImage || back.Image != "nginx:alpine" {
		t.Fatalf("recorded: %+v", back)
	}
	// It went through the same switch: a new container serves, the one
	// from before is stopped, and the app's settings were not touched.
	app := e.reload()
	if app.Container != ContainerName(app.ID, back.ID) || app.Container == serving || app.Status != db.AppRunning {
		t.Fatalf("after the rollback: %+v", app)
	}
	if app.Image != "nginx:1.99" || app.DeployedImage != first.KeptImage {
		t.Fatalf("image setting %q, running %q", app.Image, app.DeployedImage)
	}
	if !hasPrefix(calls, "docker stop") && !hasPrefix(calls, "docker rm") {
		t.Errorf("the container from before was left running:\n%s", strings.Join(calls, "\n"))
	}
	// The image it went back to is not removed as an old one.
	for _, c := range calls {
		if c == "docker rmi "+first.KeptImage {
			t.Fatal("the image just rolled back to was removed")
		}
	}

	// A rollback can itself be rolled back to: it is the same image.
	again := e.rollback(back)
	if again.Status != db.DeploySuccess || again.KeptImage != first.KeptImage || again.RollbackOf != back.ID {
		t.Fatalf("a rollback of a rollback: %+v", again)
	}
}

func TestRollbackFailsWhenTheImageIsGone(t *testing.T) {
	e := newEnv(t)
	first := e.deploy()
	e.deploy()
	serving := e.reload()
	inner := e.fake.Handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker image inspect") {
			return "", runnertest.Exit("docker", 1, "No such image")
		}
		return inner(line, c)
	}
	before := len(e.fake.Calls())
	back := e.rollback(first)
	if back.Status != db.DeployFailed || !strings.Contains(back.Error, "no longer on the server") {
		t.Fatalf("%s %q", back.Status, back.Error)
	}
	calls := e.since(before)
	for _, never := range []string{"docker run", "docker pull", "docker build"} {
		if hasPrefix(calls, never) {
			t.Errorf("after the image was found missing, %q ran", never)
		}
	}
	if app := e.reload(); app.Container != serving.Container || app.Status != db.AppRunning {
		t.Fatalf("the app was disturbed: %+v", app)
	}
}

// The image a rollback runs is named by a row. It must be an image of this
// app: any other name would make the row a way to run something else.
func TestRollbackOnlyToTheAppsOwnSuccessfulDeployments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	good := e.deploy()
	app := e.reload()

	other, err := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: app.EnvironmentID, ServerID: e.server.ID, Name: "other", Image: "redis:7", Port: 6379})
	if err != nil {
		t.Fatal(err)
	}
	row := func(mutate func(*db.Deployment)) db.Deployment {
		d := good
		mutate(&d)
		return d
	}
	refused := map[string]db.Deployment{
		"another app's deployment":   row(func(d *db.Deployment) { d.AppID = other.ID }),
		"a failed deployment":        row(func(d *db.Deployment) { d.Status = db.DeployFailed }),
		"one still running":          row(func(d *db.Deployment) { d.Status = db.DeployRunning }),
		"one with no kept image":     row(func(d *db.Deployment) { d.KeptImage = "" }),
		"another app's image":        row(func(d *db.Deployment) { d.KeptImage = keptName(other.ID, d.ID) }),
		"an image from a registry":   row(func(d *db.Deployment) { d.KeptImage = "evil.example.com/x:latest" }),
		"a name that only starts so": row(func(d *db.Deployment) { d.KeptImage = ImageRepository(app.ID) + "x:d-1" }),
		"the repository with no tag": row(func(d *db.Deployment) { d.KeptImage = ImageRepository(app.ID) + ":" }),
		"a name with an option":      row(func(d *db.Deployment) { d.KeptImage = ImageRepository(app.ID) + ":d-1 --privileged" }),
	}
	before := len(e.fake.Calls())
	for name, to := range refused {
		if _, err := e.d.Rollback(ctx, app, to); !errors.Is(err, ErrNoRollback) {
			t.Errorf("%s: %v, want ErrNoRollback", name, err)
		}
	}
	// A deployment of this app loaded for another app is the same refusal.
	if _, err := e.d.Rollback(ctx, other, good); !errors.Is(err, ErrNoRollback) {
		t.Errorf("another app rolling back to this one's deployment: %v", err)
	}
	if n := len(e.since(before)); n != 0 {
		t.Fatalf("refused rollbacks ran %d commands", n)
	}

	// The check is made again when the deployment runs: a row changed
	// while it waited does not decide what is run.
	inner := e.fake.Handle
	hold := make(chan struct{})
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) { <-hold; return inner(line, c) }
	blocker, err := e.d.Enqueue(ctx, app, "manual")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := e.d.Rollback(ctx, app, good)
	if err != nil {
		t.Fatal(err)
	}
	e.db.Exec(`UPDATE deployments SET kept_image = 'evil.example.com/x:latest' WHERE id = ?`, queued.ID)
	close(hold)
	e.wait(blocker, 15e9)
	before = len(e.fake.Calls())
	done := e.wait(queued, 15e9)
	if done.Status != db.DeployFailed {
		t.Fatalf("a rollback to a foreign image: %s", done.Status)
	}
	for _, c := range e.since(before) {
		if strings.Contains(c, "evil.example.com") {
			t.Fatalf("the foreign image reached Docker: %s", c)
		}
	}
}

// A Git app's rollback builds nothing, and waits its turn with the app's
// other deployments.
func TestRollbackOfAGitApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.fake.Handle = (&gitEnvRecorder{}).handle
	e.gitApp(nil)
	built := e.deploy()
	if built.Status != db.DeploySuccess || built.KeptImage != ImageRepository(e.app.ID)+":"+testCommit[:12] {
		t.Fatalf("%s %q kept %q", built.Status, built.Error, built.KeptImage)
	}
	before := len(e.fake.Calls())
	back := e.rollback(built)
	if back.Status != db.DeploySuccess || back.CommitSHA != testCommit {
		t.Fatalf("%s %q commit %q", back.Status, back.Error, back.CommitSHA)
	}
	for _, never := range []string{"git ", "docker build", "docker pull"} {
		if hasPrefix(e.since(before), never) {
			t.Errorf("a rollback of a Git app ran %q", never)
		}
	}
	var lock string
	e.db.QueryRowContext(ctx, `SELECT lock_key FROM jobs WHERE kind = ? ORDER BY rowid DESC LIMIT 1`, JobDeploy).Scan(&lock)
	if lock != "build:"+e.server.ID {
		t.Fatalf("lock key %q: a rollback could overlap the app's builds", lock)
	}
}
