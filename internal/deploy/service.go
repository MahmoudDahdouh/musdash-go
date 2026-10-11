package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/compose"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// A service is a Compose stack. Its file is never loaded on the server as
// the person wrote it: it is loaded in a sandbox, checked, amended, and the
// result is what `docker compose up` is given. See the compose package.

// JobService is the job kind that deploys a service.
const JobService = "service"

// maxLayoutMembers bounds the services of a stack that are recorded for its
// pages.
const maxLayoutMembers = 200

// sandboxEnvFile holds a stack's variables for the sandboxed load.
const sandboxEnvFile = "sandbox.env"

// ServiceProject is the Compose project name of a service. Its containers
// are named after it: musdash-<id>-<compose service>-1.
func ServiceProject(id string) string { return "musdash-" + id }

type servicePayload struct {
	ID string `json:"id"`
	// DeploymentID is the row of this deployment. A job queued before
	// deployments were recorded has none.
	DeploymentID string `json:"deployment_id,omitempty"`
}

// EnqueueService queues a deployment of a service and returns its record.
// A service that is already deploying answers ErrBusy; again queues
// another deployment regardless, for a change the one in progress may not
// have seen. trigger is what asked for it.
func (d *Deployer) EnqueueService(ctx context.Context, s db.Service, trigger string, again bool) (db.ServiceDeployment, error) {
	// A stack from a repository may build images: it waits its turn with
	// the server's other builds.
	lock := "service:" + s.ID
	if s.FromGit() {
		lock = "build:" + s.ServerID
	}
	began, err := d.DB.BeginServiceDeploy(ctx, s.ID)
	if err != nil {
		return db.ServiceDeployment{}, err
	}
	if !began && !again {
		return db.ServiceDeployment{}, ErrBusy
	}
	dep, err := d.DB.CreateServiceDeployment(ctx, s.ID, trigger)
	if err == nil {
		_, err = d.Queue.Enqueue(ctx, JobService, servicePayload{ID: s.ID, DeploymentID: dep.ID}, jobs.WithLockKey(lock), jobs.WithMaxAttempts(1))
	}
	if err != nil {
		// Nothing will run it: the record and the service say so, where
		// they would otherwise wait for good.
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		reason := "the deployment could not be queued: " + err.Error()
		if dep.ID != "" {
			if ferr := d.DB.FinishServiceDeployment(rec, dep.ID, db.DeployFailed, reason); ferr != nil {
				d.Log.Error("record service failure", "service", s.ID, "err", ferr)
			}
		}
		if began {
			if serr := d.DB.SetServiceState(rec, s.ID, db.AppFailed, reason); serr != nil {
				d.Log.Error("record service failure", "service", s.ID, "err", serr)
			}
		}
	}
	return dep, err
}

func (d *Deployer) runServiceJob(ctx context.Context, raw []byte) (err error) {
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
	var dep db.ServiceDeployment
	if p.DeploymentID == "" {
		// Queued before deployments were recorded. What asked for it is
		// not known.
		dep, err = d.DB.CreateServiceDeployment(ctx, s.ID, "")
	} else {
		dep, err = d.DB.ServiceDeploymentByID(ctx, p.DeploymentID)
	}
	if err != nil {
		return jobs.Permanent(fmt.Errorf("deployment of service %s: %w", s.ID, err))
	}
	// How a deployment ended is written with a context of its own, made
	// when it is needed: the job's may be over by then, and the record must
	// not stay "running".
	recording := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	}
	// settled says that the record has its end, or waits for the job to
	// run again.
	settled := false
	finish := func(status, reason string) {
		settled = true
		rec, cancel := recording()
		defer cancel()
		if err := d.DB.FinishServiceDeployment(rec, dep.ID, status, reason); err != nil {
			d.Log.Error("record a service's deployment", "service", s.ID, "deployment", dep.ID, "err", err)
		}
		d.pruneServiceDeployments(rec, s.ID)
	}
	// Whatever ends the job otherwise, a write below that failed or a
	// panic, must not leave the record waiting and the service "deploying":
	// its page has no button while it is, and this job has one attempt. A
	// job that musdash's own shutdown ended runs again and is left alone.
	defer func() {
		if settled || ctx.Err() != nil {
			return
		}
		reason := "the deployment stopped before it had finished"
		if err != nil {
			reason += ": " + err.Error()
		}
		finish(db.DeployFailed, reason)
		rec, cancel := recording()
		defer cancel()
		if cur, cerr := d.DB.ServiceByID(rec, s.ID); cerr == nil && cur.Status == db.AppDeploying {
			if serr := d.DB.SetServiceState(rec, s.ID, d.serviceStatusAfterFailure(rec, s), reason); serr != nil {
				d.Log.Error("record service failure", "service", s.ID, "err", serr)
			}
		}
	}()
	switch s.Status {
	case db.AppStopped:
		// Stopped while this deployment waited its turn. The person's last
		// word was "stop".
		finish(db.DeployFailed, "the service was stopped before this deployment started")
		return nil
	case db.AppDeploying:
	default:
		// A deployment queued behind another one: the first has finished
		// and set a status of its own.
		if err := d.DB.SetServiceState(ctx, s.ID, db.AppDeploying, ""); err != nil {
			return err
		}
	}
	// The one log a service had before each deployment had its own.
	os.Remove(d.Cfg.ServiceLogPath(s.ID))
	log, err := OpenLog(d.Cfg.ServiceLogPath(dep.ID))
	if err != nil {
		finish(db.DeployFailed, err.Error())
		d.DB.SetServiceState(ctx, s.ID, db.AppFailed, err.Error())
		return jobs.Permanent(err)
	}
	defer log.Close()
	if err := d.DB.StartServiceDeployment(ctx, dep.ID); err != nil {
		return err
	}

	err = d.deployService(ctx, s, dep, log)
	rec, cancel := recording()
	defer cancel()
	// The record ends before the service's status says so: a page that
	// sees the service settle looks for a deployment that is over.
	if err == nil {
		log.Step("Deployed.")
		finish(db.DeploySuccess, "")
		return d.DB.SetServiceState(rec, s.ID, db.AppRunning, "")
	}
	if ctx.Err() != nil {
		// The job runs again, and until then the record waits as it does:
		// it does not go on naming the part it was at.
		log.Step("Interrupted: musdash is shutting down. The deployment will restart.")
		settled = true
		if rerr := d.DB.RequeueServiceDeployment(rec, dep.ID); rerr != nil {
			d.Log.Error("record a service's deployment", "service", s.ID, "deployment", dep.ID, "err", rerr)
		}
		return err
	}
	log.Step("Failed: %v", err)
	finish(db.DeployFailed, err.Error())
	if serr := d.DB.SetServiceState(rec, s.ID, d.serviceStatusAfterFailure(rec, s), err.Error()); serr != nil {
		d.Log.Error("record service failure", "service", s.ID, "err", serr)
	}
	return jobs.Permanent(err)
}

// pruneServiceDeployments drops a service's oldest finished deployments
// and their logs.
func (d *Deployer) pruneServiceDeployments(ctx context.Context, serviceID string) {
	ids, err := d.DB.PruneServiceDeployments(ctx, serviceID, keepDeployments)
	if err != nil {
		d.Log.Error("prune a service's deployments", "service", serviceID, "err", err)
		return
	}
	for _, id := range ids {
		os.Remove(d.Cfg.ServiceLogPath(id))
	}
}

// serviceStep records the part of a deployment that begins. It is what a
// page shows of a deployment in progress; one that could not be written
// costs the page a step and the deployment nothing.
func (d *Deployer) serviceStep(ctx context.Context, dep db.ServiceDeployment, step string) {
	if err := d.DB.SetServiceDeploymentStep(ctx, dep.ID, step); err != nil {
		d.Log.Warn("record a deployment's step", "deployment", dep.ID, "err", err)
	}
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

// fillShared returns a service's variables with the shared variables
// their values name filled in.
func (d *Deployer) fillShared(ctx context.Context, s db.Service, vars map[string]string) (map[string]string, error) {
	list := make([]db.EnvVar, 0, len(vars))
	for k, v := range vars {
		list = append(list, db.EnvVar{Key: k, Value: v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	if err := d.expandShared(ctx, s.EnvironmentID, s.ServerID, list); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, v := range list {
		out[v.Key] = v.Value
	}
	return out, nil
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
			return "", fmt.Errorf("%s has no domain: give %s one under Domains", v.Name, strings.ToLower(v.ID))
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
func (d *Deployer) composeCmd(r runner.Runner, id string, args ...string) runner.Cmd {
	dir := d.at(r).AppDir(id)
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

// takenPortRE finds the port of the server that Docker could not have, in
// the ways its versions say so: "failed to bind host port 0.0.0.0:8000/tcp",
// "Bind for 0.0.0.0:8000 failed", "listen tcp4 0.0.0.0:8000: bind".
var takenPortRE = regexp.MustCompile(`(?i)(?:bind host port|bind for|listen (?:tcp|udp)[46]?)\s+(?:\[[0-9a-f:]*\]|[0-9.]*):(\d{1,5})\b`)

// takenPort is the port a failed start names as in use, or 0 when the
// output names none that can be read.
func takenPort(output string) int {
	m := takenPortRE.FindStringSubmatch(output)
	if m == nil {
		return 0
	}
	port, _ := strconv.Atoi(m[1])
	return port
}

// deployService loads, checks and starts a service's stack, and says on
// the deployment's record which part it is at. The caller holds the
// service's lock.
func (d *Deployer) deployService(ctx context.Context, s db.Service, dep db.ServiceDeployment, log *Log) error {
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	dir := d.at(r).AppDir(s.ID)
	project := ServiceProject(s.ID)

	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		return err
	}
	// A stack from a repository: fetch it, and take the Compose file from
	// there. Its endpoints are only known now, so they get their generated
	// domains now.
	checkout, commit := "", ""
	var newHost func(string) (string, bool)
	if s.FromGit() {
		d.serviceStep(ctx, dep, db.ServiceStepClone)
		if checkout, commit, s, err = d.checkoutService(ctx, r, s, log); err != nil {
			return err
		}
		// For the list of deployments; one that could not be written costs
		// the list a commit and the deployment nothing.
		if err := d.DB.SetServiceDeploymentCommit(ctx, dep.ID, commit); err != nil {
			d.Log.Warn("record a deployment's commit", "deployment", dep.ID, "err", err)
		}
		newHost = func(string) (string, bool) { return GeneratedDomain(server), false }
	}

	// From here to the file's last check: what it asks for is found out,
	// filled in and held against what a stack may do.
	d.serviceStep(ctx, dep, db.ServiceStepRead)
	vars, err := d.ServiceVariables(s)
	if err != nil {
		return err
	}
	if err := d.PrepareService(ctx, s, vars, newHost); err != nil {
		return err
	}
	endpoints, err := d.DB.ListEndpoints(ctx, s.ID)
	if err != nil {
		return err
	}
	// Shared variables are filled in for this deployment only: what is
	// stored keeps naming them.
	filled, err := d.fillShared(ctx, s, vars)
	if err != nil {
		return err
	}
	env, err := serviceEnv(s.Compose, filled, endpoints)
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
	if checkout != "" {
		// The sandbox sees the checkout, read-only, and nothing else of
		// the server. Paths in the file are relative to where it lies.
		opt.Source, opt.Mount = nil, checkout
		opt.Dir = path.Join(checkout, path.Dir(s.ComposePath))
		opt.File = path.Base(s.ComposePath)
	}
	if err := d.fetchSandbox(ctx, server.ID, dk, opt.Image, log); err != nil {
		return err
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
	if err := d.checkEnvFiles(ctx, r, raw.EnvFiles(), checkout); err != nil {
		return err
	}
	resolved, err := compose.Config(ctx, r, opt)
	if err != nil {
		return fmt.Errorf("the Compose file could not be read: %w", err)
	}
	// Ports 80 and 443 of the server are the proxy's. A file written for
	// plain `docker compose` usually publishes its web server there, and
	// it is often a repository's file, which cannot be changed here: the
	// stack is deployed without that publication, and told how the port
	// is reached instead. Leaving a publication out exposes less, never
	// more; every other port a stack may not have is refused below.
	for _, u := range resolved.Unpublish(proxyPorts...) {
		if u.UDP {
			// HTTP/3, usually. The proxy carries no UDP, and a domain is
			// no way to it.
			log.Step("Port %d of the server is the proxy's, so %s is not published on it over UDP either.", u.Published, u.Service)
			continue
		}
		log.Step("Port %d of the server is the proxy's, so %s is not published on it. To reach port %d of %s from outside, give it a domain on the Domains tab.", u.Published, u.Service, u.Target, u.Service)
	}
	// A container name the file asks for is not given: Compose names the
	// containers under the stack's own project, so that two stacks cannot
	// claim one name. The name stays what the service answers to inside
	// the stack, which is what the file's other services rely on.
	for _, u := range resolved.Unname() {
		// Only a name that became an alias is known to be a plain name:
		// any other is the file's text, and is not written into the log.
		if u.Alias {
			log.Step("The container of %s is named by musdash, not %s as the file asks: no two stacks may claim one name. Inside the stack it still answers to %s.", u.Service, u.Name, u.Name)
		} else {
			log.Step("The container of %s is named by musdash, not as the file asks: no two stacks may claim one name.", u.Service)
		}
	}
	if err := resolved.Validate(compose.ValidateOptions{Dir: dir, BuildDir: checkout, Protected: []string{d.at(r).DataDir}, ValidPort: ValidPublicPort}); err != nil {
		return fmt.Errorf("the Compose file asks for things a service may not do:\n%w", err)
	}
	if checkout != "" {
		// What the stack mounts or builds from the checkout must be the
		// repository's own files, not links out of it.
		for _, p := range resolved.CheckoutPaths(checkout) {
			rel := strings.TrimPrefix(strings.TrimPrefix(p, checkout), "/")
			if !source.ValidRelPath(rel) {
				return fmt.Errorf("the Compose file names the path %q in the repository; use only letters, numbers, dots, hyphens, underscores and slashes in paths it mounts or builds from", rel)
			}
			if err := d.refuseSymlinks(ctx, r, checkout, rel); err != nil {
				return err
			}
		}
		// A mount inside one of the repository's needs its directory to be
		// in the repository: the files are mounted read-only, so Docker
		// cannot make it, and says so in words about a file system that
		// name neither the mount nor the reason.
		for _, n := range resolved.NestedInCheckout(checkout) {
			rel := strings.TrimPrefix(strings.TrimPrefix(n.Path, checkout), "/")
			out, err := r.Output(ctx, runner.Cmd{Name: "git", Args: []string{"-C", checkout, "ls-tree", "HEAD", "--", rel}, Env: gitEnv()})
			if err != nil {
				return fmt.Errorf("inspect the repository: %w", err)
			}
			if len(out) == 0 {
				return fmt.Errorf("service %s: the mount at %s lies inside %s, which is mounted from the repository. musdash mounts a repository's files read-only, and the repository has no %s for Docker to mount it on. Add it to the repository (a directory needs a file in it to be kept, such as .gitkeep), or build the files into the image instead of mounting them", n.Service, n.Target, n.Under, rel)
			}
		}
	}

	members := resolved.Services()
	override := compose.Override{ServiceID: s.ID, ReadOnlyUnder: checkout}
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

	// What the file holds, for the pages that offer its services: recorded
	// whether or not the stack then comes up.
	// It is what a server printed: each part is cut to size before it is
	// kept.
	layout := resolved.Layout()
	stack := make([]db.StackMember, 0, len(layout))
	for _, m := range layout {
		if len(stack) == maxLayoutMembers {
			break
		}
		if len(m.Image) > 200 {
			m.Image = m.Image[:200]
		}
		if len(m.Ports) > 16 {
			m.Ports = m.Ports[:16]
		}
		stack = append(stack, db.StackMember{Name: m.Name, Image: m.Image, Ports: m.Ports, Built: m.Built})
	}
	if err := d.DB.SetServiceLayout(ctx, s.ID, s.Compose, stack); err != nil {
		return err
	}

	// Where each endpoint leads: the ones the file names, as it was just
	// read, and the ones a person added, as they chose.
	type target struct {
		endpoint db.Endpoint
		service  string
		port     int
	}
	byName := map[string]db.Endpoint{}
	var targets []target
	for _, e := range endpoints {
		if !e.Manual {
			byName[e.Name] = e
			continue
		}
		// A domain for a service the file does not have would answer
		// nothing, and nothing would say why.
		known := false
		for _, m := range members {
			known = known || m == e.ComposeService
		}
		if !known {
			return fmt.Errorf("the domain %s leads to the service %q, which the Compose file does not have (it has %s). Remove the domain under Domains, or add it again for one of them", e.Host, e.ComposeService, strings.Join(members, ", "))
		}
		if !resolved.OwnNetwork(e.ComposeService) {
			return fmt.Errorf("the domain %s leads to the service %q, which shares another container's network and cannot publish a port of its own. Give the domain to the service whose network it uses", e.Host, e.ComposeService)
		}
		targets = append(targets, target{endpoint: e, service: e.ComposeService, port: e.Port})
	}
	for _, f := range found {
		e, ok := byName[f.Name]
		if !ok {
			return fmt.Errorf("no endpoint is recorded for %s", f.Name)
		}
		targets = append(targets, target{endpoint: e, service: f.Service, port: f.Port})
	}
	// Each service and port that has a domain gets one loopback port of the
	// server for the proxy, however many domains lead to it.
	assign := func(fresh bool) error {
		used, err := d.DB.UsedHostPorts(ctx, s.ServerID)
		if err != nil {
			return err
		}
		override.Publish = override.Publish[:0]
		chosen := map[string]int{}
		kept := map[int]bool{}
		key := func(t target) string { return t.service + ":" + strconv.Itoa(t.port) }
		// A port the stack already has is kept, so a redeployment does not
		// move what the proxy is sending requests to. One port goes to one
		// target: two endpoints that shared a target, and so a port, may
		// lead to different ones now that the file has changed.
		for _, t := range targets {
			if p := t.endpoint.HostPort; !fresh && p > 0 && chosen[key(t)] == 0 && !kept[p] {
				chosen[key(t)], kept[p] = p, true
			}
		}
		for _, t := range targets {
			hostPort := chosen[key(t)]
			if hostPort == 0 {
				if hostPort = pickPort(used); hostPort == 0 {
					return errors.New("no free host port is left on this server")
				}
				used[hostPort] = true
				chosen[key(t)] = hostPort
			}
			// A domain a person removed while this deployment was under way
			// is gone, and that is all: its port is published this once.
			if err := d.DB.SetEndpointTarget(ctx, t.endpoint.ID, t.service, t.port, hostPort); err != nil && !(t.endpoint.Manual && errors.Is(err, db.ErrNotFound)) {
				return err
			}
		}
		for _, t := range targets {
			if hostPort := chosen[key(t)]; hostPort > 0 {
				override.Publish = append(override.Publish, compose.Published{Service: t.service, Port: t.port, HostPort: hostPort})
				chosen[key(t)] = 0
			}
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
			d.serviceStep(ctx, dep, db.ServiceStepImages)
			log.Step("Pulling images")
			pull := d.composeCmd(r, s.ID, "pull", "--ignore-buildable")
			pullOut := &tail{limit: 1500}
			said := &unrepeated{w: teeWriter{log, pullOut}}
			pull.Stdout, pull.Stderr = said, said
			err := r.Run(ctx, pull)
			said.Flush()
			if err != nil {
				if ctx.Err() != nil {
					return err
				}
				// What Docker said is the reason: a name that does not
				// exist, a registry that wants a login, no room left.
				reason := lastOutput(pullOut.String(), 8)
				return fmt.Errorf("pull the images: %w. Docker's last output:\n%s%s", err, reason, refusedHint(reason))
			}
			if doc.Builds() {
				log.Step("Building images")
				build := d.composeCmd(r, s.ID, "build")
				watch := &lookupWatch{w: log}
				build.Stdout, build.Stderr = watch, watch
				buildCtx, cancel := context.WithTimeout(ctx, d.buildTimeout)
				err := r.Run(buildCtx, build)
				cancel()
				if err != nil {
					if errors.Is(buildCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
						return watch.explain("build", fmt.Errorf("stopped after %s without finishing", d.buildTimeout), server.Name)
					}
					return watch.explain("build the images", err, server.Name)
				}
			}
		}

		d.serviceStep(ctx, dep, db.ServiceStepStart)
		log.Step("Starting %s", strings.Join(members, ", "))
		upOut := &tail{limit: 4096}
		up := d.composeCmd(r, s.ID, "up", "--detach", "--remove-orphans", "--wait", "--wait-timeout", strconv.Itoa(int(d.serviceStartTimeout.Seconds())))
		said := &unrepeated{w: teeWriter{log, upOut}}
		up.Stdout, up.Stderr = said, said
		err = r.Run(ctx, up)
		said.Flush()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return err
		}
		if portTaken(upOut.String()) {
			// Whose port it was. A loopback port chosen here: another will
			// do. One the file publishes: no other will, and
			// starting again does worse than fail, since Docker then
			// starts the container it could not give the port, without
			// it, and the stack would be called deployed.
			// The loopback range is musdash's alone: no file may publish a
			// port in it, so a port taken there is one of those chosen here
			// or for something else musdash runs.
			port := takenPort(upOut.String())
			if ours := port == 0 || port >= portMin && port <= portMax; !ours {
				// The container that was made and not started goes, so
				// that the next deployment does not start it as it is.
				rm := d.composeCmd(r, s.ID, "rm", "--force")
				rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				r.Run(rmCtx, rm)
				cancel()
				what := "the Compose file publishes"
				if service := resolved.PublishedBy(port); service != "" {
					what += " for " + service
				}
				return fmt.Errorf("port %d of the server, which %s, is in use by something else on the server. Publish another port in the file, or take the line out and give the service a domain on the Domains tab", port, what)
			}
			if attempt < tries {
				log.Step("A port of the server was in use by something else; trying others")
				continue
			}
		}
		// What the containers said is what explains a stack that did not
		// come up.
		logs := &tail{limit: 2000}
		show := d.composeCmd(r, s.ID, "logs", "--tail", "25", "--no-color")
		show.Stdout, show.Stderr = logs, logs
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		r.Run(logCtx, show)
		cancel()
		return fmt.Errorf("the stack did not come up: %w. Its last output:\n%s", err, logs.String())
	}

	if checkout != "" {
		// The new stack runs from the new checkout. The ones before it,
		// also those of attempts that failed, are no longer mounted by
		// anything.
		if err := d.DB.SetServiceDeployed(ctx, s.ID, commit, path.Base(checkout)); err != nil {
			return err
		}
		d.pruneCheckouts(ctx, r, dir, path.Base(checkout))
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

// fetchSandbox makes sure the image Compose files are read with is on the
// server. One deployment a server looks and pulls at a time: the first
// stacks of a new server are often made together, and each would pull the
// image the others are pulling.
func (d *Deployer) fetchSandbox(ctx context.Context, serverID string, dk docker.Client, image string, log *Log) error {
	d.sandboxMu.Lock()
	if d.sandboxOf == nil {
		d.sandboxOf = map[string]*sync.Mutex{}
	}
	mu := d.sandboxOf[serverID]
	if mu == nil {
		mu = &sync.Mutex{}
		d.sandboxOf[serverID] = mu
	}
	d.sandboxMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	have, err := dk.HasImage(ctx, image)
	if err != nil || have {
		return err
	}
	log.Step("Fetching %s, which reads Compose files safely", image)
	return pullExplained(ctx, dk, image, log)
}

// maxComposeFile bounds a Compose file read from a repository.
const maxComposeFile = 512 << 10

// checkoutPrefix starts the name of a Git service's checkout directories.
const checkoutPrefix = "src-"

// pruneCheckouts removes every checkout of a service except the one named.
// It is called once a stack is up from that one, when no container can
// still have a file of another mounted.
func (d *Deployer) pruneCheckouts(ctx context.Context, r runner.Runner, serviceDir, keep string) {
	out, err := r.Output(ctx, runner.Cmd{Name: "ls", Args: []string{"-1", "--", serviceDir}})
	if err != nil {
		d.Log.Warn("list a service's checkouts", "dir", serviceDir, "err", err)
		return
	}
	for _, name := range strings.Fields(string(out)) {
		if !strings.HasPrefix(name, checkoutPrefix) || name == keep || !docker.ValidName(name) {
			continue
		}
		if err := r.RemoveAll(ctx, path.Join(serviceDir, name)); err != nil {
			d.Log.Warn("remove an old checkout", "dir", path.Join(serviceDir, name), "err", err)
		}
	}
}

// checkoutService fetches a Git service's repository and returns the
// checkout, its commit, and the service with the repository's Compose text.
//
// A stack may mount files of its checkout, so a checkout has to stay for as
// long as anything runs from it. Every deployment therefore clones into a
// directory of its own: the running containers keep their files until they
// are replaced, and after a deployment that failed, whichever containers
// it did or did not replace still have theirs. Old checkouts go once a
// stack is up from a newer one.
func (d *Deployer) checkoutService(ctx context.Context, r runner.Runner, s db.Service, log *Log) (string, string, db.Service, error) {
	fail := func(err error) (string, string, db.Service, error) { return "", "", s, err }
	repo, err := source.ParseRepo(s.RepoURL)
	if err != nil {
		return fail(err)
	}
	if !source.ValidBranch(s.Branch) {
		return fail(fmt.Errorf("%q is not a valid branch name", s.Branch))
	}
	if s.ComposePath == "" || !source.ValidRelPath(s.ComposePath) {
		return fail(fmt.Errorf("the Compose file path %q must be a path inside the repository", s.ComposePath))
	}
	checkout := path.Join(d.at(r).AppDir(s.ID), checkoutPrefix+secret.RandomID())
	// For the deploy key, away from anything a container can be given.
	keyDir := path.Join(d.at(r).WorkDir(), "service-"+s.ID)
	for _, p := range []string{checkout, keyDir} {
		if err := r.RemoveAll(ctx, p); err != nil {
			return fail(err)
		}
	}
	if err := r.MkdirAll(ctx, keyDir, 0o700); err != nil {
		return fail(err)
	}
	defer r.RemoveAll(context.WithoutCancel(ctx), keyDir)

	commit, err := d.clone(ctx, r, db.App{GitSourceID: s.GitSourceID, SSHKeyID: s.SSHKeyID}, repo, s.Branch, keyDir, checkout, log)
	if err != nil {
		return fail(err)
	}
	if err := d.refuseSymlinks(ctx, r, checkout, s.ComposePath); err != nil {
		return fail(err)
	}
	f, err := r.ReadFile(ctx, path.Join(checkout, s.ComposePath))
	if err != nil {
		return fail(fmt.Errorf("the repository has no file %s on the branch %s", s.ComposePath, s.Branch))
	}
	text, err := io.ReadAll(io.LimitReader(f, maxComposeFile+1))
	f.Close()
	if err != nil {
		return fail(err)
	}
	if len(text) > maxComposeFile {
		return fail(fmt.Errorf("%s is larger than 512 KB", s.ComposePath))
	}
	if err := d.DB.SetServiceComposeRead(ctx, s.ID, string(text)); err != nil {
		return fail(err)
	}
	s.Compose = string(text)
	return checkout, commit, s, nil
}

// GeneratedDomain builds an address that resolves to the server without any
// DNS setup: sslip.io answers <anything>.<ip>.sslip.io with <ip>.
func GeneratedDomain(server db.Server) string {
	return strings.ToLower(secret.RandomID()[:8]) + GeneratedSuffix(server)
}

// Generated reports whether a host is one of the shared wildcard DNS
// names, which resolve to the address written in them and need no record
// of anybody's.
func Generated(host string) bool {
	return strings.HasSuffix(host, ".sslip.io") || strings.HasSuffix(host, ".nip.io")
}

// GeneratedSuffix is what every generated address of a server ends with,
// from the dot on: a name in front of it makes one.
func GeneratedSuffix(server db.Server) string {
	ip := server.IP
	if net.ParseIP(ip) == nil {
		ip = "127.0.0.1"
	}
	// IPv6 addresses use dashes in sslip.io names.
	return "." + strings.ReplaceAll(ip, ":", "-") + ".sslip.io"
}

// lastOutput returns the last n lines of text that say something.
func lastOutput(text string, n int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
	if !lockSoon(mu) {
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

// proxyPorts are the ports of a server that the proxy answers on. No stack
// is published there.
var proxyPorts = []int{80, 443}

// ErrNoSuchContainer says a container that was named is not one of the
// stack's, as its server lists them now.
var ErrNoSuchContainer = errors.New("the stack has no such container")

// ErrStopped says a stack is stopped: nothing of it is to be started alone.
var ErrStopped = errors.New("the stack is stopped")

// ErrFinished says a container did its job and ended, as a migration does:
// it is run again by a deployment, not by a restart.
var ErrFinished = errors.New("the container finished")

// RestartServiceContainer restarts one container of a stack, as it was
// made: no change is applied, which is what a deployment is for. The name
// is only a choice among the containers the server lists for this stack,
// and the stack's lock is held, so it cannot run into a deployment or a
// stop.
func (d *Deployer) RestartServiceContainer(ctx context.Context, id, container string) error {
	mu := d.lockFor(id)
	if !lockSoon(mu) {
		return ErrBusy
	}
	defer mu.Unlock()
	// Long enough for a container whose file gives it minutes to stop;
	// most are given Docker's ten seconds.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	s, err := d.DB.ServiceByID(ctx, id)
	if err != nil {
		return err
	}
	switch s.Status {
	case db.AppDeploying:
		// Queued, and waiting for the lock this holds.
		return ErrBusy
	case db.AppStopped:
		return ErrStopped
	}
	server, err := d.DB.ServerByID(ctx, s.ServerID)
	if err != nil {
		return err
	}
	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	dk := docker.Client{R: r}
	list, err := dk.StackContainers(ctx, s.ID)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.Name != container {
			continue
		}
		if c.State == "exited" && strings.HasPrefix(c.Status, "Exited (0)") {
			return ErrFinished
		}
		return dk.Restart(ctx, container)
	}
	return ErrNoSuchContainer
}

// composeDown runs a Compose command that winds a project down. A project
// that was never started has no resolved file; then there is nothing to do.
func (d *Deployer) composeDown(ctx context.Context, r runner.Runner, s db.Service, args ...string) error {
	file, err := r.ReadFile(ctx, filepath.Join(d.at(r).AppDir(s.ID), compose.ResolvedFile))
	if err != nil {
		return nil
	}
	file.Close()
	out := &tail{limit: 1500}
	cmd := d.composeCmd(r, s.ID, args...)
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
	if !lockSoon(mu) {
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
	if s.FromGit() {
		// The images built for it are of no use to anything else.
		dk := docker.Client{R: r}
		for _, member := range s.MemberNames() {
			image := ServiceProject(s.ID) + "-" + member
			if err := dk.RemoveImage(ctx, image); err != nil {
				d.Log.Warn("remove a deleted service's image", "image", image, "err", err)
			}
		}
	}
	// Directories a container created inside the stack's directory belong
	// to root and may not be removable; what is left is only files.
	if err := r.RemoveAll(ctx, d.at(r).AppDir(s.ID)); err != nil {
		d.Log.Warn("remove a deleted service's directory", "service", s.ID, "err", err)
	}
	// The deployments' records go with the service; their logs are found
	// through them, so they are listed first.
	logs, err := d.DB.ServiceDeploymentIDs(ctx, s.ID)
	if err != nil {
		return err
	}
	if err := d.DB.DeleteService(ctx, s.ID); err != nil {
		return err
	}
	// And the one log a service had before each deployment had its own.
	for _, id := range append(logs, s.ID) {
		os.Remove(d.Cfg.ServiceLogPath(id))
	}
	if err := d.SyncRoutes(ctx, server); err != nil && !errors.Is(err, ErrProxyDown) {
		d.Log.Warn("withdraw a deleted service's routes", "service", s.ID, "err", err)
	}
	d.dropNetwork(ctx, r, s.EnvironmentID, s.ServerID)
	return nil
}

// ServiceLogs writes the output of a stack's containers to w: the last
// tail lines of each, then what follows, until ctx ends or they all stop.
//
// With only, it is the output of that one Compose service: the caller has
// found the name among the stack's own.
func (d *Deployer) ServiceLogs(ctx context.Context, r runner.Runner, id string, tail int, w io.Writer, only string) error {
	args := []string{"logs", "--follow", "--no-color", "--tail", strconv.Itoa(tail)}
	if only != "" {
		args = append(args, "--", only)
	}
	cmd := d.composeCmd(r, id, args...)
	cmd.Stdout, cmd.Stderr = w, w
	return r.Run(ctx, cmd)
}

// refreshService sets a service's status from the state of its containers:
// running when all are, degraded when only some are, exited when none is.
func (d *Deployer) refreshService(ctx context.Context, serverID string, dk docker.Client, id string) error {
	listed, err := dk.List(ctx)
	if err != nil {
		return err
	}
	return d.DB.SetServiceStatusIf(ctx, serverID, id, serviceStatus(listed, id))
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

// checkEnvFiles refuses an env_file that is not a file of the stack's own
// repository. The sandbox that reads the Compose file sees the checkout and
// nothing else of the server, so any other path is read from the sandbox's
// own image, if it happens to be there: nothing of the server's leaks, but
// the containers get variables nobody wrote. A link in the repository
// would be followed to the same place.
func (d *Deployer) checkEnvFiles(ctx context.Context, r runner.Runner, files []compose.EnvFile, checkout string) error {
	for _, f := range files {
		if checkout == "" {
			return fmt.Errorf("service %s: env_file reads variables from a file, and a stack that was pasted has no files. Put them in the stack's Variables instead", f.Service)
		}
		// A variable in the path is filled in after this was looked at.
		if strings.Contains(f.Path, "$") {
			return fmt.Errorf("service %s: the env_file path %q uses a variable; write the path out", f.Service, path.Base(f.Path))
		}
		clean := path.Clean(f.Path)
		if !path.IsAbs(clean) || !strings.HasPrefix(clean, checkout+"/") {
			return fmt.Errorf("service %s: the env_file %q is not a file of the repository. Use a path inside the repository, or put the variables in the stack's Variables", f.Service, strings.TrimPrefix(clean, checkout+"/"))
		}
		rel := strings.TrimPrefix(clean, checkout+"/")
		if !source.ValidRelPath(rel) {
			return fmt.Errorf("the Compose file names the env_file %q in the repository; use only letters, numbers, dots, hyphens, underscores and slashes in its path", rel)
		}
		if err := d.refuseSymlinks(ctx, r, checkout, rel); err != nil {
			return err
		}
	}
	return nil
}
