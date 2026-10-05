package web

import (
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// access says who may call a route. Every route is registered with one,
// so the question "who can reach this?" is answered in one place, and a
// test can walk the table instead of repeating it.
type access int

const (
	// open routes carry no session: assets, and the endpoints other
	// machines call, which prove themselves with a signature or a token.
	open access = iota
	// signedOut routes work without a session: sign in, reset, invitation.
	signedOut
	// member routes need a session, in any role.
	member
	// admin routes change what the team itself has: servers, sources,
	// settings, who is invited.
	admin
	// owner routes decide who holds which role.
	owner
)

// route is one line of the route table.
type route struct {
	pattern string
	who     access
}

// rank is the least role rank a route asks for.
func (a access) rank() int {
	switch a {
	case owner:
		return db.RoleRank(db.RoleOwner)
	case admin:
		return db.RoleRank(db.RoleAdmin)
	case member:
		return db.RoleRank(db.RoleMember)
	}
	return 0
}

// needs names the role a refused person would have to hold.
func (a access) needs() string {
	if a == owner {
		return "an Owner"
	}
	return "an Admin or an Owner"
}

// handle registers a route for those who may call it.
func (s *Server) handle(mux *http.ServeMux, pattern string, who access, h http.HandlerFunc) {
	s.routes = append(s.routes, route{pattern: pattern, who: who})
	switch who {
	case open:
		mux.Handle(pattern, h)
	case signedOut:
		mux.Handle(pattern, s.anon(h))
	default:
		mux.Handle(pattern, s.authed(s.atLeast(who, h)))
	}
}

// atLeast lets a request through when the session's role is high enough.
// The refusal comes before the handler runs: nothing is looked up for a
// person who may not ask, so the answer is the same for an id that exists
// and one that does not.
func (s *Server) atLeast(who access, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db.RoleRank(sessionFrom(r).Role) < who.rank() {
			s.render(w, r, http.StatusForbidden, pages.Forbidden(s.shell(w, r, "Not allowed", ""), who.needs()))
			return
		}
		next(w, r)
	}
}

// may reports whether the request's person holds at least the role. It is
// for the few checks a route's own level cannot express, such as an Admin
// who may remove a Member but not another Admin.
func may(r *http.Request, who access) bool {
	return db.RoleRank(sessionFrom(r).Role) >= who.rank()
}
