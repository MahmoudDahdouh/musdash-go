// Package auth holds password hashing, password rules and the login limiter.
package auth

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Cost is the bcrypt work factor. bcrypt is used rather than argon2 because
// argon2's default parameters allocate about 64 MB per hash, more than the
// whole idle budget. Tests lower the cost to keep the suite fast.
var Cost = 12

// Password length limits. bcrypt ignores input beyond 72 bytes, so longer
// passwords are refused instead of being silently truncated.
const (
	MinPassword = 10
	MaxPassword = 72
)

var (
	ErrPasswordShort = errors.New("Use at least 10 characters.")
	ErrPasswordLong  = errors.New("Use at most 72 characters.")
)

// ValidatePassword checks a new password against the length rules.
func ValidatePassword(pw string) error {
	switch {
	case len(pw) < MinPassword:
		return ErrPasswordShort
	case len(pw) > MaxPassword:
		return ErrPasswordLong
	}
	return nil
}

// HashPassword returns the bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), Cost)
	return string(h), err
}

// dummyHash is compared against when the account does not exist, so a login
// attempt takes the same time whether or not the email is registered.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("musdash-no-such-account"), Cost)
	return h
})

// CheckPassword reports whether pw matches hash. An empty hash (no such
// account) still performs a full comparison.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Limiter allows a fixed number of attempts per key inside a window.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	count int
	first time.Time
}

// maxEntries bounds the limiter's memory. Keys are short (callers hash
// anything a client controls). When the table is full, expired entries are
// dropped; if none have expired it is reset, which only ever makes the
// limiter more lenient for a moment.
const maxEntries = 4096

// NewLimiter allows max attempts per key in each window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, entries: make(map[string]*entry)}
}

// Take counts one attempt against every key and reports whether the attempt
// may proceed. Checking and counting happen under one lock, so a burst of
// parallel requests cannot all slip through before any of them is counted.
// When the attempt is refused, wait is how long until the earliest refused
// key opens again.
func (l *Limiter) Take(keys ...string) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	ok = true
	for _, key := range keys {
		e, found := l.entries[key]
		if found && t.Sub(e.first) >= l.window {
			delete(l.entries, key)
			found = false
		}
		if found && e.count >= l.max {
			ok = false
			if left := l.window - t.Sub(e.first); left > wait {
				wait = left
			}
		}
	}
	if !ok {
		return false, wait
	}
	for _, key := range keys {
		if e, found := l.entries[key]; found {
			e.count++
			continue
		}
		if len(l.entries) >= maxEntries {
			for k, e := range l.entries {
				if t.Sub(e.first) >= l.window {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= maxEntries {
				clear(l.entries)
			}
		}
		l.entries[key] = &entry{count: 1, first: t}
	}
	return true, 0
}

// Reset forgets the keys, after a successful attempt.
func (l *Limiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.entries, key)
	}
}
