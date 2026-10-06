package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

const (
	// settingCleanupDay is the day (UTC) the clean-up last ran.
	settingCleanupDay = "cleanup_day"
	// cleanupHour is the hour (UTC) from which the day's clean-up runs.
	cleanupHour = 3
	// diskWarnPercent is how full Docker's disk may get before people are
	// told.
	diskWarnPercent = 85
	// Build cache older than this is removed.
	buildCacheAge = "168h"
	// buildCacheKeep is how much build cache a server keeps whatever its
	// age. A week of builds on a small server is several gigabytes, on a
	// disk that has twenty; this much keeps the layers of the last builds,
	// which is what makes the next one fast.
	buildCacheKeep = "2GB"
)

type cleanupPayload struct {
	ServerID string `json:"server_id"`
}

// cleanupIfDue queues the day's clean-up for every server, once a day.
func (o *Ops) cleanupIfDue(ctx context.Context, now time.Time) {
	now = now.UTC()
	if now.Hour() < cleanupHour {
		return
	}
	today := now.Format("2006-01-02")
	last, err := o.DB.Setting(ctx, settingCleanupDay)
	if err != nil || last == today {
		return
	}
	// Stored first: a clean-up that cannot be queued is not tried again
	// every minute of the day.
	if err := o.DB.SetSetting(ctx, settingCleanupDay, today); err != nil {
		o.Log.Error("record clean-up day", "err", err)
		return
	}
	servers, err := o.DB.AllServers(ctx)
	if err != nil {
		o.Log.Error("list servers for clean-up", "err", err)
		return
	}
	for _, s := range servers {
		if err := o.EnqueueCleanup(ctx, s.ID); err != nil {
			o.Log.Error("queue clean-up", "server", s.ID, "err", err)
		}
	}
}

// EnqueueCleanup queues a clean-up of one server. It shares the server's
// build lock: removing build cache under a running build would only make
// that build slower.
func (o *Ops) EnqueueCleanup(ctx context.Context, serverID string) error {
	_, err := o.Queue.Enqueue(ctx, JobCleanup, cleanupPayload{ServerID: serverID}, jobs.WithLockKey("build:"+serverID), jobs.WithMaxAttempts(1))
	return err
}

func (o *Ops) runCleanupJob(ctx context.Context, raw []byte) error {
	var p cleanupPayload
	if err := decode(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	server, err := o.DB.ServerByID(ctx, p.ServerID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := o.Runners.Runner(ctx, server)
	if err != nil {
		return jobs.Permanent(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// Only what nothing refers to. `image prune --all` would also take the
	// image of every stopped app and database, and the images kept to roll
	// back to.
	o.dockerCleanup(ctx, server, r, "image", "prune", "--force")
	o.dockerCleanup(ctx, server, r, "builder", "prune", "--force", "--filter", "until="+buildCacheAge)
	o.trimBuildCache(ctx, server, r)
	o.removeOrphans(ctx, server, docker.Client{R: r})
	o.removeNetworks(ctx, server, docker.Client{R: r})

	used, ok := diskUsed(ctx, r)
	if ok && used >= diskWarnPercent {
		// Build cache costs only the time of the next build. A full disk
		// stops deployments, and databases with them.
		o.dockerCleanup(ctx, server, r, "builder", "prune", "--force")
		used, ok = diskUsed(ctx, r)
	}
	if ok && used >= diskWarnPercent {
		o.Notify(server.TeamID, notify.Event{
			Kind: notify.EventDisk, At: time.Now(),
			Title: fmt.Sprintf("The disk of %s is %d%% full", server.Name, used),
			Body:  "Docker keeps images, volumes and build cache there. Unused images and the build cache were just removed; what is left is in use or is data.",
			URL:   "/servers",
		})
	}
	return nil
}

// dockerCleanup runs one docker command of the clean-up and logs what it
// freed. A command that fails is logged and the clean-up goes on.
func (o *Ops) dockerCleanup(ctx context.Context, server db.Server, r runner.Runner, args ...string) error {
	out, err := r.Output(ctx, runner.Cmd{Name: "docker", Args: args})
	if err != nil {
		o.Log.Warn("docker clean-up", "server", server.Name, "command", strings.Join(args[:2], " "), "err", err)
		return err
	}
	o.Log.Info("docker clean-up", "server", server.Name, "command", strings.Join(args[:2], " "), "result", lastLine(string(out)))
	return nil
}

// trimBuildCache removes build cache beyond buildCacheKeep, the least
// recently used first. Age alone does not bound it: a busy week of builds
// fills a small disk before any of it is a week old.
func (o *Ops) trimBuildCache(ctx context.Context, server db.Server, r runner.Runner) {
	err := o.dockerCleanup(ctx, server, r, "builder", "prune", "--force", "--max-used-space", buildCacheKeep)
	// The flag had another name before Docker 28, which newer ones still
	// take but say they will drop.
	var exit *runner.ExitError
	if errors.As(err, &exit) && strings.Contains(exit.Stderr, "unknown flag") {
		o.dockerCleanup(ctx, server, r, "builder", "prune", "--force", "--keep-storage", buildCacheKeep)
	}
}

// removeOrphans removes stopped containers whose app, database or service
// no longer exists: what a delete that was interrupted left behind.
func (o *Ops) removeOrphans(ctx context.Context, server db.Server, dk docker.Client) {
	listed, err := dk.List(ctx)
	if err != nil {
		o.Log.Warn("list containers for clean-up", "server", server.Name, "err", err)
		return
	}
	for _, c := range listed {
		if c.State == "running" || c.State == "restarting" || c.State == "paused" || c.Resource == "" {
			continue
		}
		var err error
		switch c.Kind {
		case db.KindApp:
			_, err = o.DB.AppByID(ctx, c.Resource)
		case db.KindDatabase:
			_, err = o.DB.DatabaseByID(ctx, c.Resource)
		case db.KindService:
			_, err = o.DB.ServiceByID(ctx, c.Resource)
		default:
			continue
		}
		if !errors.Is(err, db.ErrNotFound) {
			continue // it exists, or we could not tell
		}
		if err := dk.Remove(ctx, c.Name); err != nil {
			o.Log.Warn("remove a leftover container", "container", c.Name, "err", err)
			continue
		}
		o.Log.Info("removed a leftover container", "server", server.Name, "container", c.Name)
	}
}

// environmentNetworkRE is the name of an environment's network: the prefix
// and an id of ours. What a server lists is the server's word, and only a
// name of exactly this shape is asked about or removed.
var environmentNetworkRE = regexp.MustCompile(`^musdash-([a-z][a-z2-7]{11})$`)

// removeNetworks removes the networks of environments that no longer exist:
// what versions before the network was removed with its last resource left
// behind, and what a delete could not remove because the server did not
// answer.
//
// A network is only removed when nothing at all is attached to it. Docker
// would remove one that stopped containers are attached to, and they could
// never be started again; and on a Docker shared with another installation
// of musdash, that one's networks look exactly like leftovers from here.
func (o *Ops) removeNetworks(ctx context.Context, server db.Server, dk docker.Client) {
	names, err := dk.Networks(ctx)
	if err != nil {
		o.Log.Warn("list networks for clean-up", "server", server.Name, "err", err)
		return
	}
	for _, name := range names {
		m := environmentNetworkRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if exists, err := o.DB.EnvironmentExists(ctx, m[1]); err != nil || exists {
			continue // it exists, or we could not tell
		}
		if used, err := dk.NetworkInUse(ctx, name); err != nil || used {
			continue
		}
		if err := dk.RemoveNetwork(ctx, name); err != nil {
			o.Log.Warn("remove a leftover network", "network", name, "err", err)
			continue
		}
		o.Log.Info("removed a leftover network", "server", server.Name, "network", name)
	}
}

// diskUsed is how full the filesystem holding Docker's data is, in percent.
// It reports false where that cannot be read, such as a Docker that runs in
// a virtual machine of its own.
func diskUsed(ctx context.Context, r runner.Runner) (int, bool) {
	out, err := r.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"info", "--format", "{{.DockerRootDir}}"}})
	if err != nil {
		return 0, false
	}
	root := strings.TrimSpace(string(out))
	if !strings.HasPrefix(root, "/") {
		return 0, false
	}
	out, err = r.Output(ctx, runner.Cmd{Name: "df", Args: []string{"-P", "-k", "--", root}})
	if err != nil {
		return 0, false
	}
	return parseDF(string(out))
}

// parseDF reads the capacity column of `df -P`.
func parseDF(out string) (int, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, false
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(fields[4], "%"))
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
