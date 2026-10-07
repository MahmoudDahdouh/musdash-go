package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

// networkRemovals counts how often the environment's network was removed.
func (e *env) networkRemovals() int {
	n := 0
	for _, c := range e.fake.Calls() {
		if c == "docker network rm "+NetworkName(e.app.EnvironmentID) {
			n++
		}
	}
	return n
}

// An environment's network is made by the first thing that starts in it and
// was never removed: a deleted project left it on the server. It goes with
// the last app, database or service of the environment on that server, and
// not before.
func TestLastResourceOfAnEnvironmentTakesItsNetwork(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.deploy()
	pg := e.newDatabase("postgres", "pg", 0)
	svc, _ := e.scriptedService(true)

	// A database and a service are still in the environment.
	if err := e.d.Destroy(ctx, e.app.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 0 {
		t.Fatalf("the network was removed %d times with a database and a service left in the environment", n)
	}
	// A service is; that it is stopped, or never ran, does not matter.
	if err := e.d.DestroyDatabase(ctx, pg.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 0 {
		t.Fatalf("the network was removed %d times with a service left in the environment", n)
	}
	// Nothing is.
	if err := e.d.DestroyService(ctx, svc.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 1 {
		t.Fatalf("the network was removed %d times after the environment's last resource was deleted, want once", n)
	}
}

func TestNetworkGoesWhenTheRestOfTheEnvironmentIsOnAnotherServer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.deploy()
	// Its sister lives on another server: this server's network has no
	// more use, the other's is not this delete's business.
	e.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', ?, 'elsewhere', 'ssh', 1)`, e.team)
	if _, err := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: e.app.EnvironmentID, ServerID: "othersrv", Name: "api", Image: "nginx", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Destroy(ctx, e.app.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 1 {
		t.Fatalf("the network was removed %d times, want once", n)
	}
}

func TestNetworkStaysWhileAPreviewIsInTheEnvironment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.gitApp(nil)
	// A preview is an app of the environment. Deleting it leaves its
	// parent, which needs the network; deleting the parent takes both.
	preview, err := e.db.CreatePreview(ctx, e.reload(), 7, "web-pr-7", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.d.Destroy(ctx, preview.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 0 {
		t.Fatalf("the network was removed %d times with the preview's own app left", n)
	}
	if err := e.d.Destroy(ctx, e.app.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.networkRemovals(); n != 1 {
		t.Fatalf("the network was removed %d times, want once", n)
	}
}

// What goes wrong with the network must not undo or fail a delete that has
// been done: the record is gone by then.
func TestDeleteSucceedsWhenTheNetworkCannotBeRemoved(t *testing.T) {
	for name, stderr := range map[string]string{
		"already gone": "Error response from daemon: network musdash-x not found",
		"still in use": `Error response from daemon: error while removing network: network musdash-x has active endpoints (name:"other" id:"abc")`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.deploy()
			inner := e.fake.Handle
			e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
				if strings.HasPrefix(line, "docker network rm") {
					return "", runnertest.Exit("docker", 1, stderr)
				}
				return inner(line, c)
			}
			if err := e.d.Destroy(context.Background(), e.app.ID); err != nil {
				t.Fatalf("the delete failed: %v", err)
			}
			if _, err := e.db.AppByID(context.Background(), e.app.ID); err == nil {
				t.Fatal("the app is still there")
			}
		})
	}
}
