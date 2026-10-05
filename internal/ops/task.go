package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// errNoContainer is why a task did not run on an app that is not running.
var errNoContainer = errors.New("the app has no running container to run the command in")

// KeepTaskRuns is how many runs of a task stay in its history.
const KeepTaskRuns = 50

// MaxTaskCommand bounds a task's command.
const MaxTaskCommand = 2000

type taskPayload struct {
	RunID string `json:"run_id"`
}

// EnqueueTask records a run of a task and queues the job that performs it.
// Runs of one task never overlap.
func (o *Ops) EnqueueTask(ctx context.Context, t db.Task, trigger string) (db.TaskRun, error) {
	run, err := o.DB.CreateTaskRun(ctx, t.ID, trigger)
	if err != nil {
		return run, err
	}
	_, err = o.Queue.Enqueue(ctx, JobTask, taskPayload{RunID: run.ID}, jobs.WithLockKey("task:"+t.ID), jobs.WithMaxAttempts(1))
	if err != nil {
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		o.DB.FinishTaskRun(rec, run.ID, db.RunFailed, 0, "could not be queued: "+err.Error())
	}
	return run, err
}

func (o *Ops) runTaskJob(ctx context.Context, raw []byte) error {
	var p taskPayload
	if err := decode(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	run, err := o.DB.TaskRunByID(ctx, p.RunID)
	if errors.Is(err, db.ErrNotFound) {
		return nil // the task or its app was deleted while this waited
	}
	if err != nil {
		return err
	}
	if run.Status != db.RunRunning {
		return nil
	}
	var app db.App
	task, err := o.DB.TaskByID(ctx, run.TaskID)
	if err == nil {
		app, err = o.DB.AppByID(ctx, task.AppID)
	}
	code := 0
	if err == nil {
		code, err = o.runTask(ctx, app, task, run)
	}

	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	status, text := db.RunSuccess, ""
	switch {
	case err == nil:
	case ctx.Err() != nil:
		status, text = db.RunFailed, "interrupted: musdash was stopping"
	default:
		status, text = db.RunFailed, err.Error()
	}
	if ferr := o.DB.FinishTaskRun(rec, run.ID, status, code, text); ferr != nil {
		o.Log.Error("record task run", "run", run.ID, "err", ferr)
	}
	if old, perr := o.DB.PruneTaskRuns(rec, task.ID, KeepTaskRuns); perr == nil {
		for _, id := range old {
			os.Remove(o.Cfg.TaskLogPath(id))
		}
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	if errors.Is(err, errNoContainer) || app.ID == "" {
		// The app is stopped, which its people know: a task that fires
		// every minute would otherwise tell them every minute.
		return jobs.Permanent(err)
	}
	o.NotifyEnvironment(app.EnvironmentID, notify.Event{
		Kind: notify.EventTask, At: time.Now(),
		Title: "Scheduled task " + task.Name + " of " + app.Name + " failed",
		Body:  text,
		URL:   "/apps/" + app.ID + "/tasks/" + task.ID,
	})
	return jobs.Permanent(err)
}

// runTask runs the command in the app's serving container and returns its
// exit code. The command's output goes to the run's log file.
//
// Stopping a run that takes too long stops waiting for it; Docker does not
// end a process started with `docker exec` when its client goes away, so
// the command itself may carry on inside the container.
func (o *Ops) runTask(ctx context.Context, app db.App, task db.Task, run db.TaskRun) (int, error) {
	if app.Container == "" || app.Status != db.AppRunning {
		return 0, errNoContainer
	}
	if !docker.ValidName(app.Container) {
		return 0, fmt.Errorf("bad container name %q", app.Container)
	}
	server, err := o.DB.ServerByID(ctx, app.ServerID)
	if err != nil {
		return 0, err
	}
	r, err := o.Runners.Runner(ctx, server)
	if err != nil {
		return 0, err
	}
	log, err := deploy.OpenLog(o.Cfg.TaskLogPath(run.ID))
	if err != nil {
		return 0, err
	}
	defer log.Close()

	ctx, cancel := context.WithTimeout(ctx, o.taskTimeout)
	defer cancel()
	err = r.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", app.Container, "sh", "-c", task.Command}, Stdout: log, Stderr: log})
	if err == nil {
		return 0, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 0, fmt.Errorf("stopped waiting after %s", o.taskTimeout)
	}
	var exit *runner.ExitError
	if errors.As(err, &exit) {
		return exit.Code, fmt.Errorf("the command ended with status %d", exit.Code)
	}
	return 0, err
}
