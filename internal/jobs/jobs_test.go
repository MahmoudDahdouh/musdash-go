package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

func newQueue(t *testing.T, workers int) (*Queue, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
	q := New(d.DB, slog.New(slog.NewTextHandler(io.Discard, nil)), workers)
	q.pollEvery = 20 * time.Millisecond
	q.backoff = func(int) time.Duration { return 0 }
	return q, d
}

func start(t *testing.T, q *Queue) {
	t.Helper()
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		q.Stop(ctx)
	})
}

func status(t *testing.T, d *db.DB, id string) (string, int, string) {
	t.Helper()
	var st, lastErr string
	var attempts int
	if err := d.QueryRow(`SELECT status, attempts, last_error FROM jobs WHERE id = ?`, id).Scan(&st, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	return st, attempts, lastErr
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestJobRunsOnceWithPayload(t *testing.T) {
	q, d := newQueue(t, 2)
	var calls atomic.Int32
	var got atomic.Value
	q.Register("greet", func(_ context.Context, payload []byte) error {
		calls.Add(1)
		got.Store(string(payload))
		return nil
	})
	start(t, q)

	id, err := q.Enqueue(context.Background(), "greet", map[string]string{"name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job done", func() bool { st, _, _ := status(t, d, id); return st == StatusDone })
	time.Sleep(60 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times", calls.Load())
	}
	if got.Load() != `{"name":"x"}` {
		t.Fatalf("payload %v", got.Load())
	}
}

func TestRetryThenFail(t *testing.T) {
	q, d := newQueue(t, 1)
	var calls atomic.Int32
	q.Register("flaky", func(context.Context, []byte) error {
		calls.Add(1)
		return errors.New("nope")
	})
	start(t, q)

	id, _ := q.Enqueue(context.Background(), "flaky", nil, WithMaxAttempts(3))
	waitFor(t, "job failed", func() bool { st, _, _ := status(t, d, id); return st == StatusFailed })
	st, attempts, lastErr := status(t, d, id)
	if calls.Load() != 3 || attempts != 3 || lastErr != "nope" {
		t.Fatalf("status %s calls %d attempts %d err %q", st, calls.Load(), attempts, lastErr)
	}
}

func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	q, d := newQueue(t, 1)
	var calls atomic.Int32
	q.Register("once-bad", func(context.Context, []byte) error {
		if calls.Add(1) == 1 {
			return errors.New("first time")
		}
		return nil
	})
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "once-bad", nil)
	waitFor(t, "job done", func() bool { st, _, _ := status(t, d, id); return st == StatusDone })
	if _, attempts, lastErr := status(t, d, id); attempts != 2 || lastErr != "" {
		t.Fatalf("attempts %d err %q", attempts, lastErr)
	}
}

func TestPermanentErrorPanicAndUnknownKindDoNotRetry(t *testing.T) {
	q, d := newQueue(t, 2)
	var calls atomic.Int32
	q.Register("perm", func(context.Context, []byte) error {
		calls.Add(1)
		return Permanent(errors.New("bad input"))
	})
	q.Register("panics", func(context.Context, []byte) error {
		calls.Add(1)
		panic("boom")
	})
	start(t, q)

	ctx := context.Background()
	a, _ := q.Enqueue(ctx, "perm", nil)
	b, _ := q.Enqueue(ctx, "panics", nil)
	c, _ := q.Enqueue(ctx, "unregistered", nil)
	for _, id := range []string{a, b, c} {
		waitFor(t, "job failed", func() bool { st, _, _ := status(t, d, id); return st == StatusFailed })
		if _, attempts, _ := status(t, d, id); attempts != 1 {
			t.Errorf("job %s ran %d times, want 1", id, attempts)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("handlers ran %d times, want 2", calls.Load())
	}
}

func TestLockKeySerialises(t *testing.T) {
	q, d := newQueue(t, 4)
	var active, maxActive, freeActive, maxFree atomic.Int32
	track := func(cur, peak *atomic.Int32) Handler {
		return func(context.Context, []byte) error {
			n := cur.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			cur.Add(-1)
			return nil
		}
	}
	q.Register("build", track(&active, &maxActive))
	q.Register("free", track(&freeActive, &maxFree))
	start(t, q)

	ctx := context.Background()
	var ids []string
	for range 4 {
		id, _ := q.Enqueue(ctx, "build", nil, WithLockKey("build:server1"))
		ids = append(ids, id)
		id, _ = q.Enqueue(ctx, "free", nil)
		ids = append(ids, id)
	}
	for _, id := range ids {
		waitFor(t, "all jobs done", func() bool { st, _, _ := status(t, d, id); return st == StatusDone })
	}
	if maxActive.Load() != 1 {
		t.Fatalf("%d jobs with one lock key overlapped", maxActive.Load())
	}
	if maxFree.Load() < 2 {
		t.Fatalf("unlocked jobs never ran concurrently (peak %d)", maxFree.Load())
	}
}

func TestRunAfterDelays(t *testing.T) {
	q, d := newQueue(t, 1)
	q.Register("later", func(context.Context, []byte) error { return nil })
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "later", nil, WithRunAfter(time.Now().Add(time.Hour)))
	time.Sleep(120 * time.Millisecond)
	if st, _, _ := status(t, d, id); st != StatusQueued {
		t.Fatalf("delayed job is %s", st)
	}
}

func TestCrashedJobIsRequeuedAtStart(t *testing.T) {
	q, d := newQueue(t, 1)
	// A job left running by a process that died.
	if _, err := d.Exec(`INSERT INTO jobs (id, kind, status, attempts, run_after, created_at, started_at) VALUES ('crashed', 'resume', 'running', 1, 0, 0, 5)`); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Bool
	q.Register("resume", func(context.Context, []byte) error { ran.Store(true); return nil })
	start(t, q)
	waitFor(t, "crashed job to finish", func() bool { st, _, _ := status(t, d, "crashed"); return st == StatusDone })
	if !ran.Load() {
		t.Fatal("handler did not run")
	}
}

func TestStopWaitsThenCancels(t *testing.T) {
	q, d := newQueue(t, 2)
	var finished atomic.Bool
	q.Register("slow", func(context.Context, []byte) error {
		time.Sleep(150 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	var once sync.Once
	blocked := make(chan struct{})
	q.Register("stuck", func(ctx context.Context, _ []byte) error {
		once.Do(func() { close(blocked) })
		<-ctx.Done()
		return ctx.Err()
	})
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	slow, _ := q.Enqueue(context.Background(), "slow", nil)
	waitFor(t, "slow job to start", func() bool { st, _, _ := status(t, d, slow); return st == StatusRunning })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	q.Stop(ctx)
	cancel()
	if !finished.Load() {
		t.Fatal("Stop returned before the running job finished")
	}
	if st, _, _ := status(t, d, slow); st != StatusDone {
		t.Fatalf("slow job is %s", st)
	}

	// A handler that never returns on its own is cancelled at the deadline.
	q2 := New(d.DB, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	q2.pollEvery = 20 * time.Millisecond
	q2.Register("stuck", q.handlers["stuck"])
	if err := q2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stuck, _ := q2.Enqueue(context.Background(), "stuck", nil, WithMaxAttempts(1))
	<-blocked
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	q2.Stop(ctx)
	// Interrupted by shutdown: back in the queue with its attempt refunded.
	if st, attempts, _ := status(t, d, stuck); st != StatusQueued || attempts != 0 {
		t.Fatalf("stuck job is %s with %d attempts, want queued with 0", st, attempts)
	}
}

func TestDefaultBackoff(t *testing.T) {
	want := []time.Duration{5 * time.Second, 20 * time.Second, 80 * time.Second, 320 * time.Second, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := defaultBackoff(i + 1); got != w {
			t.Errorf("attempt %d: %v, want %v", i+1, got, w)
		}
	}
}
