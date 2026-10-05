package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/compose"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// A service is a Compose stack. Its file is never loaded on the server as
// the person wrote it: it is loaded in a sandbox, checked, amended, and the
// result is what `docker compose up` is given. See the compose package.

// JobService is the job kind that deploys a service.
const JobService = "service"

// sandboxEnvFile holds a stack's variables for the sandboxed load.
const sandboxEnvFile = "sandbox.env"

// ServiceProject is the Compose project name of a service. Its containers
// are named after it: musdash-<id>-<compose service>-1.
func ServiceProject(id string) string { return "musdash-" + id }

type servicePayload struct {
	ID string `json:"id"`
}

// EnqueueService queues a deployment of a service. A service that is
// already deploying answers ErrBusy; again queues another deployment
// regardless, for a change the one in progress may not have seen.
func (d *Deployer) EnqueueService(ctx context.Context, s db.Service, again bool) error {
	began, err := d.DB.BeginServiceDeploy(ctx, s.ID)
	if err != nil {
		return err
	}
	if !began && !again {
		return ErrBusy
	}
	if began {
		// The previous deployment's output goes, so a page that follows the
		// log waits for the new one instead of showing the old.
		os.Remove(d.Cfg.ServiceLogPath(s.ID))
	}
	_, err = d.Queue.Enqueue(ctx, JobService, servicePayload{ID: s.ID}, jobs.WithLockKey("service:"+s.ID), jobs.WithMaxAttempts(1))
	if err != nil && began {
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if serr := d.DB.SetServiceState(rec, s.ID, db.AppFailed, "the deployment could not be queued: "+err.Error()); serr != nil {
			d.Log.Error("record service failure", "service", s.ID, "err", serr)
		}
	}
	return err
}

func (d *Deployer) runServiceJob(ctx context.Context, raw []byte) error {
	var p servicePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	mu := d.lockFor(p.ID)
	mu.Lock()
	defer mu.Unlock()
	s, err := d.DB.ServiceByID(ctx, p.ID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("service %s: %w", p.ID, err))
	}
	switch s.Status {
	case db.AppStopped:
		// Stopped while this deployment waited its turn. The person's last
		// word was "stop".
		return nil
	case db.AppDeploying:
	default:
		// A deployment queued behind another one: the first has finished
		// and set a status of its own.
		if err := d.DB.SetServiceState(ctx, s.ID, db.AppDeploying, ""); err != nil {
			return err
		}
	}
	log, err := OpenLog(d.Cfg.ServiceLogPath(s.ID))
	if err != nil {
		d.DB.SetServiceState(ctx, s.ID, db.AppFailed, err.Error())
		return jobs.Permanent(err)
	}
	defer log.Close()

	err = d.deployService(ctx, s, log)
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err == nil {
		log.Step("Deployed.")
		return d.DB.SetServiceState(rec, s.ID, db.AppRunning, "")
	}
	if ctx.Err() != nil {
		log.Step("Interrupted: musdash is shutting down. The deployment will restart.")
		return err
	}
	log.Step("Failed: %v", err)
	if serr := d.DB.SetServiceState(rec, s.ID, d.serviceStatusAfterFailure(rec, s), err.Error()); serr != nil {
		d.Log.Error("record service failure", "service", s.ID, "err", serr)
	}
	return jobs.Permanent(err)
}

// serviceStatusAfterFailure is the status of a service whose deployment
// failed. A redeployment that fails before anything is started leaves the
// containers from before running; the service is then still what they
// make it, with the failure kept as its last error.
func (d *Deployer) serviceStatusAfterFailure(ctx context.Context, s db.Service) string {
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return db.AppFailed
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return db.AppFailed
	}
	listed, err := docker.Client{R: r}.List(ctx)
	if err != nil {
		return db.AppFailed
	}
	if status := serviceStatus(listed, s.ID); status == db.AppRunning || status == db.AppDegraded {
		return status
	}
	return db.AppFailed
}

// ServiceVariables opens a service's stored variables: the values musdash
// generated and the ones a person entered.
func (d *Deployer) ServiceVariables(s db.Service) (map[string]string, error) {
	vars := map[string]string{}
	if s.Variables == "" {
		return vars, nil
	}
	plain, err := d.Box.Open(s.Variables)
	if err != nil {
		return nil, errors.New("the service's variables cannot be decrypted: was the master key changed?")
	}
	if err := json.Unmarshal(plain, &vars); err != nil {
		return nil, fmt.Errorf("the service's stored variables are damaged: %w", err)
	}
	return vars, nil
}

// SealServiceVariables seals variables for storage.
func (d *Deployer) SealServiceVariables(vars map[string]string) (string, error) {
	plain, err := json.Marshal(vars)
	if err != nil {
		return "", err
	}
	return d.Box.Seal(plain)
}

// PrepareService brings what is stored about a service in line with its
// Compose file: a generated value for every magic variable that asks for
// one, and an endpoint for every address the file names. It is called when
// the file is saved and again before each deployment. vars is updated in
// place and stored when something was generated.
//
// newHost supplies the domain of an endpoint that did not exist yet.
func (d *Deployer) PrepareService(ctx context.Context, s db.Service, vars map[string]string, newHost func(name string) (string, bool)) error {
	magic := catalog.ScanMagic(s.Compose)
	generated := false
	seen := map[string]bool{}
	var endpoints []string
	for _, v := range magic {
		if v.Address() {
			if !seen[v.ID] {
				seen[v.ID] = true
				endpoints = append(endpoints, v.ID)
			}
			continue
		}
		if _, have := vars[v.Name]; have {
			continue
		}
		if value, ok := catalog.Generate(v); ok {
			vars[v.Name] = value
			generated = true
		}
	}
	if generated {
		sealed, err := d.SealServiceVariables(vars)
		if err != nil {
			return err
		}
		if err := d.DB.SetServiceVariables(ctx, s.ID, sealed); err != nil {
			return err
		}
	}
	return d.DB.SyncEndpoints(ctx, s.ID, endpoints, newHost)
}

// serviceEnv renders the variables a stack's file is filled in with: the
// stored ones, and the address variables worked out from the endpoints'
// domains.
func serviceEnv(composeText string, vars map[string]string, endpoints []db.Endpoint) (string, error) {
	byName := map[string]db.Endpoint{}
	for _, e := range endpoints {
		byName[e.Name] = e
	}
	all := map[string]string{}
	for k, v := range vars {
		all[k] = v
	}
	for _, v := range catalog.ScanMagic(composeText) {
		if !v.Address() {
			continue
		}
		e := byName[v.ID]
		if e.Host == "" {
			return "", fmt.Errorf("%s has no domain: give %s one under Settings", v.Name, strings.ToLower(v.ID))
		}
		all[v.Name] = catalog.AddressValue(v, e.Host, e.TLS)
	}
	list := make([]db.EnvVar, 0, len(all))
	for k, v := range all {
		list = append(list, db.EnvVar{Key: k, Value: v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return EnvFile(list)
}

// sandboxUser is the "uid:gid" the sandbox runs as: the user musdash's
// commands run as on the server, who owns the files it may need to read.
func sandboxUser(ctx context.Context, r runner.Runner) string {
	ids := make([]string, 0, 2)
	for _, flag := range []string{"-u", "-g"} {
		out, err := r.Output(ctx, runner.Cmd{Name: "id", Args: []string{flag}})
		id := strings.TrimSpace(string(out))
		if _, convErr := strconv.Atoi(id); err != nil || convErr != nil {
			// Nobody: enough to load a file fed on standard input.
			return "65534:65534"
		}
		ids = append(ids, id)
	}
	return ids[0] + ":" + ids[1]
}

// composeCmd is a `docker compose` command for a service's project, run on
// the server against the resolved file.
func (d *Deployer) composeCmd(id string, args ...string) runner.Cmd {
	dir := d.Cfg.AppDir(id)
	full := append([]string{"compose", "--project-name", ServiceProject(id), "--project-directory", dir,
		"--file", filepath.Join(dir, compose.ResolvedFile), "--ansi", "never"}, args...)
	return runner.Cmd{Name: "docker", Args: full, Dir: dir}
}

// portTaken reports whether Compose failed because a port of the server
// was in use.
func portTaken(output string) bool {
	msg := strings.ToLower(output)
	return strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use")
}

// deployService loads, checks and starts a service's stack. The caller
// holds the service's lock.
func (d *Deployer) deployService(ctx context.Context, s db.Service, log *Log) error {
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	dir := d.Cfg.AppDir(s.ID)
	project := ServiceProject(s.ID)

	vars, err := d.ServiceVariables(s)
	if err != nil {
		return err
	}
	if err := d.PrepareService(ctx, s, vars, nil); err != nil {
		return err
	}
	endpoints, err := d.DB.ListEndpoints(ctx, s.ID)
	if err != nil {
		return err
	}
	env, err := serviceEnv(s.Compose, vars, endpoints)
	if err != nil {
		return err
	}
	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		return err
	}
	envPath := filepath.Join(dir, sandboxEnvFile)
	if err := r.WriteFile(ctx, envPath, 0o600, strings.NewReader(env)); err != nil {
		return fmt.Errorf("write the variables: %w", err)
	}

	// The sandbox: Docker's own command line in a container.
	version, err := dk.Version(ctx)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	opt := compose.ConfigOptions{
		Image: compose.SandboxImage(version), Project: project, Dir: dir,
		Source: []byte(s.Compose), EnvFile: envPath, User: sandboxUser(ctx, r),
	}
	if have, err := dk.HasImage(ctx, opt.Image); err != nil {
		return err
	} else if !have {
		log.Step("Fetching %s, which reads Compose files safely", opt.Image)
		if err := dk.Pull(ctx, opt.Image, log); err != nil {
			return fmt.Errorf("pull %s: %w", opt.Image, err)
		}
	}

	log.Step("Reading the Compose file")
	rawOpt := opt
	rawOpt.Raw = true
	raw, err := compose.Config(ctx, r, rawOpt)
	if err != nil {
		return fmt.Errorf("the Compose file could not be read: %w", err)
	}
	found, err := compose.Endpoints(raw, catalog.ScanMagic(s.Compose))
	if err != nil {
		return err
	}
	resolved, err := compose.Config(ctx, r, opt)
	if err != nil {
		return fmt.Errorf("the Compose file could not be read: %w", err)
	}
	if err := resolved.Validate(compose.ValidateOptions{Dir: dir, Protected: []string{d.Cfg.DataDir}, ValidPort: ValidPublicPort}); err != nil {
		return fmt.Errorf("the Compose file asks for things a service may not do:\n%w", err)
	}

	members := resolved.Services()
	override := compose.Override{ServiceID: s.ID}
	if s.ConnectEnv {
		// On the environment's network every container answers to its
		// Compose service name. A name something else there already
		// answers to would make the two stand in for each other.
		taken, err := d.DB.NamesOnEnvNetwork(ctx, s.EnvironmentID, s.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			if what, clash := taken[m]; clash {
				return fmt.Errorf("the stack's service %q would share its name on the environment's network with %s; rename one of them, or do not connect this stack to the environment", m, what)
			}
		}
		override.EnvNetwork = NetworkName(s.EnvironmentID)
		if err := dk.EnsureNetwork(ctx, override.EnvNetwork); err != nil {
			return fmt.Errorf("network: %w", err)
		}
	}
	if err := d.DB.SetServiceMembers(ctx, s.ID, members); err != nil {
		return err
	}

	// Each endpoint gets a loopback port of the server for the proxy.
	byName := map[string]db.Endpoint{}
	for _, e := range endpoints {
		byName[e.Name] = e
	}
	assign := func(fresh bool) error {
		used, err := d.DB.UsedHostPorts(ctx, s.ServerID)
		if err != nil {
			return err
		}
		override.Publish = override.Publish[:0]
		for _, f := range found {
			e, ok := byName[f.Name]
			if !ok {
				return fmt.Errorf("no endpoint is recorded for %s", f.Name)
			}
			if e.HostPort == 0 || fresh {
				if e.HostPort = pickPort(used); e.HostPort == 0 {
					return errors.New("no free host port is left on this server")
				}
				used[e.HostPort] = true
			}
			if err := d.DB.SetEndpointTarget(ctx, e.ID, f.Service, f.Port, e.HostPort); err != nil {
				return err
			}
			byName[f.Name] = e
			override.Publish = append(override.Publish, compose.Published{Service: f.Service, Port: f.Port, HostPort: e.HostPort})
		}
		return nil
	}

	// Each attempt applies its ports to a fresh copy of the document that
	// was checked, never to one loaded again.
	checked, err := resolved.Marshal()
	if err != nil {
		return err
	}
	const tries = 3
	for attempt := 1; ; attempt++ {
		if err := assign(attempt > 1); err != nil {
			return err
		}
		doc, err := compose.Parse(checked)
		if err != nil {
			return err
		}
		doc.Apply(override)
		out, err := doc.Marshal()
		if err != nil {
			return err
		}
		// Private: the resolved file holds every variable's value.
		if err := r.WriteFile(ctx, filepath.Join(dir, compose.ResolvedFile), 0o600, strings.NewReader(string(out))); err != nil {
			return fmt.Errorf("write the resolved Compose file: %w", err)
		}

		if attempt == 1 {
			log.Step("Pulling images")
			pull := d.composeCmd(s.ID, "pull", "--ignore-buildable")
			pull.Stdout, pull.Stderr = log, log
			if err := r.Run(ctx, pull); err != nil {
				return fmt.Errorf("pull the images: %w", err)
			}
		}

		log.Step("Starting %s", strings.Join(members, ", "))
		upOut := &tail{limit: 4096}
		up := d.composeCmd(s.ID, "up", "--detach", "--remove-orphans", "--wait", "--wait-timeout", strconv.Itoa(int(d.serviceStartTimeout.Seconds())))
		up.Stdout, up.Stderr = teeWriter{log, upOut}, teeWriter{log, upOut}
		err = r.Run(ctx, up)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return err
		}
		if portTaken(upOut.String()) && attempt < tries {
			log.Step("A port of the server was in use by something else; trying others")
			continue
		}
		// What the containers said is what explains a stack that did not
		// come up.
		logs := &tail{limit: 2000}
		show := d.composeCmd(s.ID, "logs", "--tail", "25", "--no-color")
		show.Stdout, show.Stderr = logs, logs
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		r.Run(logCtx, show)
		cancel()
		return fmt.Errorf("the stack did not come up: %w. Its last output:\n%s", err, logs.String())
	}

	if err := d.DB.SetServiceState(ctx, s.ID, db.AppRunning, ""); err != nil {
		return err
	}
	// The stack is up whatever happens to the routes; a proxy that is not
	// running picks them up when it starts.
	if err := d.SyncRoutes(context.WithoutCancel(ctx), server); err != nil && !errors.Is(err, ErrProxyDown) {
		return fmt.Errorf("the stack is running, but its routes could not be published: %w", err)
	}
	return nil
}

// teeWriter writes to each of its writers.
type teeWriter []interface{ Write([]byte) (int, error) }

func (t teeWriter) Write(p []byte) (int, error) {
	for _, w := range t {
		w.Write(p)
	}
	return len(p), nil
}

// StopService stops a service's containers. They and their data stay.
func (d *Deployer) StopService(ctx context.Context, id string) error {
	mu := d.lockFor(id)
	if !mu.TryLock() {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.dbStopGrace+2*time.Minute)
	defer cancel()
	s, err := d.DB.ServiceByID(ctx, id)
	if err != nil {
		return err
	}
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	// Marked stopped first, so the monitor does not report the exits this
	// causes as a failure, and the routes go before the containers do.
	if err := d.DB.SetServiceState(ctx, s.ID, db.AppStopped, ""); err != nil {
		return err
	}
	if err := d.SyncRoutes(ctx, server); err != nil && !errors.Is(err, ErrProxyDown) {
		d.Log.Warn("withdraw a stopped service's routes", "service", s.ID, "err", err)
	}
	return d.composeDown(ctx, r, s, "stop", "--timeout", strconv.Itoa(int(d.dbStopGrace.Seconds())))
}

// composeDown runs a Compose command that winds a project down. A project
// that was never started has no resolved file; then there is nothing to do.
func (d *Deployer) composeDown(ctx context.Context, r runner.Runner, s db.Service, args ...string) error {
	file, err := r.ReadFile(ctx, filepath.Join(d.Cfg.AppDir(s.ID), compose.ResolvedFile))
	if err != nil {
		return nil
	}
	file.Close()
	out := &tail{limit: 1500}
	cmd := d.composeCmd(s.ID, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := r.Run(ctx, cmd); err != nil {
		return fmt.Errorf("docker compose %s: %w %s", args[0], err, out.String())
	}
	return nil
}

// DestroyService stops and deletes a service. Its volumes are removed only
// when deleteData is set.
func (d *Deployer) DestroyService(ctx context.Context, id string, deleteData bool) error {
	mu := d.lockFor(id)
	if !mu.TryLock() {
		return ErrBusy
	}
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.dbStopGrace+3*time.Minute)
	defer cancel()
	s, err := d.DB.ServiceByID(ctx, id)
	if err != nil {
		return err
	}
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	if err := d.DB.SetServiceState(ctx, s.ID, db.AppStopped, ""); err != nil {
		return err
	}
	args := []string{"down", "--remove-orphans", "--timeout", strconv.Itoa(int(d.dbStopGrace.Seconds()))}
	if deleteData {
		args = append(args, "--volumes")
	}
	if err := d.composeDown(ctx, r, s, args...); err != nil {
		return err
	}
	// Directories a container created inside the stack's directory belong
	// to root and may not be removable; what is left is only files.
	if err := r.RemoveAll(ctx, d.Cfg.AppDir(s.ID)); err != nil {
		d.Log.Warn("remove a deleted service's directory", "service", s.ID, "err", err)
	}
	if err := d.DB.DeleteService(ctx, s.ID); err != nil {
		return err
	}
	if err := d.SyncRoutes(ctx, server); err != nil && !errors.Is(err, ErrProxyDown) {
		d.Log.Warn("withdraw a deleted service's routes", "service", s.ID, "err", err)
	}
	return nil
}

// ServiceLogs writes the output of a stack's containers to w: the last
// tail lines of each, then what follows, until ctx ends or they all stop.
func (d *Deployer) ServiceLogs(ctx context.Context, r runner.Runner, id string, tail int, w io.Writer) error {
	cmd := d.composeCmd(id, "logs", "--follow", "--no-color", "--tail", strconv.Itoa(tail))
	cmd.Stdout, cmd.Stderr = w, w
	return r.Run(ctx, cmd)
}

// refreshService sets a service's status from the state of its containers:
// running when all are, degraded when only some are, exited when none is.
func (d *Deployer) refreshService(ctx context.Context, dk docker.Client, id string) error {
	listed, err := dk.List(ctx)
	if err != nil {
		return err
	}
	return d.DB.SetServiceStatusIf(ctx, id, serviceStatus(listed, id))
}

func serviceStatus(listed []docker.Listed, id string) string {
	total, up := 0, 0
	for _, c := range listed {
		if c.Kind != db.KindService || c.Resource != id {
			continue
		}
		// A container that ended well and was not restarted did a job and
		// is done: a migration, an init step. It says nothing about whether
		// the stack is up.
		if c.State == "exited" && strings.HasPrefix(c.Status, "Exited (0)") {
			continue
		}
		total++
		if c.State == "running" {
			up++
		}
	}
	switch {
	case total > 0 && up == total:
		return db.AppRunning
	case up > 0:
		return db.AppDegraded
	}
	return db.AppExited
}
