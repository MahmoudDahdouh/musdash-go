package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// JobDatabase is the job kind that starts or restarts a database.
const JobDatabase = "database"

// A database has one container, replaced in place. Its data directory can
// be opened by one server process only, so two versions never run together.

// DatabaseContainer is the name of a database's container.
func DatabaseContainer(id string) string { return "musdash-db-" + id }

// DatabaseVolume is the Docker volume that holds a database's data.
func DatabaseVolume(id string) string { return "musdash-db-" + id + "-data" }

type databasePayload struct {
	ID string `json:"id"`
}

// EnqueueDatabase queues a start (or restart) of a database. The status
// changes at once so the page shows that work is under way.
//
// A database that is already starting answers ErrBusy, so two clicks queue
// one start. again queues a further start regardless, for a change of
// settings that the start in progress may have been too early to see.
func (d *Deployer) EnqueueDatabase(ctx context.Context, m db.Database, again bool) error {
	began, err := d.DB.BeginDatabaseStart(ctx, m.ID)
	if err != nil {
		return err
	}
	if !began && !again {
		return ErrBusy
	}
	_, err = d.Queue.Enqueue(ctx, JobDatabase, databasePayload{ID: m.ID}, jobs.WithLockKey("database:"+m.ID), jobs.WithMaxAttempts(1))
	if err != nil && began {
		// Nothing will run, so nothing would ever move it on from
		// "starting". Recorded even if the request has gone away.
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if serr := d.DB.SetDatabaseState(rec, m.ID, db.AppFailed, m.Container, "the start could not be queued: "+err.Error()); serr != nil {
			d.Log.Error("record database failure", "database", m.ID, "err", serr)
		}
	}
	return err
}

func (d *Deployer) runDatabaseJob(ctx context.Context, raw []byte) error {
	var p databasePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	mu := d.lockFor(p.ID)
	mu.Lock()
	defer mu.Unlock()
	m, err := d.DB.DatabaseByID(ctx, p.ID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("database %s: %w", p.ID, err))
	}
	err = d.startDatabase(ctx, m)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		// Shutdown interrupted the start; the job runs again afterwards.
		return err
	}
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	// The container, if one was started, stays on record: it may still be
	// running, and Stop, Logs and the monitor find it by that name.
	container := ""
	if cur, cerr := d.DB.DatabaseByID(rec, m.ID); cerr == nil {
		container = cur.Container
	}
	if serr := d.DB.SetDatabaseState(rec, m.ID, db.AppFailed, container, err.Error()); serr != nil {
		d.Log.Error("record database failure", "database", m.ID, "err", serr)
	}
	return jobs.Permanent(err)
}

// databaseCreds opens a database's credentials.
func (d *Deployer) databaseCreds(m db.Database) (catalog.Creds, error) {
	pass, err := d.Box.OpenString(m.Password)
	if err != nil {
		return catalog.Creds{}, errors.New("the database's password cannot be decrypted: was the master key changed?")
	}
	return catalog.Creds{User: m.Username, Pass: pass, DB: m.DBName}, nil
}

// startDatabase (re)creates a database's container and waits until the
// engine accepts connections. The caller holds the database's lock.
func (d *Deployer) startDatabase(ctx context.Context, m db.Database) error {
	tpl, ok := catalog.Database(m.Engine)
	if !ok {
		return fmt.Errorf("unknown database engine %q", m.Engine)
	}
	creds, err := d.databaseCreds(m)
	if err != nil {
		return err
	}
	server, err := d.DB.ServerByID(ctx, m.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	container := DatabaseContainer(m.ID)

	// An image already on the server is used as it is. A database then
	// starts without the registry being reachable, and a restart never
	// moves it to a newer build of the same tag behind the person's back;
	// changing the tag is how a version is changed.
	have, err := dk.HasImage(ctx, m.Image)
	if err != nil {
		return err
	}
	if !have {
		// Progress is not shown anywhere, but a failed pull's last lines
		// are what explains the failure.
		pullOut := &tail{limit: 2048}
		if err := dk.Pull(ctx, m.Image, pullOut); err != nil {
			return fmt.Errorf("pull %s: %w %s", m.Image, err, pullOut.String())
		}
	}
	if err := dk.EnsureNetwork(ctx, NetworkName(m.EnvironmentID)); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	// The password travels in a private file, never on the command line.
	env := tpl.RenderEnv(creds)
	vars := make([]db.EnvVar, 0, len(env))
	for k, v := range env {
		vars = append(vars, db.EnvVar{Key: k, Value: v})
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].Key < vars[j].Key })
	body, err := EnvFile(vars)
	if err != nil {
		return err
	}
	dir := d.at(r).AppDir(m.ID)
	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		return err
	}
	envPath := filepath.Join(dir, "env")
	if err := r.WriteFile(ctx, envPath, 0o600, strings.NewReader(body)); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}

	// Decided before the running database is touched: an image that keeps
	// its data somewhere else must not replace one that is working.
	volumePath, err := d.databaseVolumePath(ctx, dk, tpl, m)
	if err != nil {
		return err
	}

	// The old container must be gone before the new one opens the data.
	if err := dk.Stop(ctx, container, d.dbStopGrace); err != nil {
		return fmt.Errorf("stop the running database: %w", err)
	}
	if err := dk.Remove(ctx, container); err != nil {
		return err
	}
	if err := d.DB.SetDatabaseState(ctx, m.ID, db.AppDeploying, "", ""); err != nil {
		return err
	}
	if err := d.DB.SetDatabaseVolumePath(ctx, m.ID, volumePath); err != nil {
		return err
	}

	spec := docker.RunSpec{
		Name: container, Image: m.Image, Network: NetworkName(m.EnvironmentID), Alias: m.Name,
		ContainerPort: tpl.Port, PublicPort: m.PublicPort,
		EnvFile: envPath, MemoryMB: m.MemoryMB, CPUs: m.CPUs,
		Mounts:  []docker.Mount{{Kind: docker.MountVolume, Source: DatabaseVolume(m.ID), Target: volumePath}},
		Command: tpl.Command,
		Labels: map[string]string{
			docker.ManagedLabel:  "true",
			docker.LabelKind:     db.KindDatabase,
			docker.LabelResource: m.ID,
		},
	}
	err = dk.Run(ctx, spec)
	if errors.Is(err, docker.ErrPortTaken) {
		dk.Remove(ctx, container)
		return fmt.Errorf("public port %d is already in use on the server; choose another", m.PublicPort)
	}
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	// The container is recorded before the wait, so the monitor and a stop
	// both know about it even if the wait fails.
	if err := d.DB.SetDatabaseState(ctx, m.ID, db.AppDeploying, container, ""); err != nil {
		return err
	}
	if err := d.waitDatabase(ctx, dk, container, tpl.RenderHealth(creds)); err != nil {
		return err
	}
	return d.DB.SetDatabaseState(ctx, m.ID, db.AppRunning, container, "")
}

// databaseVolumePath decides where the data volume is mounted in the
// container: the path the image itself declares as its volume, when that is
// one the engine's template knows, otherwise the template's default.
//
// Once a database has started, the path is fixed. An image that expects
// its data elsewhere (PostgreSQL 18 moved it) would start on an empty
// directory and look like a database that lost everything, so it is
// refused instead.
func (d *Deployer) databaseVolumePath(ctx context.Context, dk docker.Client, tpl catalog.DBTemplate, m db.Database) (string, error) {
	declared, err := dk.ImageVolumes(ctx, m.Image)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", m.Image, err)
	}
	path := tpl.VolumePath
	for _, known := range append([]string{tpl.VolumePath}, tpl.OtherVolumePaths...) {
		if slices.Contains(declared, known) {
			path = known
			break
		}
	}
	if m.VolumePath != "" && m.VolumePath != path {
		return "", fmt.Errorf("the image %s keeps its data in %s, but this database's data was created in %s by another version. "+
			"Moving between such versions needs a dump and a restore into a new database; set the image back to keep using this one", m.Image, path, m.VolumePath)
	}
	return path, nil
}

// waitDatabase polls until the engine answers its health command. Without a
// command it relies on the image's own health check.
func (d *Deployer) waitDatabase(ctx context.Context, dk docker.Client, container string, healthCmd []string) error {
	ctx, cancel := context.WithTimeout(ctx, d.dbStartTimeout)
	defer cancel()
	ticker := time.NewTicker(d.healthEvery)
	defer ticker.Stop()
	for {
		st, err := dk.State(ctx, container)
		switch {
		case err != nil && ctx.Err() == nil:
			return err
		case err == nil && st.Status != "created" && (!st.Running || st.Status == "restarting"):
			out := &tail{limit: 1500}
			logCtx, cancelLog := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			dk.Logs(logCtx, container, 30, false, out)
			cancelLog()
			return fmt.Errorf("the database exited with status %d while starting. Its last output: %s", st.ExitCode, out.String())
		case err == nil && len(healthCmd) > 0:
			if dk.Exec(ctx, container, healthCmd...) == nil {
				return nil
			}
		case err == nil && st.Health == "healthy":
			return nil
		}
		select {
		case <-ctx.Done():
			// The container is left as it is: a first start on a slow disk
			// may simply need longer, and stopping it now would interrupt
			// the engine while it creates its files.
			out := &tail{limit: 1500}
			logCtx, cancelLog := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			dk.Logs(logCtx, container, 30, false, out)
			cancelLog()
			return fmt.Errorf("the database did not accept connections within %s. It was left running; if it is still setting up, it may yet come up. Its last output: %s", d.dbStartTimeout, out.String())
		case <-ticker.C:
		}
	}
}

// StopDatabase stops a database and removes its container. The data stays
// in its volume.
func (d *Deployer) StopDatabase(ctx context.Context, id string) error {
	mu := d.lockFor(id)
	if !lockSoon(mu) {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.dbStopGrace+90*time.Second)
	defer cancel()
	m, err := d.DB.DatabaseByID(ctx, id)
	if err != nil {
		return err
	}
	_, err = d.stopDatabaseLocked(ctx, m)
	return err
}

func (d *Deployer) stopDatabaseLocked(ctx context.Context, m db.Database) (runner.Runner, error) {
	server, err := d.DB.ServerByID(ctx, m.ServerID)
	if err != nil {
		return nil, err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return nil, err
	}
	dk := docker.Client{R: r}
	container := DatabaseContainer(m.ID)
	// Marked stopped first, so the monitor does not report the exit this
	// causes as a crash. The container name is kept until it is gone.
	if err := d.DB.SetDatabaseState(ctx, m.ID, db.AppStopped, m.Container, ""); err != nil {
		return r, err
	}
	// A database needs longer than an app to flush and close its files.
	if err := dk.Stop(ctx, container, d.dbStopGrace); err != nil {
		return r, fmt.Errorf("stop the container: %w", err)
	}
	if err := dk.Remove(ctx, container); err != nil {
		return r, fmt.Errorf("remove the container: %w", err)
	}
	return r, d.DB.SetDatabaseState(ctx, m.ID, db.AppStopped, "", "")
}

// DestroyDatabase stops and deletes a database. Its data volume is removed
// only when deleteData is set: losing data needs its own explicit choice.
func (d *Deployer) DestroyDatabase(ctx context.Context, id string, deleteData bool) error {
	mu := d.lockFor(id)
	if !lockSoon(mu) {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.dbStopGrace+90*time.Second)
	defer cancel()
	m, err := d.DB.DatabaseByID(ctx, id)
	if err != nil {
		return err
	}
	r, err := d.stopDatabaseLocked(ctx, m)
	if err != nil {
		return err
	}
	if err := r.RemoveAll(ctx, d.at(r).AppDir(m.ID)); err != nil {
		return err
	}
	if deleteData {
		if err := (docker.Client{R: r}).RemoveVolume(ctx, DatabaseVolume(m.ID)); err != nil {
			return fmt.Errorf("delete the data volume: %w", err)
		}
		// Its backups on the server are its data too. Copies in a storage
		// bucket are not touched: they are there to outlive the server.
		if err := r.RemoveAll(ctx, d.at(r).DatabaseBackupDir(m.ID)); err != nil {
			return fmt.Errorf("delete the backups: %w", err)
		}
	}
	if err := d.DB.DeleteDatabase(ctx, m.ID); err != nil {
		return err
	}
	d.dropNetwork(ctx, r, m.EnvironmentID, m.ServerID)
	return nil
}

// ValidPublicPort reports whether a person may choose this public port for
// a database: unprivileged, and outside the range musdash hands to apps on
// the loopback interface, where it would collide with one of them.
func ValidPublicPort(p int) bool {
	return p >= 1024 && p <= 65535 && (p < portMin || p > portMax)
}

// tail keeps the last limit bytes written to it.
type tail struct {
	buf   []byte
	limit int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }
