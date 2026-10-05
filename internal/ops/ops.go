// Package ops is what musdash does on its own: backups and commands on a
// schedule, cleaning up after Docker, and telling people what happened.
package ops

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/backup"
	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/cron"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// Job kinds.
const (
	JobBackup  = "backup"
	JobRestore = "restore"
	JobTask    = "task"
	JobCleanup = "cleanup"
)

// Ops runs the scheduled work.
type Ops struct {
	DB      *db.DB
	Box     *secret.Box
	Queue   *jobs.Queue
	Runners deploy.Runners
	Cfg     *config.Config
	Log     *slog.Logger
	Sender  notify.Sender
	// S3Lookup and S3AllowLocal are passed to every storage that is
	// opened. Tests set them.
	S3Lookup     func(ctx context.Context, host string) ([]netip.Addr, error)
	S3AllowLocal bool

	// A dump or a restore of a large database takes hours; a scheduled
	// command should not.
	backupTimeout time.Duration
	taskTimeout   time.Duration
	// events waits for the one goroutine that sends notifications, so that
	// a slow webhook never holds up the job that had something to say.
	events chan outgoing
	// recent is when each "stopped unexpectedly" was last sent. A container
	// that keeps crashing is restarted by Docker every few seconds; people
	// are told once in a while, not every time. Only the sending goroutine
	// touches it.
	recent   map[string]time.Time
	recentMu sync.Mutex
}

// New returns an Ops with the usual limits.
func New(d *db.DB, box *secret.Box, queue *jobs.Queue, runners deploy.Runners, cfg *config.Config, log *slog.Logger) *Ops {
	return &Ops{
		DB: d, Box: box, Queue: queue, Runners: runners, Cfg: cfg, Log: log,
		backupTimeout: 6 * time.Hour,
		taskTimeout:   time.Hour,
		events:        make(chan outgoing, 64),
		recent:        map[string]time.Time{},
	}
}

// Register installs the job handlers.
func (o *Ops) Register() {
	o.Queue.Register(JobBackup, o.runBackupJob)
	o.Queue.Register(JobRestore, o.runRestoreJob)
	o.Queue.Register(JobTask, o.runTaskJob)
	o.Queue.Register(JobCleanup, o.runCleanupJob)
	// A command or a dump may run for hours. Together they never take the
	// last worker, so a deployment is not kept waiting by them.
	o.Queue.Background(JobBackup, JobRestore, JobTask, JobCleanup)
}

// NextRun is when a schedule next fires after the given time, in Unix
// seconds; 0 when it never does or cannot be read.
func NextRun(schedule string, after time.Time) int64 {
	s, err := cron.Parse(schedule)
	if err != nil {
		return 0
	}
	next := s.Next(after)
	if next.IsZero() {
		return 0
	}
	return next.Unix()
}

// Recover puts right what a process that died left behind. It must run
// before the job queue starts: a requeued job looks at its record to see
// whether there is still something to do.
func (o *Ops) Recover(ctx context.Context) error {
	if err := o.DB.FailRunningBackups(ctx, "musdash was restarted while this was running"); err != nil {
		return err
	}
	// Storage keys of an upload that was cut short.
	if left, err := filepath.Glob(filepath.Join(o.Cfg.BackupDir(), "*", backup.EnvFilePrefix+"*")); err == nil {
		for _, p := range left {
			os.Remove(p)
		}
	}
	return o.settlePending(ctx, time.Now())
}

// settlePending gives a next run to schedules that were claimed but never
// got one: the tick that claimed them died, or could not store it.
func (o *Ops) settlePending(ctx context.Context, now time.Time) error {
	backups, tasks, err := o.DB.PendingSchedules(ctx)
	if err != nil {
		return err
	}
	for _, m := range backups {
		if err := o.DB.SetBackupNextRun(ctx, m.ID, NextRun(m.Schedule, now)); err != nil {
			return err
		}
	}
	for _, m := range tasks {
		if err := o.DB.SetTaskNextRun(ctx, m.ID, NextRun(m.Schedule, now)); err != nil {
			return err
		}
	}
	return nil
}

// Run sends notifications and fires schedules until ctx ends.
func (o *Ops) Run(ctx context.Context) {
	go o.deliverLoop(ctx)
	for {
		// Just after the top of the minute, so a schedule for 03:00 is seen
		// as due by the tick that starts at 03:00.
		now := time.Now()
		wait := time.Until(now.Truncate(time.Minute).Add(time.Minute + time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		o.Tick(ctx, time.Now())
	}
}

// Tick queues a job for every schedule that has come due. A schedule whose
// time passed while musdash was not running fires once when it is back,
// however many of its times were missed, and moves on to its next one.
func (o *Ops) Tick(ctx context.Context, now time.Time) {
	// Only this goroutine claims, so anything still claimed here is left
	// over from a tick that could not finish its bookkeeping.
	if err := o.settlePending(ctx, now); err != nil {
		o.Log.Error("settle claimed schedules", "err", err)
	}
	backups, err := o.DB.ClaimDueBackups(ctx, now.Unix())
	if err != nil {
		o.Log.Error("claim due backups", "err", err)
	}
	for _, m := range backups {
		if busy, err := o.DB.BackupRunning(ctx, m.ID); err != nil {
			o.Log.Error("scheduled backup", "database", m.ID, "err", err)
		} else if busy {
			o.Log.Warn("scheduled backup skipped: the previous one is still running", "database", m.ID)
		} else if _, err := o.EnqueueBackup(ctx, m.ID, db.TriggerSchedule); err != nil {
			o.Log.Error("queue scheduled backup", "database", m.ID, "err", err)
		}
		if err := o.DB.SetBackupNextRun(ctx, m.ID, NextRun(m.Schedule, now)); err != nil {
			o.Log.Error("store next backup time", "database", m.ID, "err", err)
		}
	}

	tasks, err := o.DB.ClaimDueTasks(ctx, now.Unix())
	if err != nil {
		o.Log.Error("claim due tasks", "err", err)
	}
	for _, m := range tasks {
		if busy, err := o.DB.TaskRunning(ctx, m.ID); err != nil {
			o.Log.Error("scheduled task", "task", m.ID, "err", err)
		} else if busy {
			// A command that takes longer than its interval would
			// otherwise pile up runs without end.
			o.Log.Warn("scheduled task skipped: the previous run is still going", "task", m.ID)
		} else if t, err := o.DB.TaskByID(ctx, m.ID); err != nil {
			o.Log.Error("scheduled task", "task", m.ID, "err", err)
		} else if _, err := o.EnqueueTask(ctx, t, db.TriggerSchedule); err != nil {
			o.Log.Error("queue scheduled task", "task", m.ID, "err", err)
		}
		if err := o.DB.SetTaskNextRun(ctx, m.ID, NextRun(m.Schedule, now)); err != nil {
			o.Log.Error("store next task time", "task", m.ID, "err", err)
		}
	}

	o.cleanupIfDue(ctx, now)
}
