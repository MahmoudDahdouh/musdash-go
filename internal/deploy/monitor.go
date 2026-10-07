package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
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
// stoppedEvent is what people are told when a container ends on its own.
func stoppedEvent(what, link, exitCode string) notify.Event {
	e := notify.Event{Kind: notify.EventContainer, Title: what + " stopped unexpectedly", URL: link, At: time.Now()}
	if exitCode != "" {
		e.Body = "Its container ended with status " + exitCode + "."
	}
	e.Body = strings.TrimSpace(e.Body + " Docker starts it again by itself unless it keeps failing; its logs say why it stopped.")
	return e
}

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
func (d *Deployer) applyEvent(ctx context.Context, serverID string, dk docker.Client, line []byte) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	status := statusFor(ev.Action)
	attrs := ev.Actor.Attributes
	if status == "" {
		return
	}
	var err error
	var changed bool
	switch attrs[docker.LabelKind] {
	case db.KindApp:
		changed, err = d.DB.SetAppStatusIf(ctx, serverID, attrs[docker.LabelResource], attrs["name"], status)
		if changed && status == db.AppExited {
			// Not a stop or a deployment: those set a status this event
			// does not replace.
			if app, aerr := d.DB.AppByID(ctx, attrs[docker.LabelResource]); aerr == nil {
				d.tell(app.EnvironmentID, stoppedEvent("The app "+app.Name, "/apps/"+app.ID, attrs["exitCode"]))
			}
		}
	case db.KindDatabase:
		changed, err = d.DB.SetDatabaseStatusIf(ctx, serverID, attrs[docker.LabelResource], attrs["name"], status)
		if changed && status == db.AppExited {
			if m, merr := d.DB.DatabaseByID(ctx, attrs[docker.LabelResource]); merr == nil {
				d.tell(m.EnvironmentID, stoppedEvent("The database "+m.Name, "/databases/"+m.ID, attrs["exitCode"]))
			}
		}
	case db.KindService:
		// A stack's status depends on all of its containers, so they are
		// looked at together rather than taken from this one event.
		err = d.refreshService(ctx, serverID, dk, attrs[docker.LabelResource])
	}
	if err != nil {
		d.Log.Error("record container event", "err", err)
	}
}

// Monitor keeps app statuses in step with Docker on one server. It holds a
// single `docker events` stream rather than polling or keeping a goroutine
// per container, reconnecting with back-off when the stream ends. It
// returns when ctx is cancelled.
//
// It also returns when the server was removed, or is one that has not been
// checked yet: whoever started it starts another when that changes.
func (d *Deployer) Monitor(ctx context.Context, server db.Server) {
	backoff := time.Second
	for ctx.Err() == nil {
		current, err := d.DB.ServerByID(ctx, server.ID)
		if errors.Is(err, db.ErrNotFound) || (err == nil && !servers.IsLocal(current) && current.HostKey == "") {
			return
		}
		if err == nil {
			server = current
		}
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
			d.applyEvent(ctx, server.ID, dk, sc.Bytes())
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
		if _, err := d.DB.SetAppStatusIf(ctx, server.ID, app.ID, app.Container, status); err != nil {
			return err
		}
	}
	databases, err := d.DB.DatabasesOnServer(ctx, server.ID)
	if err != nil {
		return err
	}
	for _, m := range databases {
		if m.Container == "" {
			continue
		}
		status := db.AppExited
		if state[m.Container] == "running" {
			status = db.AppRunning
		}
		if _, err := d.DB.SetDatabaseStatusIf(ctx, server.ID, m.ID, m.Container, status); err != nil {
			return err
		}
	}
	services, err := d.DB.ServicesOnServer(ctx, server.ID)
	if err != nil {
		return err
	}
	for _, s := range services {
		if err := d.DB.SetServiceStatusIf(ctx, server.ID, s.ID, serviceStatus(listed, s.ID)); err != nil {
			return err
		}
	}
	return d.removeOrphans(ctx, dk, listed, current)
}

// orphanGrace keeps the cleanup away from the container of a deployment
// that only just succeeded. A container carries the id of the deployment
// that made it, so this is the container a deployment made a moment ago
// and the next one has already replaced: it may still be draining.
const orphanGrace = 5 * time.Minute

// removeOrphans deletes app containers that nothing refers to: left by a
// process that died mid-deploy, or by a stop that could not finish. They
// would otherwise hold memory and a port for ever. Containers of
// deployments that are queued, running or just succeeded are left alone;
// the container of a failed deployment is not, and that is what removes
// the one a process killed in mid-deployment left waiting for its health
// check (the deployment is failed at start, before this runs).
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
