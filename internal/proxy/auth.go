package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// authRemember is how long a verified user name and password are
	// accepted without another bcrypt comparison. A comparison costs tens
	// of milliseconds of CPU, and a page loads dozens of files.
	authRemember = 5 * time.Minute
	// authRemembered bounds the table of verified credentials.
	authRemembered = 256
	// authWait is how long a request waits for its turn to be compared.
	authWait = 5 * time.Second
	// authWaiting is how many requests for one route may wait for a turn.
	// More are refused at once: a flood of guesses at one site then holds
	// a few places in the line, not all of it.
	authWaiting = 4
	// maxPassword is bcrypt's limit; a longer password would be compared
	// by its first 72 bytes only.
	maxPassword = 72
)

type authResult int

const (
	authDenied authResult = iota
	authOK
	authBusy
)

// gate checks the user name and password of guarded routes.
type gate struct {
	// key makes what is remembered useless outside this process.
	key []byte
	// turn lets one comparison run at a time: guessing passwords must not
	// take every CPU from the sites being served.
	turn chan struct{}

	mu       sync.Mutex
	verified map[[sha256.Size]byte]time.Time // credentials → until when
	waiting  map[string]int                  // route → requests waiting for a turn
	now      func() time.Time
}

func newGate() *gate {
	g := &gate{
		key:      make([]byte, 32),
		turn:     make(chan struct{}, 1),
		verified: make(map[[sha256.Size]byte]time.Time),
		waiting:  make(map[string]int),
		now:      time.Now,
	}
	rand.Read(g.key)
	return g
}

// check reports whether a request carries the route's user name and
// password.
func (g *gate) check(r *http.Request, rt Route) authResult {
	user, password, ok := r.BasicAuth()
	if !ok || len(password) > maxPassword {
		return authDenied
	}
	id := g.id(rt, user, password)
	if g.remembered(id) {
		return authOK
	}
	route := rt.Host + rt.Path + "\x00" + rt.guard
	if !g.wait(route) {
		return authBusy
	}
	wait := time.NewTimer(authWait)
	defer wait.Stop()
	select {
	case g.turn <- struct{}{}:
		g.waited(route)
	case <-r.Context().Done():
		g.waited(route)
		return authDenied
	case <-wait.C:
		g.waited(route)
		return authBusy
	}
	// Compared whatever the user name is, so that a wrong name and a wrong
	// password take the same time.
	err := bcrypt.CompareHashAndPassword([]byte(rt.AuthHash), []byte(password))
	<-g.turn
	if err != nil || subtle.ConstantTimeCompare([]byte(user), []byte(rt.AuthUser)) != 1 {
		return authDenied
	}
	g.remember(id)
	return authOK
}

// id names one user name and password on one route. The route's hash is
// part of it: a new password, or another route, never matches what was
// remembered for this one.
func (g *gate) id(rt Route, user, password string) (id [sha256.Size]byte) {
	h := hmac.New(sha256.New, g.key)
	// The path is the one the password belongs to, which a route asking on
	// another's behalf (Table.Guard) carries apart from its own.
	owner := rt.Path
	if rt.guard != "" {
		owner = rt.guard
	}
	for _, part := range []string{rt.Host, owner, rt.AuthUser, rt.AuthHash, user, password} {
		// The length first, so that parts cannot run into each other.
		h.Write([]byte{byte(len(part) >> 8), byte(len(part))})
		h.Write([]byte(part))
	}
	h.Sum(id[:0])
	return id
}

// wait takes a place in the line for a route, or reports that the line
// for it is full.
func (g *gate) wait(route string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting[route] >= authWaiting {
		return false
	}
	g.waiting[route]++
	return true
}

func (g *gate) waited(route string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting[route]--; g.waiting[route] <= 0 {
		delete(g.waiting, route)
	}
}

// remembered reports whether credentials were verified a short while ago.
// Each use keeps them a while longer: somebody working behind a password
// is then not sent back to the line every five minutes, where a flood of
// guesses could keep them out.
func (g *gate) remembered(id [sha256.Size]byte) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.verified[id]
	if !ok {
		return false
	}
	now := g.now()
	if !now.Before(until) {
		delete(g.verified, id)
		return false
	}
	g.verified[id] = now.Add(authRemember)
	return true
}

func (g *gate) remember(id [sha256.Size]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if len(g.verified) >= authRemembered {
		for k, until := range g.verified {
			if !now.Before(until) {
				delete(g.verified, k)
			}
		}
	}
	// Still full of live entries: one of them gives way. Which one does not
	// matter, since its owner is only compared again.
	for k := range g.verified {
		if len(g.verified) < authRemembered {
			break
		}
		delete(g.verified, k)
	}
	g.verified[id] = now.Add(authRemember)
}
