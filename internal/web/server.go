// Package web is the control plane's HTTP layer: pages, forms and live
// updates, all rendered on the server.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/ops"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
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
	// Deploy runs deployments and publishes routes; Pool reaches servers.
	Ops    *ops.Ops
	Deploy *deploy.Deployer
	Pool   *servers.Pool
	Log    *slog.Logger
	// Pprof exposes /debug/pprof to loopback clients.
	Pprof bool
	// Closing is cancelled when the process begins to shut down. Live
	// streams end with it. Nil means streams end only with their request.
	Closing context.Context

	// GitHub is the client for GitHub's API. Nil uses github.com.
	GitHub *source.GitHub

	logins *auth.Limiter
	// hooks limits the endpoints other machines call; hookBodies lets one
	// webhook body be read at a time.
	hooks      *auth.Limiter
	hookBodies chan struct{}
	// hashing bounds how many password hashes run at once. Each costs about
	// a quarter of a second of CPU; without a bound a burst of sign-in
	// requests would pile them up.
	hashing chan struct{}
	// streams bounds the live log views open at once.
	streams chan struct{}
	// What phase 9 added: see extras.go.
	extras
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	s.logins = auth.NewLimiter(5, 15*time.Minute)
	s.hooks = auth.NewLimiter(120, time.Minute)
	if s.GitHub == nil {
		s.GitHub = source.NewGitHub()
	}
	s.hookBodies = make(chan struct{}, 1)
	s.hashing = make(chan struct{}, 2)
	s.streams = make(chan struct{}, maxStreams)
	mux := http.NewServeMux()

	// Open to everyone.
	mux.Handle("GET /static/{name}", static.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	// Called by other machines: authenticated by signature or bearer token.
	mux.HandleFunc("POST /webhooks/github/{id}", s.githubWebhook)
	mux.HandleFunc("POST /webhooks/git/{id}", s.gitWebhook)
	mux.HandleFunc("POST /api/v1/deploy", s.apiDeploy)

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

	mux.Handle("GET /projects/{id}/apps/new", s.authed(s.appNew))
	mux.Handle("POST /projects/{id}/apps", s.authed(s.appCreate))
	mux.Handle("GET /apps/{id}", s.authed(s.appOverview))
	mux.Handle("GET /apps/{id}/status", s.authed(s.appStatus))
	mux.Handle("POST /apps/{id}/deploy", s.authed(s.appDeploy))
	mux.Handle("POST /apps/{id}/stop", s.authed(s.appStop))
	mux.Handle("GET /apps/{id}/deployments", s.authed(s.appDeployments))
	mux.Handle("GET /apps/{id}/deployments/{dep}", s.authed(s.appDeployment))
	mux.Handle("GET /apps/{id}/deployments/{dep}/status", s.authed(s.appDeploymentStatus))
	mux.Handle("POST /apps/{id}/deployments/{dep}/rollback", s.authed(s.appRollback))
	mux.Handle("GET /apps/{id}/deployments/{dep}/stream", s.authed(s.appDeploymentStream))
	mux.Handle("GET /apps/{id}/logs", s.authed(s.appLogs))
	mux.Handle("GET /apps/{id}/logs/stream", s.authed(s.appLogsStream))
	mux.Handle("GET /apps/{id}/environment", s.authed(s.ownSettings(s.appEnvironment)))
	mux.Handle("POST /apps/{id}/environment", s.authed(s.ownSettings(s.appEnvironmentSave)))
	mux.Handle("GET /apps/{id}/storage", s.authed(s.ownSettings(s.appStorage)))
	mux.Handle("POST /apps/{id}/storage", s.authed(s.ownSettings(s.appStorageAdd)))
	mux.Handle("POST /apps/{id}/storage/{sid}/delete", s.authed(s.ownSettings(s.appStorageDelete)))
	mux.Handle("GET /apps/{id}/settings", s.authed(s.appSettings))
	mux.Handle("POST /apps/{id}/settings", s.authed(s.ownSettings(s.appSettingsSave)))
	mux.Handle("POST /apps/{id}/domains", s.authed(s.ownSettings(s.appDomainAdd)))
	mux.Handle("POST /apps/{id}/domains/{did}/delete", s.authed(s.ownSettings(s.appDomainDelete)))
	mux.Handle("POST /apps/{id}/delete", s.authed(s.appDelete))
	mux.Handle("GET /apps/{id}/tasks", s.authed(s.ownSettings(s.appTasks)))
	mux.Handle("POST /apps/{id}/tasks", s.authed(s.ownSettings(s.appTaskCreate)))
	mux.Handle("GET /apps/{id}/tasks/{tid}", s.authed(s.ownSettings(s.appTask)))
	mux.Handle("POST /apps/{id}/tasks/{tid}", s.authed(s.ownSettings(s.appTaskSave)))
	mux.Handle("GET /apps/{id}/tasks/{tid}/runs", s.authed(s.ownSettings(s.appTaskRuns)))
	mux.Handle("POST /apps/{id}/tasks/{tid}/run", s.authed(s.ownSettings(s.appTaskRun)))
	mux.Handle("POST /apps/{id}/tasks/{tid}/delete", s.authed(s.ownSettings(s.appTaskDelete)))
	mux.Handle("POST /apps/{id}/source", s.authed(s.ownSettings(s.appSourceSave)))
	mux.Handle("POST /apps/{id}/webhook-secret", s.authed(s.ownSettings(s.appWebhookSecret)))
	mux.Handle("POST /apps/{id}/deploy-token", s.authed(s.ownSettings(s.appDeployToken)))
	mux.Handle("POST /apps/{id}/build-server", s.authed(s.ownSettings(s.appBuildServer)))
	mux.Handle("POST /apps/{id}/previews", s.authed(s.appPreviewsSave))
	mux.Handle("POST /apps/{id}/previews/{number}/delete", s.authed(s.appPreviewDelete))

	mux.Handle("GET /projects/{id}/databases/new", s.authed(s.databaseNew))
	mux.Handle("POST /projects/{id}/databases", s.authed(s.databaseCreate))
	mux.Handle("GET /databases/{id}", s.authed(s.databaseOverview))
	mux.Handle("GET /databases/{id}/status", s.authed(s.databaseStatus))
	mux.Handle("POST /databases/{id}/start", s.authed(s.databaseStart))
	mux.Handle("POST /databases/{id}/stop", s.authed(s.databaseStop))
	mux.Handle("GET /databases/{id}/logs", s.authed(s.databaseLogs))
	mux.Handle("GET /databases/{id}/logs/stream", s.authed(s.databaseLogsStream))
	mux.Handle("GET /databases/{id}/settings", s.authed(s.databaseSettings))
	mux.Handle("POST /databases/{id}/settings", s.authed(s.databaseSettingsSave))
	mux.Handle("POST /databases/{id}/delete", s.authed(s.databaseDelete))
	mux.Handle("GET /databases/{id}/backups", s.authed(s.databaseBackups))
	mux.Handle("POST /databases/{id}/backups", s.authed(s.databaseBackupNow))
	mux.Handle("GET /databases/{id}/backups/list", s.authed(s.databaseBackupList))
	mux.Handle("POST /databases/{id}/backups/schedule", s.authed(s.databaseBackupSchedule))
	mux.Handle("GET /databases/{id}/backups/{bid}/download", s.authed(s.databaseBackupDownload))
	mux.Handle("POST /databases/{id}/backups/{bid}/restore", s.authed(s.databaseBackupRestore))
	mux.Handle("POST /databases/{id}/backups/{bid}/delete", s.authed(s.databaseBackupDelete))

	mux.Handle("GET /projects/{id}/services/new", s.authed(s.serviceNew))
	mux.Handle("POST /projects/{id}/services", s.authed(s.serviceCreate))
	mux.Handle("GET /services/{id}", s.authed(s.serviceOverview))
	mux.Handle("GET /services/{id}/status", s.authed(s.serviceStatus))
	mux.Handle("POST /services/{id}/deploy", s.authed(s.serviceDeploy))
	mux.Handle("POST /services/{id}/stop", s.authed(s.serviceStop))
	mux.Handle("GET /services/{id}/deploy-log/stream", s.authed(s.serviceDeployLog))
	mux.Handle("GET /services/{id}/logs", s.authed(s.serviceLogs))
	mux.Handle("GET /services/{id}/logs/stream", s.authed(s.serviceLogsStream))
	mux.Handle("GET /services/{id}/compose", s.authed(s.serviceCompose))
	mux.Handle("POST /services/{id}/compose", s.authed(s.serviceComposeSave))
	mux.Handle("GET /services/{id}/settings", s.authed(s.serviceSettings))
	mux.Handle("POST /services/{id}/source", s.authed(s.serviceSourceSave))
	mux.Handle("POST /services/{id}/webhook-secret", s.authed(s.serviceWebhookSecret))
	mux.Handle("POST /services/{id}/deploy-token", s.authed(s.serviceDeployToken))
	mux.Handle("POST /services/{id}/endpoints/{eid}", s.authed(s.serviceEndpointSave))
	mux.Handle("POST /services/{id}/delete", s.authed(s.serviceDelete))

	mux.Handle("GET /sources", s.authed(s.sourcesPage))
	mux.Handle("POST /sources/github", s.authed(s.githubStart))
	mux.Handle("GET /sources/github/callback", s.authed(s.githubCallback))
	mux.Handle("GET /sources/github/installed", s.authed(s.githubInstalled))
	mux.Handle("GET /sources/github/{id}/repos", s.authed(s.githubRepos))
	mux.Handle("POST /sources/github/{id}/delete", s.authed(s.githubDelete))
	mux.Handle("POST /sources/keys", s.authed(s.sshKeyCreate))
	mux.Handle("POST /sources/keys/{id}/delete", s.authed(s.sshKeyDelete))

	mux.Handle("GET /servers", s.authed(s.serverList))
	mux.Handle("POST /servers/{id}", s.authed(s.serverUpdate))
	mux.Handle("POST /servers", s.authed(s.serverCreate))
	mux.Handle("POST /servers/{id}/check", s.authed(s.serverCheck))
	mux.Handle("POST /servers/{id}/proxy", s.authed(s.serverProxy))
	mux.Handle("POST /servers/{id}/forget-host-key", s.authed(s.serverForgetHostKey))
	mux.Handle("POST /servers/{id}/delete", s.authed(s.serverDelete))
	mux.Handle("GET /settings", s.authed(s.settingsPage))
	mux.Handle("POST /settings", s.authed(s.settingsSave))
	mux.Handle("GET /settings/storages", s.authed(s.storagesPage))
	mux.Handle("POST /settings/storages", s.authed(s.storageCreate))
	mux.Handle("POST /settings/storages/{sid}/test", s.authed(s.storageTest))
	mux.Handle("POST /settings/storages/{sid}/delete", s.authed(s.storageDelete))
	mux.Handle("GET /settings/notifications", s.authed(s.notificationsPage))
	mux.Handle("POST /settings/notifications", s.authed(s.notificationCreate))
	mux.Handle("POST /settings/notifications/{cid}", s.authed(s.notificationSave))
	mux.Handle("POST /settings/notifications/{cid}/test", s.authed(s.notificationTest))
	mux.Handle("POST /settings/notifications/{cid}/delete", s.authed(s.notificationDelete))

	s.extraRoutes(mux)

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
