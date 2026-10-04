package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
)

// event is the part of a `docker events` line the monitor reads.
type event struct {
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// statusFor maps a Docker event to an app status, or "" for events that do
// not change it.
func statusFor(action string) string {
	switch action {
	case "start":
		return db.AppRunning
	case "die":
		return db.AppExited
	}
	return ""
}

// applyEvent updates the app a container event belongs to. Only the app's
// serving container counts: events from a container that is being replaced
// or was stopped on purpose are ignored by SetAppStatusIf.
func (d *Deployer) applyEvent(ctx context.Context, line []byte) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	status := statusFor(ev.Action)
	attrs := ev.Actor.Attributes
	if status == "" || attrs[docker.LabelKind] != db.KindApp {
		return
	}
	if err := d.DB.SetAppStatusIf(ctx, attrs[docker.LabelResource], attrs["name"], status); err != nil {
		d.Log.Error("record container event", "err", err)
	}
}

// Monitor keeps app statuses in step with Docker on one server. It holds a
// single `docker events` stream rather than polling or keeping a goroutine
// per container, reconnecting with back-off when the stream ends. It
// returns when ctx is cancelled.
func (d *Deployer) Monitor(ctx context.Context, server db.Server) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		if err := d.monitorOnce(ctx, server); err != nil && ctx.Err() == nil {
			d.Log.Warn("container monitor stopped; will retry", "server", server.Name, "err", err)
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (d *Deployer) monitorOnce(ctx context.Context, server db.Server) error {
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	// Events that happened while nothing was listening are caught up first.
	if err := d.Reconcile(ctx, server, dk); err != nil {
		return err
	}

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 16<<10), 256<<10)
		for sc.Scan() {
			d.applyEvent(ctx, sc.Bytes())
		}
		// Keep draining so the docker process never blocks on a full pipe.
		io.Copy(io.Discard, pr)
	}()
	err = dk.Events(ctx, pw)
	pw.Close()
	<-done
	return err
}

// Reconcile brings the database and Docker back into agreement: each app's
// status is set from what Docker reports, and containers no app points at
// are removed.
func (d *Deployer) Reconcile(ctx context.Context, server db.Server, dk docker.Client) error {
	// Docker is listed before the database is read. A container that became
	// an app's serving container in between is then seen as current, never
	// as an orphan.
	listed, err := dk.List(ctx)
	if err != nil {
		return err
	}
	state := make(map[string]string, len(listed))
	for _, c := range listed {
		state[c.Name] = c.State
	}
	apps, err := d.DB.AppsOnServer(ctx, server.ID)
	if err != nil {
		return err
	}
	current := make(map[string]bool, len(apps))
	for _, app := range apps {
		if app.Container == "" {
			continue
		}
		current[app.Container] = true
		status := db.AppExited
		if state[app.Container] == "running" {
			status = db.AppRunning
		}
		if err := d.DB.SetAppStatusIf(ctx, app.ID, app.Container, status); err != nil {
			return err
		}
	}
	return d.removeOrphans(ctx, dk, listed, current)
}

// orphanGrace keeps the cleanup away from containers of deployments that
// only just finished: the previous container may still be draining.
const orphanGrace = 5 * time.Minute

// removeOrphans deletes app containers that nothing refers to: left by a
// process that died mid-deploy, or by a stop that could not finish. They
// would otherwise hold memory and a port for ever. Containers of
// deployments that are queued, running or just finished are left alone.
func (d *Deployer) removeOrphans(ctx context.Context, dk docker.Client, listed []docker.Listed, current map[string]bool) error {
	protected, err := d.DB.ProtectedDeployments(ctx, time.Now().Add(-orphanGrace).Unix())
	if err != nil {
		return err
	}
	for _, c := range listed {
		if c.Kind != db.KindApp || current[c.Name] || protected[c.Deployment] {
			continue
		}
		d.Log.Info("removing a container no app refers to", "container", c.Name)
		if err := dk.Remove(ctx, c.Name); err != nil {
			d.Log.Warn("remove orphaned container", "container", c.Name, "err", err)
		}
	}
	return nil
}
