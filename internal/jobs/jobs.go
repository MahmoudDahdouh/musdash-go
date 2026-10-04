// Package jobs is a small persistent job queue backed by the jobs table.
//
// A single dispatcher goroutine claims work and hands it to at most Workers
// concurrent handlers, so an idle queue costs one sleeping goroutine. Jobs
// that share a lock key never run at the same time, which is how builds are
// limited to one per server.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// Handler runs one job. Returning an error retries the job with back-off
// until its attempts are used up; wrap the error with Permanent to fail it
// at once.
type Handler func(ctx context.Context, payload []byte) error

// Status values stored in jobs.status.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks err as not worth retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// Queue dispatches jobs to registered handlers.
type Queue struct {
	db      *sql.DB
	log     *slog.Logger
	workers int

	// Tunables, overridden by tests.
	pollEvery time.Duration
	backoff   func(attempt int) time.Duration
	keepDone  time.Duration

	mu       sync.Mutex
	handlers map[string]Handler

	started bool
	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	running sync.WaitGroup
	cancel  context.CancelFunc
}

// New returns a queue that runs up to workers jobs at once.
func New(db *sql.DB, log *slog.Logger, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}
	return &Queue{
		db:        db,
		log:       log,
		workers:   workers,
		pollEvery: 5 * time.Second,
		backoff:   defaultBackoff,
		keepDone:  7 * 24 * time.Hour,
		handlers:  make(map[string]Handler),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

// defaultBackoff waits 5 s, 20 s, 80 s, … capped at 10 minutes.
func defaultBackoff(attempt int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempt && d < 10*time.Minute; i++ {
		d *= 4
	}
	return min(d, 10*time.Minute)
}

// Register sets the handler for a job kind. Call it before Start.
func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

type options struct {
	lockKey     string
	runAfter    time.Time
	maxAttempts int
}

// Option adjusts one Enqueue call.
type Option func(*options)

// WithLockKey makes the job mutually exclusive with every other job that
// carries the same key.
func WithLockKey(key string) Option { return func(o *options) { o.lockKey = key } }

// WithRunAfter delays the job until t.
func WithRunAfter(t time.Time) Option { return func(o *options) { o.runAfter = t } }

// WithMaxAttempts sets how many times the job may run before it is failed.
func WithMaxAttempts(n int) Option { return func(o *options) { o.maxAttempts = n } }

// Enqueue stores a job and returns its id. The payload is encoded as JSON.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opts ...Option) (string, error) {
	o := options{runAfter: time.Now(), maxAttempts: 3}
	for _, opt := range opts {
		opt(&o)
	}
	if o.maxAttempts < 1 {
		o.maxAttempts = 1
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode %s payload: %w", kind, err)
	}
	id := secret.RandomID()
	_, err = q.db.ExecContext(ctx, `INSERT INTO jobs (id, kind, payload, max_attempts, lock_key, run_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, kind, string(body), o.maxAttempts, o.lockKey, o.runAfter.Unix(), time.Now().Unix())
	if err != nil {
		return "", err
	}
	q.poke()
	return id, nil
}

// poke wakes the dispatcher without blocking.
func (q *Queue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Start requeues jobs a previous process left running, then begins
// dispatching. It returns immediately.
func (q *Queue) Start(ctx context.Context) error {
	// A job still marked running at start belonged to a process that died.
	// One that has used all its attempts is failed rather than requeued:
	// a job that takes the process down must not do so on every restart.
	if _, err := q.db.ExecContext(ctx, `UPDATE jobs SET status = ?, finished_at = ?, last_error = ?
		WHERE status = ? AND attempts >= max_attempts`, StatusFailed, time.Now().Unix(), "the process stopped while this job was running", StatusRunning); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = 0 WHERE status = ?`, StatusQueued, StatusRunning); err != nil {
		return err
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	q.cancel = cancel
	q.started = true
	go q.dispatch(jobCtx)
	return nil
}

// Stop stops claiming new jobs and waits for running ones. When ctx expires
// first, the running handlers' contexts are cancelled and Stop waits for
// them to return.
func (q *Queue) Stop(ctx context.Context) {
	if !q.started {
		return
	}
	close(q.stop)
	<-q.stopped
	done := make(chan struct{})
	go func() { q.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		q.cancel()
		<-done
	}
	q.cancel()
}

type job struct {
	id          string
	kind        string
	payload     string
	attempts    int
	maxAttempts int
}

func (q *Queue) dispatch(ctx context.Context) {
	defer close(q.stopped)
	slots := make(chan struct{}, q.workers)
	finished := make(chan struct{}, q.workers)
	ticker := time.NewTicker(q.pollEvery)
	defer ticker.Stop()
	lastPrune := time.Time{}

	for {
		// Claim until the workers are busy or nothing is ready. Once Stop
		// has been called nothing new is claimed.
	claim:
		for {
			select {
			case <-q.stop:
				return
			default:
			}
			select {
			case slots <- struct{}{}:
			default:
				break claim
			}
			j, ok, err := q.claim(ctx)
			if err != nil {
				q.log.Error("claim job", "err", err)
			}
			if !ok {
				<-slots
				break claim
			}
			q.running.Add(1)
			go func() {
				defer func() {
					<-slots
					q.running.Done()
					select {
					case finished <- struct{}{}:
					default:
					}
				}()
				q.run(ctx, j)
			}()
		}

		if time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			q.prune(ctx)
		}

		select {
		case <-q.stop:
			return
		case <-q.wake:
		case <-finished:
		case <-ticker.C:
		}
	}
}

// claim atomically takes the oldest ready job whose lock key is free.
func (q *Queue) claim(ctx context.Context) (job, bool, error) {
	nowUnix := time.Now().Unix()
	var j job
	err := q.db.QueryRowContext(ctx, `
		UPDATE jobs SET status = 'running', started_at = ?, attempts = attempts + 1
		WHERE id = (
			SELECT j.id FROM jobs j
			WHERE j.status = 'queued' AND j.run_after <= ?
			  AND (j.lock_key = '' OR NOT EXISTS (
			        SELECT 1 FROM jobs r WHERE r.status = 'running' AND r.lock_key = j.lock_key))
			ORDER BY j.run_after, j.created_at, j.id
			LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts`, nowUnix, nowUnix).
		Scan(&j.id, &j.kind, &j.payload, &j.attempts, &j.maxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return job{}, false, nil
	}
	if err != nil {
		return job{}, false, err
	}
	return j, true, nil
}

func (q *Queue) run(ctx context.Context, j job) {
	q.mu.Lock()
	h := q.handlers[j.kind]
	q.mu.Unlock()

	var err error
	if h == nil {
		err = Permanent(fmt.Errorf("no handler registered for job kind %q", j.kind))
	} else {
		err = safely(ctx, h, []byte(j.payload))
	}

	// The outcome must be recorded even when the job's context was cancelled
	// by shutdown.
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	nowUnix := time.Now().Unix()

	switch {
	case err == nil:
		_, err = q.db.ExecContext(rec, `UPDATE jobs SET status = ?, finished_at = ?, last_error = '' WHERE id = ?`, StatusDone, nowUnix, j.id)
	case ctx.Err() != nil:
		// Interrupted by shutdown, not by its own fault: put it back without
		// spending an attempt, so it runs again after the restart.
		_, err = q.db.ExecContext(rec, `UPDATE jobs SET status = ?, started_at = 0, attempts = attempts - 1 WHERE id = ?`, StatusQueued, j.id)
	case errors.As(err, new(permanentError)) || j.attempts >= j.maxAttempts:
		q.log.Warn("job failed", "kind", j.kind, "id", j.id, "attempts", j.attempts, "err", err)
		_, err = q.db.ExecContext(rec, `UPDATE jobs SET status = ?, finished_at = ?, last_error = ? WHERE id = ?`, StatusFailed, nowUnix, clip(err.Error()), j.id)
	default:
		q.log.Info("job will retry", "kind", j.kind, "id", j.id, "attempt", j.attempts, "err", err)
		after := time.Now().Add(q.backoff(j.attempts)).Unix()
		_, err = q.db.ExecContext(rec, `UPDATE jobs SET status = ?, run_after = ?, last_error = ? WHERE id = ?`, StatusQueued, after, clip(err.Error()), j.id)
	}
	if err != nil {
		q.log.Error("record job result", "id", j.id, "err", err)
	}
}

// safely runs h and turns a panic into a permanent error, so one bad handler
// cannot take the process down.
func safely(ctx context.Context, h Handler, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(fmt.Errorf("panic: %v\n%s", r, debug.Stack()))
		}
	}()
	return h(ctx, payload)
}

// prune deletes finished jobs older than the retention window.
func (q *Queue) prune(ctx context.Context) {
	cutoff := time.Now().Add(-q.keepDone).Unix()
	if _, err := q.db.ExecContext(ctx, `DELETE FROM jobs WHERE status IN (?, ?) AND finished_at > 0 AND finished_at < ?`, StatusDone, StatusFailed, cutoff); err != nil {
		q.log.Error("prune jobs", "err", err)
	}
}

// clip bounds an error message stored in the database.
func clip(s string) string {
	const limit = 4000
	if len(s) > limit {
		return s[:limit]
	}
	return s
}
