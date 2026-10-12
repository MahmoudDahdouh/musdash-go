package ops

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpdateIsAskedForOnceADay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	asked, answer, fail := 0, "v0.1.3", false
	e.o.Latest = func(context.Context) (string, error) {
		asked++
		if fail {
			return "", errors.New("no route to host")
		}
		return answer, nil
	}
	start := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

	// A build from a checkout asks nobody.
	e.o.Version = "dev"
	e.o.updateIfDue(ctx, start)
	if asked != 0 {
		t.Fatal("a development build asked for the latest release")
	}

	e.o.Version = "v0.1.2"
	e.o.updateIfDue(ctx, start)
	e.o.updateIfDue(ctx, start.Add(time.Minute))
	e.o.updateIfDue(ctx, start.Add(23*time.Hour))
	if asked != 1 {
		t.Fatalf("asked %d times in a day, want 1", asked)
	}
	u, err := e.o.Update(ctx)
	if err != nil || u.Latest != "v0.1.3" || u.Checked != start.Unix() || !u.Auto || !u.Available() {
		t.Fatalf("state = %+v, %v", u, err)
	}

	// A failure is not tried again every minute, and keeps what was known.
	fail = true
	next := start.Add(24 * time.Hour)
	e.o.updateIfDue(ctx, next)
	e.o.updateIfDue(ctx, next.Add(time.Minute))
	if asked != 2 {
		t.Fatalf("asked %d times, want 2", asked)
	}
	if u, _ := e.o.Update(ctx); u.Latest != "v0.1.3" || u.Checked != start.Unix() {
		t.Fatalf("a failed check changed what was stored: %+v", u)
	}

	// Switched off, nothing is asked; the button still asks.
	fail = false
	if err := e.o.SetUpdateAuto(ctx, false); err != nil {
		t.Fatal(err)
	}
	e.o.updateIfDue(ctx, next.Add(48*time.Hour))
	if asked != 2 {
		t.Fatal("asked although the daily check is off")
	}
	answer = "v0.2.0"
	if got, err := e.o.CheckUpdate(ctx, next); err != nil || got != "v0.2.0" {
		t.Fatalf("CheckUpdate = %q, %v", got, err)
	}
	if u, _ := e.o.Update(ctx); u.Latest != "v0.2.0" || u.Auto {
		t.Fatalf("state = %+v", u)
	}

	// After the upgrade the same answer is no update.
	e.o.Version = "v0.2.0"
	if u, _ := e.o.Update(ctx); u.Available() {
		t.Fatal("the running version is offered as an update")
	}
}
