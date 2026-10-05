package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

func TestDeployTag(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other, err := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "api", Image: "nginx:alpine", Port: 80, HealthTimeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	untagged, _ := e.db.CreateApp(ctx, e.team, db.App{EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "worker", Image: "nginx:alpine", Port: 80, HealthTimeout: 1})
	for _, id := range []string{e.app.ID, other.ID} {
		if err := e.db.SetTags(ctx, e.team, db.KindApp, id, []string{"nightly"}); err != nil {
			t.Fatal(err)
		}
	}

	// A tag nothing has, and another team asking for this one.
	if queued, tagged, err := e.d.DeployTag(ctx, e.team, "nosuchtag", "tag"); err != nil || queued != 0 || tagged != 0 {
		t.Fatalf("unknown tag: %d %d %v", queued, tagged, err)
	}
	if queued, tagged, err := e.d.DeployTag(ctx, "otherteam", "nightly", "tag"); err != nil || queued != 0 || tagged != 0 {
		t.Fatalf("another team: %d %d %v", queued, tagged, err)
	}

	// Hold every pull, so deployments stay where the test can count them.
	release := make(chan struct{})
	base := e.fake.Handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker pull") {
			<-release
		}
		return base(line, c)
	}

	queued, tagged, err := e.d.DeployTag(ctx, e.team, "nightly", "tag")
	if err != nil || queued != 2 || tagged != 2 {
		t.Fatalf("first: %d of %d, %v", queued, tagged, err)
	}
	count := func(appID string) (n int) {
		e.db.QueryRow(`SELECT count(*) FROM deployments WHERE app_id = ?`, appID).Scan(&n)
		return n
	}
	// Both are running now (held at the pull). A second call queues one
	// more behind each, and a third finds those waiting and queues none.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var running int
		e.db.QueryRow(`SELECT count(*) FROM deployments WHERE status = 'running'`).Scan(&running)
		if running == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queued, _, _ := e.d.DeployTag(ctx, e.team, "nightly", "tag"); queued != 2 {
		t.Fatalf("behind running deployments: %d queued, want 2", queued)
	}
	if queued, tagged, _ := e.d.DeployTag(ctx, e.team, "nightly", "tag"); queued != 0 || tagged != 2 {
		t.Fatalf("behind waiting deployments: %d queued of %d, want 0 of 2", queued, tagged)
	}
	if count(e.app.ID) != 2 || count(other.ID) != 2 || count(untagged.ID) != 0 {
		t.Fatalf("deployments: %d %d %d", count(e.app.ID), count(other.ID), count(untagged.ID))
	}
	var trigger string
	e.db.QueryRow(`SELECT trigger FROM deployments WHERE app_id = ? LIMIT 1`, other.ID).Scan(&trigger)
	if trigger != "tag" {
		t.Fatalf("trigger %q", trigger)
	}
	// Let them finish, so the queue stops at once when the test ends.
	close(release)
	for _, id := range []string{e.app.ID, other.ID} {
		if dep := e.last(id); dep.Status != db.DeploySuccess {
			t.Fatalf("%s %q", dep.Status, dep.Error)
		}
	}
}

func TestDeployTagIncludesServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, _ := e.scriptedService(false)
	if err := e.db.SetTags(ctx, e.team, db.KindService, s.ID, []string{"nightly"}); err != nil {
		t.Fatal(err)
	}
	queued, tagged, err := e.d.DeployTag(ctx, e.team, "nightly", "tag")
	if err != nil || queued != 1 || tagged != 1 {
		t.Fatalf("%d of %d, %v", queued, tagged, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := e.db.ServiceByID(ctx, s.ID); got.Status == db.AppRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the service was not deployed:\n%s", e.serviceLog(s))
}
