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
	// accounts counts attempts at one account's password and second step.
	// Its keys are accounts that exist, so nobody outside can fill it.
	accounts *auth.Limiter
	// hooks limits the endpoints other machines call; hookBodies lets one
	// webhook body be read at a time.
	hooks      *auth.Limiter
	hookBodies chan struct{}
	// apiCalls limits how many calls one API token makes in a minute, and
	// apiAddrs how many one address makes, with or without a token.
	apiCalls *auth.Limiter
	apiAddrs *auth.Limiter
	// hashing bounds how many password hashes run at once. Each costs about
	// a quarter of a second of CPU; without a bound a burst of sign-in
	// requests would pile them up.
	hashing chan struct{}
	// streams bounds the live log views open at once.
	streams chan struct{}
	// routes is the route table as it was registered, for the tests that
	// walk it.
	routes []route
	// What phase 9 added: see extras.go.
	extras
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	s.logins = auth.NewLimiter(5, 15*time.Minute)
	s.accounts = auth.NewLimiter(5, 15*time.Minute)
	s.hooks = auth.NewLimiter(120, time.Minute)
	s.apiCalls = auth.NewLimiter(apiPerMinute, time.Minute)
	s.apiAddrs = auth.NewLimiter(apiPerMinuteByAddress, time.Minute)
	if s.GitHub == nil {
		s.GitHub = source.NewGitHub()
	}
	s.hookBodies = make(chan struct{}, 1)
	s.hashing = make(chan struct{}, 2)
	s.streams = make(chan struct{}, maxStreams)
	mux := http.NewServeMux()
	// Every route is registered with who may call it; see routes.go.
	s.routes = nil
	handle := func(pattern string, who access, h http.HandlerFunc) { s.handle(mux, pattern, who, h) }

	// Open to everyone.
	handle("GET /static/{name}", open, static.Handler().ServeHTTP)
	handle("GET /healthz", open, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	// Called by other machines: authenticated by signature or bearer token.
	handle("POST /webhooks/github/{id}", open, s.githubWebhook)
	handle("POST /webhooks/git/{id}", open, s.gitWebhook)
	handle("POST /api/v1/deploy", open, s.apiDeploy)

	// The API: each request carries a person's API token and no session.
	read, operate := db.AbilityRead, db.AbilityDeploy
	handle("GET /api/v1/me", open, s.api(read, s.apiMe))
	handle("GET /api/v1/servers", open, s.api(read, s.apiServers))
	handle("GET /api/v1/projects", open, s.api(read, s.apiProjects))
	handle("GET /api/v1/tags", open, s.api(read, s.apiTags))
	handle("GET /api/v1/apps", open, s.api(read, s.apiApps))
	handle("GET /api/v1/apps/{id}", open, s.api(read, s.apiApp))
	handle("GET /api/v1/apps/{id}/deployments", open, s.api(read, s.apiAppDeployments))
	handle("POST /api/v1/apps/{id}/deploy", open, s.api(operate, s.apiAppDeploy))
	handle("POST /api/v1/apps/{id}/stop", open, s.api(operate, s.apiAppStop))
	handle("GET /api/v1/deployments/{id}", open, s.api(read, s.apiDeployment))
	handle("GET /api/v1/databases", open, s.api(read, s.apiDatabases))
	handle("GET /api/v1/databases/{id}", open, s.api(read, s.apiDatabase))
	handle("POST /api/v1/databases/{id}/start", open, s.api(operate, s.apiDatabaseStart))
	handle("POST /api/v1/databases/{id}/stop", open, s.api(operate, s.apiDatabaseStop))
	handle("GET /api/v1/services", open, s.api(read, s.apiServices))
	handle("GET /api/v1/services/{id}", open, s.api(read, s.apiService))
	handle("POST /api/v1/services/{id}/deploy", open, s.api(operate, s.apiServiceDeploy))
	handle("POST /api/v1/services/{id}/stop", open, s.api(operate, s.apiServiceStop))
	handle("/api/", open, s.apiNotFound)

	// Signed-out pages.
	handle("GET /setup", signedOut, s.setupForm)
	handle("POST /setup", signedOut, s.setupSubmit)
	handle("GET /login", signedOut, s.loginForm)
	handle("POST /login", signedOut, s.loginSubmit)
	handle("GET /login/code", signedOut, s.loginCodeForm)
	handle("POST /login/code", signedOut, s.loginCodeSubmit)
	handle("GET /reset/{token}", signedOut, s.resetForm)
	handle("POST /reset/{token}", signedOut, s.resetSubmit)
	handle("GET /invite/{token}", signedOut, s.inviteForm)
	handle("POST /invite/{token}", signedOut, s.inviteSubmit)

	// Signed-in pages.
	handle("POST /logout", member, s.logout)
	handle("GET /account", member, s.accountPage)
	handle("POST /account/profile", member, s.accountProfile)
	handle("POST /account/password", member, s.accountPassword)
	handle("POST /account/two-step/start", member, s.twoStepStart)
	handle("POST /account/two-step/confirm", member, s.twoStepConfirm)
	handle("POST /account/two-step/codes", member, s.twoStepCodes)
	handle("POST /account/two-step/off", member, s.twoStepOff)
	handle("POST /account/tokens", member, s.tokenCreate)
	handle("POST /account/tokens/{id}/delete", member, s.tokenDelete)
	handle("GET /sys/mem", member, s.memReadout)

	handle("GET /{$}", member, s.home)
	handle("GET /home/live", member, s.homeLive)
	handle("GET /projects", member, s.projectList)
	handle("POST /projects", member, s.projectCreate)
	handle("GET /projects/{id}", member, s.projectShow)
	handle("GET /projects/{id}/e/{env}", member, s.environmentShow)
	handle("GET /projects/{id}/e/{env}/new", member, s.resourceNew)
	handle("GET /projects/{id}/switch/environments", member, s.switchEnvironments)
	handle("GET /environments/{id}/switch/resources", member, s.switchResources)
	handle("POST /projects/{id}", member, s.projectUpdate)
	handle("GET /projects/{id}/settings", member, s.projectSettings)
	handle("POST /projects/{id}/delete", member, s.projectDelete)
	handle("POST /projects/{id}/environments", member, s.environmentCreate)
	handle("POST /environments/{id}/delete", member, s.environmentDelete)
	handle("GET /projects/{id}/variables", member, s.sharedShow(s.sharedProject))
	handle("POST /projects/{id}/variables", member, s.sharedSave(s.sharedProject))
	handle("GET /environments/{id}/variables", member, s.sharedShow(s.sharedEnvironment))
	handle("POST /environments/{id}/variables", member, s.sharedSave(s.sharedEnvironment))

	handle("GET /projects/{id}/e/{env}/apps/new", member, s.appNew)
	handle("POST /projects/{id}/e/{env}/apps", member, s.appCreate)
	handle("GET /apps/{id}", member, s.appOverview)
	handle("GET /apps/{id}/status", member, s.appStatus)
	handle("POST /apps/{id}/deploy", member, s.appDeploy)
	handle("POST /apps/{id}/stop", member, s.appStop)
	handle("GET /apps/{id}/deployments", member, s.appDeployments)
	handle("GET /apps/{id}/deployments/{dep}", member, s.appDeployment)
	handle("GET /apps/{id}/deployments/{dep}/status", member, s.appDeploymentStatus)
	handle("POST /apps/{id}/deployments/{dep}/rollback", member, s.appRollback)
	handle("GET /apps/{id}/deployments/{dep}/stream", member, s.appDeploymentStream)
	handle("GET /apps/{id}/logs", member, s.appLogs)
	handle("GET /apps/{id}/logs/stream", member, s.appLogsStream)
	handle("GET /apps/{id}/environment", member, s.ownSettings(s.appEnvironment))
	handle("POST /apps/{id}/environment", member, s.ownSettings(s.appEnvironmentSave))
	handle("GET /apps/{id}/storage", member, s.ownSettings(s.appStorage))
	handle("POST /apps/{id}/storage", member, s.ownSettings(s.appStorageAdd))
	handle("POST /apps/{id}/storage/{sid}/delete", member, s.ownSettings(s.appStorageDelete))
	handle("GET /apps/{id}/settings", member, s.appSettings)
	handle("POST /apps/{id}/settings", member, s.ownSettings(s.appSettingsSave))
	handle("POST /apps/{id}/domains", member, s.ownSettings(s.appDomainAdd))
	handle("POST /apps/{id}/domains/{did}/delete", member, s.ownSettings(s.appDomainDelete))
	handle("POST /apps/{id}/delete", member, s.appDelete)
	handle("GET /apps/{id}/tasks", member, s.ownSettings(s.appTasks))
	handle("POST /apps/{id}/tasks", member, s.ownSettings(s.appTaskCreate))
	handle("GET /apps/{id}/tasks/{tid}", member, s.ownSettings(s.appTask))
	handle("POST /apps/{id}/tasks/{tid}", member, s.ownSettings(s.appTaskSave))
	handle("GET /apps/{id}/tasks/{tid}/runs", member, s.ownSettings(s.appTaskRuns))
	handle("POST /apps/{id}/tasks/{tid}/run", member, s.ownSettings(s.appTaskRun))
	handle("POST /apps/{id}/tasks/{tid}/delete", member, s.ownSettings(s.appTaskDelete))
	handle("POST /apps/{id}/source", member, s.ownSettings(s.appSourceSave))
	handle("POST /apps/{id}/webhook-secret", member, s.ownSettings(s.appWebhookSecret))
	handle("POST /apps/{id}/deploy-token", member, s.ownSettings(s.appDeployToken))
	handle("POST /apps/{id}/build-server", member, s.ownSettings(s.appBuildServer))
	handle("POST /apps/{id}/tags", member, s.ownSettings(s.appTagsSave))
	handle("POST /apps/{id}/previews", member, s.appPreviewsSave)
	handle("POST /apps/{id}/previews/{number}/delete", member, s.appPreviewDelete)

	handle("GET /projects/{id}/e/{env}/databases/new", member, s.databaseNew)
	handle("POST /projects/{id}/e/{env}/databases", member, s.databaseCreate)
	handle("GET /databases/{id}", member, s.databaseOverview)
	handle("GET /databases/{id}/status", member, s.databaseStatus)
	handle("POST /databases/{id}/start", member, s.databaseStart)
	handle("POST /databases/{id}/stop", member, s.databaseStop)
	handle("GET /databases/{id}/logs", member, s.databaseLogs)
	handle("GET /databases/{id}/logs/stream", member, s.databaseLogsStream)
	handle("GET /databases/{id}/settings", member, s.databaseSettings)
	handle("POST /databases/{id}/settings", member, s.databaseSettingsSave)
	handle("POST /databases/{id}/delete", member, s.databaseDelete)
	handle("GET /databases/{id}/backups", member, s.databaseBackups)
	handle("POST /databases/{id}/backups", member, s.databaseBackupNow)
	handle("GET /databases/{id}/backups/list", member, s.databaseBackupList)
	handle("POST /databases/{id}/backups/schedule", member, s.databaseBackupSchedule)
	handle("GET /databases/{id}/backups/{bid}/download", member, s.databaseBackupDownload)
	handle("POST /databases/{id}/backups/{bid}/restore", member, s.databaseBackupRestore)
	handle("POST /databases/{id}/backups/{bid}/delete", member, s.databaseBackupDelete)

	handle("GET /projects/{id}/e/{env}/services/new", member, s.serviceNew)
	handle("POST /projects/{id}/e/{env}/services", member, s.serviceCreate)
	handle("GET /services/{id}", member, s.serviceOverview)
	handle("GET /services/{id}/status", member, s.serviceStatus)
	handle("POST /services/{id}/deploy", member, s.serviceDeploy)
	handle("POST /services/{id}/stop", member, s.serviceStop)
	handle("GET /services/{id}/deploy-log/stream", member, s.serviceDeployLog)
	handle("GET /services/{id}/logs", member, s.serviceLogs)
	handle("GET /services/{id}/logs/stream", member, s.serviceLogsStream)
	handle("GET /services/{id}/compose", member, s.serviceCompose)
	handle("POST /services/{id}/compose", member, s.serviceComposeSave)
	handle("GET /services/{id}/settings", member, s.serviceSettings)
	handle("POST /services/{id}/source", member, s.serviceSourceSave)
	handle("POST /services/{id}/webhook-secret", member, s.serviceWebhookSecret)
	handle("POST /services/{id}/deploy-token", member, s.serviceDeployToken)
	handle("POST /services/{id}/endpoints/{eid}", member, s.serviceEndpointSave)
	handle("POST /services/{id}/tags", member, s.serviceTagsSave)
	handle("POST /services/{id}/delete", member, s.serviceDelete)

	handle("GET /tags", member, s.tagList)
	handle("POST /tags", member, s.tagCreate)
	handle("GET /tags/{tag}", member, s.tagShow)
	handle("POST /tags/{tag}", member, s.tagRename)
	handle("POST /tags/{tag}/delete", member, s.tagDelete)
	handle("POST /tags/{tag}/deploy", member, s.tagDeploy)

	handle("GET /sources", member, s.sourcesPage)
	handle("POST /sources/github", admin, s.githubStart)
	handle("GET /sources/github/callback", admin, s.githubCallback)
	handle("GET /sources/github/installed", member, s.githubInstalled)
	handle("GET /sources/github/{id}/repos", member, s.githubRepos)
	handle("POST /sources/github/{id}/delete", admin, s.githubDelete)
	handle("POST /sources/keys", admin, s.sshKeyCreate)
	handle("POST /sources/keys/{id}/delete", admin, s.sshKeyDelete)

	handle("GET /servers", member, s.serverList)
	handle("POST /servers/{id}", admin, s.serverUpdate)
	handle("POST /servers", admin, s.serverCreate)
	handle("POST /servers/{id}/check", admin, s.serverCheck)
	handle("POST /servers/{id}/proxy", admin, s.serverProxy)
	handle("POST /servers/{id}/forget-host-key", admin, s.serverForgetHostKey)
	handle("POST /servers/{id}/delete", admin, s.serverDelete)
	handle("GET /servers/{id}/variables", member, s.sharedShow(s.sharedServer))
	handle("POST /servers/{id}/variables", admin, s.sharedSave(s.sharedServer))
	handle("GET /team", member, s.teamPage)
	handle("POST /team", admin, s.teamRename)
	handle("GET /team/variables", member, s.sharedShow(s.sharedTeam))
	handle("POST /team/variables", admin, s.sharedSave(s.sharedTeam))
	handle("POST /team/invitations", admin, s.invitationCreate)
	handle("POST /team/invitations/{id}/delete", admin, s.invitationDelete)
	handle("POST /team/members/{id}/role", owner, s.memberRole)
	handle("POST /team/members/{id}/reset", admin, s.memberReset)
	handle("POST /team/members/{id}/two-step-off", admin, s.memberTwoStepOff)
	handle("POST /team/members/{id}/delete", admin, s.memberRemove)

	handle("GET /settings", admin, s.settingsPage)
	handle("POST /settings", admin, s.settingsSave)
	handle("GET /settings/storages", admin, s.storagesPage)
	handle("POST /settings/storages", admin, s.storageCreate)
	handle("POST /settings/storages/{sid}/test", admin, s.storageTest)
	handle("POST /settings/storages/{sid}/delete", admin, s.storageDelete)
	handle("GET /settings/notifications", admin, s.notificationsPage)
	handle("POST /settings/notifications", admin, s.notificationCreate)
	handle("POST /settings/notifications/{cid}", admin, s.notificationSave)
	handle("POST /settings/notifications/{cid}/test", admin, s.notificationTest)
	handle("POST /settings/notifications/{cid}/delete", admin, s.notificationDelete)

	s.extraRoutes(handle)

	if s.Cfg.Dev {
		handle("GET /_ui", member, s.gallery)
		handle("POST /_ui", member, s.gallery)
		handle("GET /_ui/switch", member, s.gallerySwitch)
	}
	if s.Pprof {
		mux.Handle("/debug/pprof/", loopbackOnly(http.HandlerFunc(pprof.Index)))
		mux.Handle("/debug/pprof/cmdline", loopbackOnly(http.HandlerFunc(pprof.Cmdline)))
		mux.Handle("/debug/pprof/profile", loopbackOnly(http.HandlerFunc(pprof.Profile)))
		mux.Handle("/debug/pprof/symbol", loopbackOnly(http.HandlerFunc(pprof.Symbol)))
		mux.Handle("/debug/pprof/trace", loopbackOnly(http.HandlerFunc(pprof.Trace)))
	}

	// Anything else is a 404 inside the right frame.
	handle("/", signedOut, s.notFound)

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
		Admin:  db.RoleRank(sess.Role) >= db.RoleRank(db.RoleAdmin),
		Owner:  sess.Role == db.RoleOwner,
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

func (s *Server) gallerySwitch(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, pages.GallerySwitch())
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
