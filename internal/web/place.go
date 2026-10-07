package web

import (
	"errors"
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// inEnv is what the address of everything inside an environment starts
// with: the project, then the environment. In a route under it {id} is the
// resource the route is about, as it is in every other route.
const inEnv = "/projects/{project}/env/{env}"

// resourceRoute registers one route of a resource of some kind: the
// method, what follows the resource's address, who may call it.
type resourceRoute func(method, rest string, who access, h http.HandlerFunc)

// placed wraps every route of a resource of this kind. The address names a
// project and an environment besides the resource, and the three have to
// belong together: an address that names a project the resource is not in
// is not a second address of its page. It is asked here, in front of the
// handler, so that no handler can forget it, the ones that load only the
// row (a status that is polled) included.
func (s *Server) placed(kind string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, envID, err := s.DB.PlaceOf(r.Context(), sessionFrom(r).TeamID, kind, r.PathValue("id"))
		if errors.Is(err, db.ErrNotFound) || (err == nil && (projectID != r.PathValue("project") || envID != r.PathValue("env"))) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r)
	}
}

// placeInPath is the address of the resource a placed route was asked for,
// for a handler that has the row and not where it is: placed has checked
// that the path says where.
func placeInPath(r *http.Request, kind string) string {
	return pages.ResourcePath(r.PathValue("project"), r.PathValue("env"), kind, r.PathValue("id"))
}

// short answers the short address of a resource, /apps/{id} and what
// follows it: it names no project and no environment, and leads to the
// address that does, with the rest of the path and the query. It is what a
// message musdash sent holds (a notification's link), and every bookmark
// from before a resource's address said where it is.
func (s *Server) short(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, envID, err := s.DB.PlaceOf(r.Context(), sessionFrom(r).TeamID, kind, r.PathValue("id"))
		if errors.Is(err, db.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		to := pages.ResourcePath(projectID, envID, kind, r.PathValue("id"))
		if rest := r.PathValue("rest"); rest != "" {
			to += "/" + rest
		}
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
		redirect(w, r, to)
	}
}

// oldEnvironment answers the address an environment's page had,
// /projects/{id}/e/{env} and what was under it, with the page itself.
func (s *Server) oldEnvironment(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, pages.EnvPath(r.PathValue("id"), r.PathValue("env")))
}

// oldEnvironmentVariables answers the address an environment's shared
// variables had, which named the environment alone.
func (s *Server) oldEnvironmentVariables(w http.ResponseWriter, r *http.Request) {
	env, err := s.DB.Environment(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, pages.EnvPath(env.ProjectID, env.ID)+"/variables")
}
