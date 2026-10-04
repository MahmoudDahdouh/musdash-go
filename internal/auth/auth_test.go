package auth

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordRules(t *testing.T) {
	cases := map[string]error{
		"short":                 ErrPasswordShort,
		"exactly10!":            nil,
		strings.Repeat("a", 72): nil,
		strings.Repeat("a", 73): ErrPasswordLong,
		strings.Repeat("é", 36): nil, // 72 bytes
		strings.Repeat("é", 37): ErrPasswordLong,
	}
	for pw, want := range cases {
		if got := ValidatePassword(pw); got != want {
			t.Errorf("ValidatePassword(%d bytes) = %v, want %v", len(pw), got, want)
		}
	}
}

func TestHashAndCheck(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery") {
		t.Fatal("right password rejected")
	}
	if CheckPassword(hash, "wrong") || CheckPassword("", "anything") || CheckPassword("not-a-hash", "x") {
		t.Fatal("wrong password, missing account or malformed hash accepted")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := l.Take("k", "shared"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	ok, wait := l.Take("k", "shared")
	if ok || wait != time.Minute {
		t.Fatalf("fourth attempt: ok=%v wait=%v", ok, wait)
	}
	// One exhausted key refuses the attempt even when the other key is new.
	if ok, _ := l.Take("fresh", "shared"); ok {
		t.Fatal("an exhausted shared key must refuse")
	}
	// A refused attempt is not counted against its other keys.
	if ok, _ := l.Take("fresh"); !ok {
		t.Fatal("a refused attempt consumed a slot of another key")
	}
	if ok, _ := l.Take("other"); !ok {
		t.Fatal("another key must not be affected")
	}

	now = now.Add(61 * time.Second)
	if ok, _ := l.Take("k", "shared"); !ok {
		t.Fatal("still refused after the window passed")
	}

	l.Reset("k", "shared")
	for range 3 {
		if ok, _ := l.Take("k"); !ok {
			t.Fatal("Reset did not clear earlier attempts")
		}
	}
}

func TestLimiterCountsParallelAttempts(t *testing.T) {
	l := NewLimiter(5, time.Minute)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Take("k"); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("%d parallel attempts allowed, want 5", allowed.Load())
	}
}

func TestLimiterIsBounded(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	for i := range maxEntries * 3 {
		l.Take(time.Duration(i).String())
	}
	if len(l.entries) > maxEntries {
		t.Fatalf("limiter grew to %d entries", len(l.entries))
	}
}
