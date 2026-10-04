// Package web is the control plane's HTTP layer: pages, forms and live
// updates, all rendered on the server.
package web

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/sysmem"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/static"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// Server holds what handlers need.
type Server struct {
	Cfg   *config.Config
	DB    *db.DB
	Box   *secret.Box
	Queue *jobs.Queue
	Log   *slog.Logger
	// Pprof exposes /debug/pprof to loopback clients.
	Pprof bool

	logins *auth.Limiter
	// hashing bounds how many password hashes run at once. Each costs about
	// a quarter of a second of CPU; without a bound a burst of sign-in
	// requests would pile them up.
	hashing chan struct{}
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	s.logins = auth.NewLimiter(5, 15*time.Minute)
	s.hashing = make(chan struct{}, 2)
	mux := http.NewServeMux()

	// Open to everyone.
	mux.Handle("GET /static/{name}", static.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	// Signed-out pages.
	mux.Handle("GET /setup", s.anon(s.setupForm))
	mux.Handle("POST /setup", s.anon(s.setupSubmit))
	mux.Handle("GET /login", s.anon(s.loginForm))
	mux.Handle("POST /login", s.anon(s.loginSubmit))
	mux.Handle("GET /reset/{token}", s.anon(s.resetForm))
	mux.Handle("POST /reset/{token}", s.anon(s.resetSubmit))

	// Signed-in pages.
	mux.Handle("POST /logout", s.authed(s.logout))
	mux.Handle("GET /account", s.authed(s.accountPage))
	mux.Handle("POST /account/profile", s.authed(s.accountProfile))
	mux.Handle("POST /account/password", s.authed(s.accountPassword))
	mux.Handle("GET /sys/mem", s.authed(s.memReadout))

	mux.Handle("GET /{$}", s.authed(s.projectList))
	mux.Handle("GET /projects/new", s.authed(s.projectNew))
	mux.Handle("POST /projects", s.authed(s.projectCreate))
	mux.Handle("GET /projects/{id}", s.authed(s.projectShow))
	mux.Handle("POST /projects/{id}", s.authed(s.projectUpdate))
	mux.Handle("GET /projects/{id}/settings", s.authed(s.projectSettings))
	mux.Handle("POST /projects/{id}/delete", s.authed(s.projectDelete))
	mux.Handle("POST /projects/{id}/environments", s.authed(s.environmentCreate))
	mux.Handle("POST /environments/{id}/delete", s.authed(s.environmentDelete))

	if s.Cfg.Dev {
		mux.Handle("GET /_ui", s.authed(s.gallery))
	}
	if s.Pprof {
		mux.Handle("/debug/pprof/", loopbackOnly(http.HandlerFunc(pprof.Index)))
		mux.Handle("/debug/pprof/cmdline", loopbackOnly(http.HandlerFunc(pprof.Cmdline)))
		mux.Handle("/debug/pprof/profile", loopbackOnly(http.HandlerFunc(pprof.Profile)))
		mux.Handle("/debug/pprof/symbol", loopbackOnly(http.HandlerFunc(pprof.Symbol)))
		mux.Handle("/debug/pprof/trace", loopbackOnly(http.HandlerFunc(pprof.Trace)))
	}

	// Anything else is a 404 inside the right frame.
	mux.Handle("/", s.anon(s.notFound))

	h := s.recoverer(secureHeaders(mux))
	if s.Cfg.Dev {
		h = s.requestLog(h)
	}
	return h
}

// render writes a templ component with the given status.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.Log.Error("render", "route", logRoute(r), "err", err)
	}
}

// shell builds the frame data for a signed-in page and consumes the flash.
func (s *Server) shell(w http.ResponseWriter, r *http.Request, title, active string, crumbs ...ui.Crumb) ui.Shell {
	sess := sessionFrom(r)
	return ui.Shell{
		Title:  title,
		Active: active,
		Name:   sess.User.Name,
		Email:  sess.User.Email,
		CSRF:   sess.CSRFToken,
		MemMB:  sysmem.MB(sysmem.RSS()),
		Crumbs: crumbs,
		Flash:  takeFlash(w, r),
		Dev:    s.Cfg.Dev,
	}
}

func (s *Server) memReadout(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, ui.MemReadout(sysmem.MB(sysmem.RSS())))
}

func (s *Server) gallery(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, pages.Gallery(s.shell(w, r, "Components", "ui")))
}

// notFound answers unknown paths: inside the app frame for a signed-in
// person, as a plain page otherwise.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r) != nil {
		s.render(w, r, http.StatusNotFound, pages.NotFound(s.shell(w, r, "Not found", "")))
		return
	}
	s.render(w, r, http.StatusNotFound, pages.Message("Page not found", "The address is wrong, or you need to sign in first."))
}

// fail logs an unexpected error and answers 500 without leaking detail.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "route", logRoute(r), "err", err)
	s.render(w, r, http.StatusInternalServerError, pages.Message("Something went wrong", "The error was logged on the server. Try again; if it keeps happening, check the musdash log."))
}

// redirect sends the browser to path. htmx requests get HX-Redirect so the
// whole page navigates rather than a fragment being swapped in.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func itoa(n int) string { return strconv.Itoa(n) }
