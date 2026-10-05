// Package deploy turns an app's settings into a running container and keeps
// the proxy's routes in step with what is running.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
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
	// Probe replaces the health probe, which otherwise connects through
	// each server's Runner. Tests set it.
	Probe Probe
	// Tokens mints repository tokens for apps deployed through a GitHub App.
	Tokens TokenSource
	// Comments tells a pull request where its preview is. Nil writes none.
	Comments PullRequestComments
	// InstanceTarget is the control plane's own loopback address, routed
	// when a dashboard domain is set.
	InstanceTarget string
	// Notify, when set, is told about things worth telling the people of
	// an environment's team: a deployment's end, a container that stopped.
	// It must not block.
	Notify func(environmentID string, e notify.Event)

	healthEvery time.Duration // how often a starting container is checked
	drain       time.Duration // pause between switching traffic and stopping the old container
	stopGrace   time.Duration // how long a container gets to exit after SIGTERM
	pollWait    time.Duration // how long the proxy may take to notice a routes file it was not signalled about
	// Databases get longer than apps on both ends: a first start
	// initialises the data directory, and a stop must flush it.
	dbStartTimeout time.Duration
	dbStopGrace    time.Duration
	// How long a service's stack may take to come up, images already
	// pulled.
	serviceStartTimeout time.Duration
	// A clone and a build each get this long before they are stopped.
	cloneTimeout time.Duration
	buildTimeout time.Duration
	// extraGitEnv is added to every clone's environment. Tests use it to
	// point a repository address at a local repository.
	extraGitEnv []string

	// routesOf holds a lock per server that makes each publication of its
	// routes one step: read the database, write the file, signal. Without
	// it a slower publication could write older data over a newer file.
	// routesMu guards the map.
	routesMu sync.Mutex
	routesOf map[string]*sync.Mutex
	// appLocks serialise everything that changes one app's container: a
	// deployment, a stop, a delete. They are striped by app id so the set
	// never grows.
	appLocks [64]sync.Mutex
}

// ErrBusy is returned by Stop and Destroy while a deployment of the app is
// running.
var ErrBusy = errors.New("a deployment of this app is in progress")

// keepDeployments is how many finished deployments, with their logs, are
// kept per app.
const keepDeployments = 50

// lockFor returns the lock guarding one app's container.
func (d *Deployer) lockFor(appID string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(appID))
	return &d.appLocks[h.Sum32()%uint32(len(d.appLocks))]
}

// lockSoon takes a resource's lock unless something holds on to it. A
// deployment keeps the lock for a moment after it has recorded its result;
// a stop pressed in that moment should wait for it, not be told that a
// deployment is in progress.
func lockSoon(mu *sync.Mutex) bool {
	const patience, step = 750 * time.Millisecond, 15 * time.Millisecond
	for waited := time.Duration(0); ; waited += step {
		if mu.TryLock() {
			return true
		}
		if waited >= patience {
			return false
		}
		time.Sleep(step)
	}
}

// New returns a Deployer with production timings.
func New(d *db.DB, box *secret.Box, q *jobs.Queue, r Runners, cfg *config.Config, log *slog.Logger, instanceTarget string) *Deployer {
	return &Deployer{
		DB: d, Box: box, Queue: q, Runners: r, Cfg: cfg, Log: log,
		Tokens:              source.NewGitHub(),
		Comments:            source.NewGitHub(),
		InstanceTarget:      instanceTarget,
		healthEvery:         time.Second,
		drain:               3 * time.Second,
		stopGrace:           30 * time.Second,
		pollWait:            4 * time.Second,
		dbStartTimeout:      5 * time.Minute,
		dbStopGrace:         60 * time.Second,
		serviceStartTimeout: 10 * time.Minute,
		cloneTimeout:        10 * time.Minute,
		buildTimeout:        30 * time.Minute,
	}
}

// PathsOn is the data-directory layout on the server a Runner reaches. For
// this machine it is cfg itself; a remote server has its own directory.
// Every path given to a Runner comes from here. Files the control plane
// writes for itself, such as deployment logs, come from cfg directly.
func PathsOn(cfg *config.Config, r runner.Runner) config.Config {
	return cfg.On(runner.DataDirOf(r))
}

// at is PathsOn for the deployer's own configuration.
func (d *Deployer) at(r runner.Runner) config.Config { return PathsOn(d.Cfg, r) }

// tell reports an event to whoever listens.
func (d *Deployer) tell(environmentID string, e notify.Event) {
	if d.Notify != nil {
		d.Notify(environmentID, e)
	}
}

// Register installs the deploy job handler on the queue.
func (d *Deployer) Register() {
	d.Queue.Register(JobDeploy, d.runJob)
	d.Queue.Register(JobDatabase, d.runDatabaseJob)
	d.Queue.Register(JobService, d.runServiceJob)
	d.Queue.Register(JobClosePreview, d.runClosePreview)
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
	dep := db.Deployment{AppID: app.ID, Trigger: trigger, Image: app.Image}
	if app.Source == db.SourceGit {
		dep.Image = "" // known once the commit is built
	}
	return d.enqueue(ctx, app, dep)
}

// TriggerRollback is the trigger of a deployment made by Rollback.
const TriggerRollback = "rollback"

// ErrNoRollback is returned when a deployment cannot be rolled back to.
var ErrNoRollback = errors.New("this deployment cannot be rolled back to")

// Rollback queues a deployment that runs the image of an earlier,
// successful deployment of the same app again. Nothing is pulled or built:
// the image is the one that ran then, as it was kept on the server. The
// app's settings and variables are today's; only the image goes back.
func (d *Deployer) Rollback(ctx context.Context, app db.App, to db.Deployment) (db.Deployment, error) {
	// The image must be one of this app's own. A name from anywhere else
	// would let a deployment row decide what another app runs.
	if to.AppID != app.ID || to.Status != db.DeploySuccess || !ownImage(app.ID, to.KeptImage) {
		return db.Deployment{}, ErrNoRollback
	}
	return d.enqueue(ctx, app, db.Deployment{AppID: app.ID, Trigger: TriggerRollback,
		Image: to.Image, CommitSHA: to.CommitSHA, RollbackOf: to.ID, KeptImage: to.KeptImage})
}

// ownImage reports whether an image name is in the app's own repository.
func ownImage(appID, image string) bool {
	tag, ok := strings.CutPrefix(image, ImageRepository(appID)+":")
	return ok && tag != "" && docker.ValidImage(image)
}

// keptName is the name under which a pulled image is kept for a deployment.
func keptName(appID, deploymentID string) string {
	return ImageRepository(appID) + ":d-" + deploymentID
}

// deployLock is the lock key under which an app's deployments are queued.
//
// Deployments of one app never overlap. Builds additionally run one at a
// time per server: an image build can use a gigabyte or more of memory,
// and two at once would exhaust a small server. Every Git deployment of an
// app is on that app's one server, so the build lock also keeps them in
// order; a rollback of such an app takes the same lock for that reason,
// though it builds nothing.
func deployLock(app db.App) string {
	if app.Source != db.SourceGit {
		return "deploy:" + app.ID
	}
	// The lock is of the server that does the building.
	if app.BuildServerID != "" {
		return "build:" + app.BuildServerID
	}
	return "build:" + app.ServerID
}

func (d *Deployer) enqueue(ctx context.Context, app db.App, dep db.Deployment) (db.Deployment, error) {
	lock := deployLock(app)
	dep, err := d.DB.CreateDeployment(ctx, dep)
	if err != nil {
		return dep, err
	}
	// One attempt: a failed deploy is reported, not silently repeated.
	_, err = d.Queue.Enqueue(ctx, JobDeploy, payload{DeploymentID: dep.ID}, jobs.WithLockKey(lock), jobs.WithMaxAttempts(1))
	if err != nil {
		// Recorded even if the request that asked has gone away: a row left
		// as "queued" would make later pushes think a deployment is waiting.
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if ferr := d.DB.FinishDeployment(rec, dep.ID, db.DeployFailed, "could not be queued: "+err.Error()); ferr != nil {
			d.Log.Error("record deployment", "deployment", dep.ID, "err", ferr)
		}
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
	// Held for the whole deployment, so a stop or delete cannot interleave
	// with it. The app is read after the lock is taken: it is the state this
	// deployment starts from.
	mu := d.lockFor(dep.AppID)
	mu.Lock()
	defer mu.Unlock()
	app, err := d.DB.AppByID(ctx, dep.AppID)
	if err == nil && app.IsPreview() {
		// What a preview is built and run with is its parent's, as it is
		// now: a port or a Dockerfile path changed there since the pull
		// request was opened applies here too.
		if err = d.DB.RefreshPreview(ctx, app.ID); err == nil {
			app, err = d.DB.AppByID(ctx, dep.AppID)
		}
	}
	if err != nil {
		// The app was deleted while this deployment waited in the queue.
		d.DB.FinishDeployment(ctx, dep.ID, db.DeployFailed, "the app no longer exists")
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
		if ferr := d.DB.FinishDeployment(rec, dep.ID, db.DeploySuccess, ""); ferr != nil {
			return ferr
		}
		d.pruneDeployments(rec, app.ID)
		if app.IsPreview() {
			if done, derr := d.DB.DeploymentByID(rec, dep.ID); derr == nil {
				d.announcePreview(rec, app, done)
			}
		}
		d.tell(app.EnvironmentID, notify.Event{Kind: notify.EventDeploy, OK: true, At: time.Now(),
			Title: app.Name + " was deployed", URL: "/apps/" + app.ID + "/deployments/" + dep.ID})
		return nil
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
	body := err.Error()
	if status == db.AppRunning {
		body += "\n\nThe version from before is still running."
	}
	d.tell(app.EnvironmentID, notify.Event{Kind: notify.EventDeploy, At: time.Now(),
		Title: "The deployment of " + app.Name + " failed", Body: body, URL: "/apps/" + app.ID + "/deployments/" + dep.ID})
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

	// Checked before any work: a refused option should not cost a build.
	extraArgs, err := ParseRunOptions(app.DockerOptions)
	if err != nil {
		return fmt.Errorf("custom Docker options: %w", err)
	}

	// kept is the name of the app's own under which this deployment's
	// image stays on the server, to be rolled back to.
	kept := dep.KeptImage
	switch {
	case dep.RollbackOf != "":
		// Checked again here: the row is what decides which image runs.
		if !ownImage(app.ID, kept) {
			return ErrNoRollback
		}
		was := dep.Image
		if was == "" {
			was = kept
		}
		log.Step("Rolling back to the image of an earlier deployment (%s)", was)
		have, herr := dk.HasImage(ctx, kept)
		if herr != nil {
			return herr
		}
		if !have {
			return errors.New("the image of that deployment is no longer on the server; deploy again instead")
		}
		image = kept
	case app.Source == db.SourceGit:
		var commit string
		if image, commit, err = d.buildFor(ctx, r, server, app, dep, log); err != nil {
			return err
		}
		if err := d.DB.SetDeploymentBuild(ctx, dep.ID, image, commit); err != nil {
			return err
		}
	default:
		log.Step("Pulling %s", image)
		if err := dk.Pull(ctx, image, log); err != nil {
			return fmt.Errorf("pull %s: %w", image, err)
		}
	}
	if dep.RollbackOf == "" {
		// A tag such as nginx:latest moves, and so does the tag of a
		// commit that is built a second time. Under a name of this
		// deployment's own the image that is deployed now stays what it is.
		kept = keptName(app.ID, dep.ID)
		if err := dk.Tag(ctx, image, kept); err != nil {
			return fmt.Errorf("keep the image for a rollback: %w", err)
		}
		if err := d.DB.SetDeploymentKept(ctx, dep.ID, kept); err != nil {
			return err
		}
	}
	// Old images go only once this one is serving.
	defer func() {
		if err == nil {
			d.pruneImages(context.WithoutCancel(ctx), dk, app.ID, kept, image)
		}
	}()

	network := NetworkName(app.EnvironmentID)
	if err := dk.EnsureNetwork(ctx, network); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	spec := docker.RunSpec{
		// Pulled, built or kept a moment ago: it is here, or the
		// deployment fails. Never fetched under this name from outside.
		Local: true,
		Name:  container, Image: image, Network: network, Alias: app.Name,
		ContainerPort: app.Port, MemoryMB: app.MemoryMB, CPUs: app.CPUs,
		ExtraArgs: extraArgs,
		Labels: map[string]string{
			docker.ManagedLabel:    "true",
			docker.LabelKind:       db.KindApp,
			docker.LabelResource:   app.ID,
			docker.LabelDeployment: dep.ID,
		},
	}
	if app.StartCommand != "" {
		// Run through the image's shell, so the command may use pipes,
		// variables and the like as it would in a Dockerfile's CMD.
		spec.Command = []string{"sh", "-c", app.StartCommand}
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

	// Switch traffic. Once begun this must finish, even if musdash is being
	// shut down: stopping halfway would leave the routes and the database
	// disagreeing about which container serves.
	sw, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := d.DB.SetAppRuntime(sw, app.ID, db.AppRunning, container, port, image); err != nil {
		return fmt.Errorf("record the new container: %w", err)
	}
	switch serr := d.SyncRoutes(sw, server); {
	case errors.Is(serr, ErrProxyDown):
		// Nothing is serving traffic on this server at all, so there is
		// nothing to switch. The routes are written for when the proxy starts.
		log.Step("Routes written, but the proxy is not running. Start it with: musdash proxy")
	case serr != nil:
		// The routes still point at the previous container. Put the record
		// back and fail: stopping the previous container now would take the
		// app offline.
		if rerr := d.DB.SetAppRuntime(sw, app.ID, statusAfterFailure(app), app.Container, app.HostPort, app.DeployedImage); rerr != nil {
			d.Log.Error("restore app after failed switch", "app", app.ID, "err", rerr)
		}
		return fmt.Errorf("publish the new routes: %w", serr)
	default:
		log.Step("Traffic switched to the new container")
	}

	if app.Container != "" && app.Container != container {
		// Let requests already inside the old container finish.
		time.Sleep(d.drain)
		log.Step("Stopping the previous container")
		if serr := dk.Stop(sw, app.Container, d.stopGrace); serr != nil {
			d.Log.Error("stop old container", "container", app.Container, "err", serr)
		}
		// If this fails the container is found and removed by the next
		// reconcile, which clears containers no app points at.
		if rerr := dk.Remove(sw, app.Container); rerr != nil {
			d.Log.Error("remove old container", "container", app.Container, "err", rerr)
			log.Step("The previous container could not be removed yet: %v", rerr)
		}
	}
	return nil
}

// pruneDeployments drops an app's oldest finished deployments and their logs.
func (d *Deployer) pruneDeployments(ctx context.Context, appID string) {
	ids, err := d.DB.PruneDeployments(ctx, appID, keepDeployments)
	if err != nil {
		d.Log.Error("prune deployments", "app", appID, "err", err)
		return
	}
	for _, id := range ids {
		os.Remove(d.Cfg.DeployLogPath(id))
	}
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
	// A preview runs with its parent's variables, as they are now.
	sealed, err := d.DB.ListEnvVars(ctx, db.KindApp, app.ConfigOwner())
	if err != nil {
		return "", err
	}
	vars := make([]db.EnvVar, 0, len(sealed)+2)
	// What lets an app tell that it is a preview, and of which pull
	// request: to use another database than production's, for one.
	if app.IsPreview() {
		vars = append(vars, db.EnvVar{Key: "MUSDASH_PREVIEW", Value: "1"},
			db.EnvVar{Key: "MUSDASH_PULL_REQUEST", Value: strconv.Itoa(app.PRNumber)})
	}
	for _, v := range sealed {
		if v.BuildTime || (app.IsPreview() && (v.Key == "MUSDASH_PREVIEW" || v.Key == "MUSDASH_PULL_REQUEST")) {
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
	dir := d.at(r).AppDir(app.ID)
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
	storages, err := d.DB.ListStorages(ctx, db.KindApp, app.ConfigOwner())
	if err != nil {
		return nil, err
	}
	var mounts []docker.Mount
	for _, s := range storages {
		// A preview gets its parent's files and nothing that holds data:
		// a directory of the server would be production's own, and a
		// volume would be one more thing to remove with every pull request.
		if app.IsPreview() && s.Kind != db.StorageFile {
			continue
		}
		switch s.Kind {
		case db.StorageVolume:
			mounts = append(mounts, docker.Mount{Kind: docker.MountVolume, Source: VolumeName(app.ID, s.Source), Target: s.Target})
		case db.StorageBind:
			// Checked again at deploy time, with the data directory this
			// install actually uses.
			if err := docker.CheckBindSource(s.Source, d.at(r).DataDir); err != nil {
				return nil, err
			}
			mounts = append(mounts, docker.Mount{Kind: docker.MountBind, Source: s.Source, Target: s.Target})
		case db.StorageFile:
			content, err := d.Box.OpenString(s.Content)
			if err != nil {
				return nil, fmt.Errorf("file mount %s cannot be decrypted: was the master key changed?", s.Target)
			}
			dir := filepath.Join(d.at(r).AppDir(app.ID), "files")
			if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
				return nil, err
			}
			path := filepath.Join(dir, s.ID)
			// 0644: the process inside the container usually runs as
			// another user and must be able to read its config file.
			if err := r.WriteFile(ctx, path, 0o644, strings.NewReader(content)); err != nil {
				return nil, fmt.Errorf("write file mount %s: %w", s.Target, err)
			}
			mounts = append(mounts, docker.Mount{Kind: docker.MountBind, Source: path, Target: s.Target, Internal: true})
		}
	}
	return mounts, nil
}

// Stop takes an app offline: its route goes first, then its container. It
// returns ErrBusy while a deployment of the app is running.
//
// Stopping is several steps that must not be abandoned halfway, so it does
// not end when the caller's context does (a closed browser tab, a proxy
// timeout); it has its own deadline.
func (d *Deployer) Stop(ctx context.Context, appID string) error {
	mu := d.lockFor(appID)
	if !lockSoon(mu) {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.stopGrace+90*time.Second)
	defer cancel()
	app, err := d.DB.AppByID(ctx, appID)
	if err != nil {
		return err
	}
	_, err = d.stopLocked(ctx, app)
	return err
}

// stopLocked stops an app whose lock the caller holds.
//
// The order matters at each step. The route is withdrawn before the
// container stops, so no request is sent to a dying container. The
// container's name is forgotten only after it is really gone, so a stop
// that fails partway can simply be run again.
func (d *Deployer) stopLocked(ctx context.Context, app db.App) (db.Server, error) {
	server, err := d.DB.ServerByID(ctx, app.ServerID)
	if err != nil {
		return server, err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return server, err
	}
	dk := docker.Client{R: r}
	if err := d.DB.SetAppStopping(ctx, app.ID); err != nil {
		return server, err
	}
	if err := d.SyncRoutes(ctx, server); err != nil && !errors.Is(err, ErrProxyDown) {
		// The proxy also re-reads its file on its own; carry on.
		d.Log.Error("sync routes", "server", server.ID, "err", err)
	}
	if app.Container == "" {
		return server, nil
	}
	if err := dk.Stop(ctx, app.Container, d.stopGrace); err != nil {
		return server, fmt.Errorf("stop the container: %w", err)
	}
	if err := dk.Remove(ctx, app.Container); err != nil {
		return server, fmt.Errorf("remove the container: %w", err)
	}
	return server, d.DB.ClearAppContainer(ctx, app.ID, app.Container)
}

// Destroy stops an app and deletes it, with its env file, file mounts and
// deployment logs. Docker volumes are kept: they hold the person's data,
// and removing them is a separate, explicit act.
//
// An app's previews go first, each as an app of its own.
func (d *Deployer) Destroy(ctx context.Context, appID string) error {
	previews, err := d.DB.Previews(ctx, appID)
	if err != nil {
		return err
	}
	for _, p := range previews {
		// Before this app's own lock is taken: the locks are shared among
		// apps, and a preview may wait on the same one.
		if err := d.destroy(ctx, p.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
	}
	return d.destroy(ctx, appID)
}

func (d *Deployer) destroy(ctx context.Context, appID string) error {
	mu := d.lockFor(appID)
	if !lockSoon(mu) {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.stopGrace+90*time.Second)
	defer cancel()
	app, err := d.DB.AppByID(ctx, appID)
	if err != nil {
		return err
	}
	// A pull request opened in this very moment: the row could not be
	// deleted, so nothing is torn down either. Asked again, the delete
	// takes the new preview first.
	if late, err := d.DB.Previews(ctx, appID); err != nil {
		return err
	} else if len(late) > 0 {
		return db.ErrHasPreviews
	}
	server, err := d.stopLocked(ctx, app)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	if err := r.RemoveAll(ctx, d.at(r).AppDir(app.ID)); err != nil {
		return err
	}
	// The images built or kept for it are of no use to anything else.
	dk := docker.Client{R: r}
	repo := ImageRepository(app.ID)
	tags, err := dk.ImageTags(ctx, repo)
	if err != nil {
		d.Log.Warn("list a deleted app's images", "app", app.ID, "err", err)
	}
	for _, tag := range tags {
		if err := dk.RemoveImage(ctx, repo+":"+tag); err != nil {
			d.Log.Warn("remove a deleted app's image", "image", repo+":"+tag, "err", err)
		}
	}
	logs, err := d.DB.DeploymentIDs(ctx, app.ID)
	if err != nil {
		return err
	}
	runs, err := d.DB.TaskRunIDs(ctx, app.ID)
	if err != nil {
		return err
	}
	if err := d.DB.DeleteApp(ctx, app.ID); err != nil {
		return err
	}
	for _, id := range logs {
		os.Remove(d.Cfg.DeployLogPath(id))
	}
	for _, id := range runs {
		os.Remove(d.Cfg.TaskLogPath(id))
	}
	return nil
}
