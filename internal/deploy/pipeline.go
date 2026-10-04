// Package deploy turns an app's settings into a running container and keeps
// the proxy's routes in step with what is running.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// JobDeploy is the job kind of a deployment.
const JobDeploy = "deploy"

// Host ports handed to containers. The range sits below Linux's default
// ephemeral range (32768+) so outgoing connections do not collide with it.
const (
	portMin = 20000
	portMax = 29999
)

// Runners resolves the Runner for a server.
type Runners interface {
	Runner(ctx context.Context, s db.Server) (runner.Runner, error)
}

// Deployer runs deployments.
type Deployer struct {
	DB      *db.DB
	Box     *secret.Box
	Queue   *jobs.Queue
	Runners Runners
	Cfg     *config.Config
	Log     *slog.Logger
	Probe   Probe
	// InstanceTarget is the control plane's own loopback address, routed
	// when a dashboard domain is set.
	InstanceTarget string

	healthEvery time.Duration // how often a starting container is checked
	drain       time.Duration // pause between switching traffic and stopping the old container
	stopGrace   time.Duration // how long a container gets to exit after SIGTERM
}

// New returns a Deployer with production timings.
func New(d *db.DB, box *secret.Box, q *jobs.Queue, r Runners, cfg *config.Config, log *slog.Logger, instanceTarget string) *Deployer {
	return &Deployer{
		DB: d, Box: box, Queue: q, Runners: r, Cfg: cfg, Log: log,
		Probe:          newLocalProbe(),
		InstanceTarget: instanceTarget,
		healthEvery:    time.Second,
		drain:          3 * time.Second,
		stopGrace:      30 * time.Second,
	}
}

// Register installs the deploy job handler on the queue.
func (d *Deployer) Register() {
	d.Queue.Register(JobDeploy, d.runJob)
}

type payload struct {
	DeploymentID string `json:"deployment_id"`
}

func runnerCmd(name string, args ...string) runner.Cmd {
	return runner.Cmd{Name: name, Args: args}
}

// ContainerName is the name of the container one deployment starts.
func ContainerName(appID, deploymentID string) string {
	return "musdash-" + appID + "-" + deploymentID
}

// NetworkName is the Docker network shared by one environment's resources.
func NetworkName(environmentID string) string { return "musdash-" + environmentID }

// VolumeName namespaces a volume to its resource, so two apps that both ask
// for a volume called "data" do not share one.
func VolumeName(resourceID, name string) string { return "musdash-" + resourceID + "-" + name }

// Enqueue records a deployment of the app's current settings and queues it.
// Deployments of one app run one at a time, in order.
func (d *Deployer) Enqueue(ctx context.Context, app db.App, trigger string) (db.Deployment, error) {
	dep, err := d.DB.CreateDeployment(ctx, db.Deployment{AppID: app.ID, Trigger: trigger, Image: app.Image})
	if err != nil {
		return dep, err
	}
	// One attempt: a failed deploy is reported, not silently repeated.
	_, err = d.Queue.Enqueue(ctx, JobDeploy, payload{DeploymentID: dep.ID},
		jobs.WithLockKey("deploy:"+app.ID), jobs.WithMaxAttempts(1))
	if err != nil {
		d.DB.FinishDeployment(ctx, dep.ID, db.DeployFailed, "could not be queued: "+err.Error())
	}
	return dep, err
}

func (d *Deployer) runJob(ctx context.Context, raw []byte) error {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	dep, err := d.DB.DeploymentByID(ctx, p.DeploymentID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("deployment %s: %w", p.DeploymentID, err))
	}
	app, err := d.DB.AppByID(ctx, dep.AppID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("app %s: %w", dep.AppID, err))
	}

	log, err := OpenLog(d.Cfg.DeployLogPath(dep.ID))
	if err != nil {
		d.DB.FinishDeployment(ctx, dep.ID, db.DeployFailed, err.Error())
		return jobs.Permanent(err)
	}
	defer log.Close()

	if err := d.DB.StartDeployment(ctx, dep.ID); err != nil {
		return err
	}
	if err := d.DB.SetAppStatus(ctx, app.ID, db.AppDeploying); err != nil {
		return err
	}

	err = d.deploy(ctx, app, dep, log)

	// The result must be recorded even when shutdown cancelled the job.
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err == nil {
		log.Step("Deployed.")
		return d.DB.FinishDeployment(rec, dep.ID, db.DeploySuccess, "")
	}

	if ctx.Err() != nil {
		// Shutdown interrupted the deploy. The job goes back in the queue
		// and runs again from the start after the restart.
		log.Step("Interrupted: musdash is shutting down. The deployment will restart.")
		return err
	}
	log.Step("Failed: %v", err)
	status := statusAfterFailure(app)
	if serr := d.DB.SetAppStatus(rec, app.ID, status); serr != nil {
		d.Log.Error("restore app status", "app", app.ID, "err", serr)
	}
	if ferr := d.DB.FinishDeployment(rec, dep.ID, db.DeployFailed, err.Error()); ferr != nil {
		d.Log.Error("record deployment", "deployment", dep.ID, "err", ferr)
	}
	return jobs.Permanent(err)
}

// statusAfterFailure is what an app goes back to when a deployment fails.
// Whatever was serving before is still serving; an app that was stopped on
// purpose stays stopped; an app with nothing behind it has failed.
func statusAfterFailure(before db.App) string {
	switch {
	case before.Container != "" && before.Status == db.AppExited:
		return db.AppExited
	case before.Container != "":
		return db.AppRunning
	case before.Status == db.AppStopped:
		return db.AppStopped
	}
	return db.AppFailed
}

// deploy performs one deployment. On any error the new container is removed
// and the previous one keeps serving.
func (d *Deployer) deploy(ctx context.Context, app db.App, dep db.Deployment, log *Log) (err error) {
	server, err := d.DB.ServerByID(ctx, app.ServerID)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	image := dep.Image
	container := ContainerName(app.ID, dep.ID)

	log.Step("Pulling %s", image)
	if err := dk.Pull(ctx, image, log); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}

	network := NetworkName(app.EnvironmentID)
	if err := dk.EnsureNetwork(ctx, network); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	spec := docker.RunSpec{
		Name: container, Image: image, Network: network, Alias: app.Name,
		ContainerPort: app.Port, MemoryMB: app.MemoryMB, CPUs: app.CPUs,
		Labels: map[string]string{
			docker.ManagedLabel:    "true",
			docker.LabelKind:       db.KindApp,
			docker.LabelResource:   app.ID,
			docker.LabelDeployment: dep.ID,
		},
	}
	if spec.EnvFile, err = d.writeEnvFile(ctx, r, app); err != nil {
		return err
	}
	if spec.Mounts, err = d.prepareMounts(ctx, r, app); err != nil {
		return err
	}

	// A container left under this name by an interrupted run of the same
	// deployment would block the name.
	if err := dk.Remove(ctx, container); err != nil {
		return err
	}
	// From here on, a failure must not leave the new container behind.
	defer func() {
		if err != nil {
			clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if rerr := dk.Remove(clean, container); rerr != nil {
				d.Log.Error("remove failed container", "container", container, "err", rerr)
			}
		}
	}()

	port, err := d.runOnFreePort(ctx, dk, server.ID, spec, log)
	if err != nil {
		return err
	}

	if err := d.waitHealthy(ctx, dk, app, container, port, log); err != nil {
		if errors.Is(err, errExited) {
			log.Step("The container's last output:")
			tail, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			dk.Logs(tail, container, 40, false, log)
			cancel()
		}
		return err
	}
	log.Step("Healthy on port %d", port)

	// Switch traffic. From this point the deployment has succeeded: the new
	// container is recorded as serving, so a later error must not remove it.
	if err := d.DB.SetAppRuntime(ctx, app.ID, db.AppRunning, container, port, image); err != nil {
		return err
	}
	switch serr := d.SyncRoutes(ctx, server); {
	case errors.Is(serr, ErrProxyDown):
		log.Step("Routes written, but the proxy is not running. Start it with: musdash proxy")
	case serr != nil:
		log.Step("Could not update the proxy's routes: %v", serr)
		d.Log.Error("sync routes", "server", server.ID, "err", serr)
	default:
		log.Step("Traffic switched to the new container")
	}

	if app.Container != "" && app.Container != container {
		// Let requests already inside the old container finish.
		select {
		case <-time.After(d.drain):
		case <-ctx.Done():
		}
		old, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.stopGrace+30*time.Second)
		defer cancel()
		log.Step("Stopping the previous container")
		if serr := dk.Stop(old, app.Container, d.stopGrace); serr != nil {
			d.Log.Error("stop old container", "container", app.Container, "err", serr)
		}
		if rerr := dk.Remove(old, app.Container); rerr != nil {
			d.Log.Error("remove old container", "container", app.Container, "err", rerr)
			log.Step("The previous container could not be removed: %v", rerr)
		}
	}
	return nil
}

// runOnFreePort starts the container on a host port nothing else uses,
// trying another port when Docker reports the first as taken.
func (d *Deployer) runOnFreePort(ctx context.Context, dk docker.Client, serverID string, spec docker.RunSpec, log *Log) (int, error) {
	used, err := d.DB.UsedHostPorts(ctx, serverID)
	if err != nil {
		return 0, err
	}
	const tries = 6
	for range tries {
		port := pickPort(used)
		if port == 0 {
			return 0, errors.New("no free host port is left on this server")
		}
		used[port] = true
		spec.HostPort = port
		log.Step("Starting container on 127.0.0.1:%d", port)
		err := dk.Run(ctx, spec)
		if err == nil {
			return port, nil
		}
		if !errors.Is(err, docker.ErrPortTaken) {
			return 0, fmt.Errorf("start container: %w", err)
		}
		// Docker keeps the container it failed to start; clear the name.
		log.Step("Port %d is in use by something else; trying another", port)
		if err := dk.Remove(ctx, spec.Name); err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("could not find a free host port after %d tries", tries)
}

// pickPort returns a random unused port from the range, or 0 when full.
func pickPort(used map[int]bool) int {
	size := portMax - portMin + 1
	start := rand.IntN(size)
	for i := range size {
		if p := portMin + (start+i)%size; !used[p] {
			return p
		}
	}
	return 0
}

// writeEnvFile writes the app's runtime variables to its env file on the
// server and returns the path. The file is private: it holds secrets.
func (d *Deployer) writeEnvFile(ctx context.Context, r runner.Runner, app db.App) (string, error) {
	sealed, err := d.DB.ListEnvVars(ctx, db.KindApp, app.ID)
	if err != nil {
		return "", err
	}
	vars := make([]db.EnvVar, 0, len(sealed))
	for _, v := range sealed {
		if v.BuildTime {
			continue
		}
		plain, err := d.Box.OpenString(v.Value)
		if err != nil {
			return "", fmt.Errorf("environment variable %s cannot be decrypted: was the master key changed?", v.Key)
		}
		vars = append(vars, db.EnvVar{Key: v.Key, Value: plain})
	}
	body, err := EnvFile(vars)
	if err != nil {
		return "", err
	}
	dir := d.Cfg.AppDir(app.ID)
	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "env")
	if err := r.WriteFile(ctx, path, 0o600, strings.NewReader(body)); err != nil {
		return "", fmt.Errorf("write env file: %w", err)
	}
	return path, nil
}

// prepareMounts turns the app's storages into mounts, writing file mounts to
// the server first.
func (d *Deployer) prepareMounts(ctx context.Context, r runner.Runner, app db.App) ([]docker.Mount, error) {
	storages, err := d.DB.ListStorages(ctx, db.KindApp, app.ID)
	if err != nil {
		return nil, err
	}
	var mounts []docker.Mount
	for _, s := range storages {
		switch s.Kind {
		case db.StorageVolume:
			mounts = append(mounts, docker.Mount{Kind: docker.MountVolume, Source: VolumeName(app.ID, s.Source), Target: s.Target})
		case db.StorageBind:
			mounts = append(mounts, docker.Mount{Kind: docker.MountBind, Source: s.Source, Target: s.Target})
		case db.StorageFile:
			content, err := d.Box.OpenString(s.Content)
			if err != nil {
				return nil, fmt.Errorf("file mount %s cannot be decrypted: was the master key changed?", s.Target)
			}
			dir := filepath.Join(d.Cfg.AppDir(app.ID), "files")
			if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
				return nil, err
			}
			path := filepath.Join(dir, s.ID)
			// 0644: the process inside the container usually runs as
			// another user and must be able to read its config file.
			if err := r.WriteFile(ctx, path, 0o644, strings.NewReader(content)); err != nil {
				return nil, fmt.Errorf("write file mount %s: %w", s.Target, err)
			}
			mounts = append(mounts, docker.Mount{Kind: docker.MountBind, Source: path, Target: s.Target})
		}
	}
	return mounts, nil
}

// Stop takes an app offline: its route goes first, then its container.
func (d *Deployer) Stop(ctx context.Context, app db.App) error {
	server, err := d.DB.ServerByID(ctx, app.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	// Clearing the container first also makes the status monitor ignore the
	// "die" event this stop is about to cause.
	if err := d.DB.SetAppRuntime(ctx, app.ID, db.AppStopped, "", 0, app.DeployedImage); err != nil {
		return err
	}
	if err := d.SyncRoutes(ctx, server); err != nil && !errors.Is(err, ErrProxyDown) {
		d.Log.Error("sync routes", "server", server.ID, "err", err)
	}
	if app.Container == "" {
		return nil
	}
	if err := dk.Stop(ctx, app.Container, d.stopGrace); err != nil {
		return err
	}
	return dk.Remove(ctx, app.Container)
}

// Destroy stops an app and deletes it, with its env file and file mounts.
// Docker volumes are kept: they hold the person's data, and removing them is
// a separate, explicit act.
func (d *Deployer) Destroy(ctx context.Context, app db.App) error {
	if err := d.Stop(ctx, app); err != nil {
		return err
	}
	server, err := d.DB.ServerByID(ctx, app.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	if err := r.RemoveAll(ctx, d.Cfg.AppDir(app.ID)); err != nil {
		return err
	}
	return d.DB.DeleteApp(ctx, app.ID)
}
