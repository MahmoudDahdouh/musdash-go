// Package web is the control plane's HTTP layer: pages, forms and live
// updates, all rendered on the server.
package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"net/netip"
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
	// GitLab is the client for a GitLab instance's API. Nil uses one that
	// connects only where netguard allows.
	GitLab *source.GitLab
	// Resolve looks a host name up for Check DNS. Nil uses the system's
	// resolver; tests put their own in.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)

	logins *auth.Limiter
	// accounts counts attempts at one account's password and second step.
	// Its keys are accounts that exist, so nobody outside can fill it.
	accounts *auth.Limiter
	// sent remembers, for a day, the forms that were acted on and must not
	// be acted on again when a browser sends them a second time: see
	// sentBefore.
	sent *auth.Limiter
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
	s.sent = auth.NewLimiter(1, 24*time.Hour)
	s.hooks = auth.NewLimiter(120, time.Minute)
	s.apiCalls = auth.NewLimiter(apiPerMinute, time.Minute)
	s.apiAddrs = auth.NewLimiter(apiPerMinuteByAddress, time.Minute)
	if s.GitHub == nil {
		s.GitHub = source.NewGitHub()
	}
	if s.GitLab == nil {
		s.GitLab = source.NewGitLab()
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
	// Each route names the one permission its token must have.
	read, write, deploys := db.AbilityRead, db.AbilityWrite, db.AbilityDeploy
	handle("GET /api/v1/me", open, s.api(read, s.apiMe))
	handle("GET /api/v1/servers", open, s.api(read, s.apiServers))
	handle("GET /api/v1/projects", open, s.api(read, s.apiProjects))
	handle("GET /api/v1/tags", open, s.api(read, s.apiTags))
	handle("GET /api/v1/apps", open, s.api(read, s.apiApps))
	handle("GET /api/v1/apps/{id}", open, s.api(read, s.apiApp))
	handle("GET /api/v1/apps/{id}/deployments", open, s.api(read, s.apiAppDeployments))
	handle("GET /api/v1/apps/{id}/envs", open, s.api(read, s.apiAppEnvs))
	handle("POST /api/v1/apps/{id}/deploy", open, s.api(deploys, s.apiAppDeploy))
	handle("POST /api/v1/apps/{id}/stop", open, s.api(write, s.apiAppStop))
	handle("GET /api/v1/deployments/{id}", open, s.api(read, s.apiDeployment))
	handle("GET /api/v1/databases", open, s.api(read, s.apiDatabases))
	handle("GET /api/v1/databases/{id}", open, s.api(read, s.apiDatabase))
	handle("POST /api/v1/databases/{id}/start", open, s.api(write, s.apiDatabaseStart))
	handle("POST /api/v1/databases/{id}/stop", open, s.api(write, s.apiDatabaseStop))
	handle("GET /api/v1/services", open, s.api(read, s.apiServices))
	handle("GET /api/v1/services/{id}", open, s.api(read, s.apiService))
	handle("POST /api/v1/services/{id}/deploy", open, s.api(deploys, s.apiServiceDeploy))
	handle("POST /api/v1/services/{id}/stop", open, s.api(write, s.apiServiceStop))
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

	handle("GET /{$}", member, s.home)
	handle("GET /home/live", member, s.homeLive)
	handle("GET /projects", member, s.projectList)
	handle("POST /projects", member, s.projectCreate)
	handle("GET /projects/{id}", member, s.projectShow)
	handle("GET "+inEnv, member, s.environmentShow)
	handle("GET "+inEnv+"/new", member, s.resourceNew)
	handle("GET /projects/{id}/switch/environments", member, s.switchEnvironments)
	handle("GET "+inEnv+"/switch/resources", member, s.switchResources)
	handle("GET /switch/teams", member, s.switchTeams)
	handle("GET /switch/projects", member, s.switchProjects)
	handle("GET /search", member, s.search)
	handle("GET /switch/tags", member, s.switchTags)
	handle("POST /projects/{id}", member, s.projectUpdate)
	handle("GET /projects/{id}/settings", member, s.projectSettings)
	handle("POST /projects/{id}/delete", member, s.projectDelete)
	handle("POST /projects/{id}/environments", member, s.environmentCreate)
	handle("GET "+inEnv+"/settings", member, s.environmentSettings)
	handle("POST "+inEnv+"/settings", member, s.environmentRename)
	handle("POST "+inEnv+"/delete", member, s.environmentDelete)
	handle("GET /projects/{id}/variables", member, s.sharedShow(s.sharedProject))
	handle("GET /projects/{id}/variables/values", member, s.sharedValues(s.sharedProject))
	handle("GET /projects/{id}/variables/edit", member, s.sharedEdit(s.sharedProject))
	handle("POST /projects/{id}/variables", member, s.sharedSave(s.sharedProject))
	handle("GET "+inEnv+"/variables", member, s.sharedShow(s.sharedEnvironment))
	handle("GET "+inEnv+"/variables/values", member, s.sharedValues(s.sharedEnvironment))
	handle("GET "+inEnv+"/variables/edit", member, s.sharedEdit(s.sharedEnvironment))
	handle("POST "+inEnv+"/variables", member, s.sharedSave(s.sharedEnvironment))

	// Every route of a resource says where the resource is, and is
	// answered only when that is where it is (place.go).
	resource := func(kind string) resourceRoute {
		return func(method, rest string, who access, h http.HandlerFunc) {
			handle(method+" "+inEnv+"/"+kind+"/{id}"+rest, who, s.placed(kind, h))
		}
	}
	app, database, service := resource(db.KindApp), resource(db.KindDatabase), resource(db.KindService)

	// The short address of a resource, and the addresses an environment
	// had, lead to the ones above.
	for kind, plural := range map[string]string{db.KindApp: "apps", db.KindDatabase: "databases", db.KindService: "services"} {
		handle("GET /"+plural+"/{id}", member, s.short(kind))
		handle("GET /"+plural+"/{id}/{rest...}", member, s.short(kind))
	}
	handle("GET /projects/{id}/e/{env}", member, s.oldEnvironment)
	handle("GET /projects/{id}/e/{env}/{rest...}", member, s.oldEnvironment)
	handle("GET /environments/{id}/variables", member, s.oldEnvironmentVariables)

	handle("GET "+inEnv+"/app/new", member, s.appNew)
	handle("POST "+inEnv+"/app", member, s.appCreate)
	app("GET", "", member, s.appOverview)
	app("GET", "/status", member, s.appStatus)
	app("POST", "/deploy", member, s.appDeploy)
	app("POST", "/stop", member, s.appStop)
	app("GET", "/deployments", member, s.appDeployments)
	app("GET", "/deployments/{dep}", member, s.appDeployment)
	app("GET", "/deployments/{dep}/status", member, s.appDeploymentStatus)
	app("POST", "/deployments/{dep}/rollback", member, s.appRollback)
	app("GET", "/deployments/{dep}/stream", member, s.appDeploymentStream)
	app("GET", "/logs", member, s.appLogs)
	app("GET", "/logs/stream", member, s.appLogsStream)
	app("GET", "/environment", member, s.ownSettings(s.appEnvironment))
	app("GET", "/environment/values", member, s.ownSettings(s.appEnvironmentValues))
	app("GET", "/environment/edit", member, s.ownSettings(s.appEnvironmentEdit))
	app("POST", "/environment", member, s.ownSettings(s.appEnvironmentSave))
	app("GET", "/storage", member, s.ownSettings(s.appStorage))
	app("POST", "/storage", member, s.ownSettings(s.appStorageAdd))
	app("POST", "/storage/{sid}/delete", member, s.ownSettings(s.appStorageDelete))
	app("GET", "/settings", member, s.appSettings)
	app("POST", "/settings", member, s.ownSettings(s.appSettingsSave))
	app("GET", "/domains", member, s.ownSettings(s.appDomains))
	app("POST", "/domains", member, s.ownSettings(s.appDomainAdd))
	app("GET", "/domains/{did}/dns", member, s.ownSettings(s.appDomainDNS))
	app("POST", "/domains/{did}/delete", member, s.ownSettings(s.appDomainDelete))
	app("POST", "/delete", member, s.appDelete)
	app("GET", "/tasks", member, s.ownSettings(s.appTasks))
	app("POST", "/tasks", member, s.ownSettings(s.appTaskCreate))
	app("GET", "/tasks/{tid}", member, s.ownSettings(s.appTask))
	app("POST", "/tasks/{tid}", member, s.ownSettings(s.appTaskSave))
	app("GET", "/tasks/{tid}/runs", member, s.ownSettings(s.appTaskRuns))
	app("POST", "/tasks/{tid}/run", member, s.ownSettings(s.appTaskRun))
	app("POST", "/tasks/{tid}/delete", member, s.ownSettings(s.appTaskDelete))
	app("POST", "/source", member, s.ownSettings(s.appSourceSave))
	app("GET", "/webhook-secret", member, s.ownSettings(s.appWebhookShow))
	app("POST", "/webhook-secret", member, s.ownSettings(s.appWebhookSecret))
	app("POST", "/deploy-token", member, s.ownSettings(s.appDeployToken))
	app("POST", "/build-server", member, s.ownSettings(s.appBuildServer))
	app("POST", "/tags", member, s.ownSettings(s.appTagsSave))
	app("POST", "/previews", member, s.appPreviewsSave)
	app("POST", "/previews/{number}/delete", member, s.appPreviewDelete)

	handle("GET "+inEnv+"/database/new", member, s.databaseNew)
	handle("POST "+inEnv+"/database", member, s.databaseCreate)
	database("GET", "", member, s.databaseOverview)
	database("GET", "/status", member, s.databaseStatus)
	database("POST", "/start", member, s.databaseStart)
	database("POST", "/stop", member, s.databaseStop)
	database("GET", "/logs", member, s.databaseLogs)
	database("GET", "/logs/stream", member, s.databaseLogsStream)
	database("GET", "/settings", member, s.databaseSettings)
	database("POST", "/settings", member, s.databaseSettingsSave)
	database("POST", "/delete", member, s.databaseDelete)
	database("GET", "/backups", member, s.databaseBackups)
	database("POST", "/backups", member, s.databaseBackupNow)
	database("GET", "/backups/list", member, s.databaseBackupList)
	database("POST", "/backups/schedule", member, s.databaseBackupSchedule)
	database("GET", "/backups/{bid}/download", member, s.databaseBackupDownload)
	database("POST", "/backups/{bid}/restore", member, s.databaseBackupRestore)
	database("POST", "/backups/{bid}/delete", member, s.databaseBackupDelete)

	handle("GET "+inEnv+"/service/new", member, s.serviceNew)
	handle("POST "+inEnv+"/service", member, s.serviceCreate)
	service("GET", "", member, s.serviceOverview)
	service("GET", "/status", member, s.serviceStatus)
	service("POST", "/deploy", member, s.serviceDeploy)
	service("POST", "/stop", member, s.serviceStop)
	service("GET", "/deploy-log/stream", member, s.serviceDeployLog)
	service("GET", "/logs", member, s.serviceLogs)
	service("GET", "/logs/stream", member, s.serviceLogsStream)
	service("GET", "/compose", member, s.serviceCompose)
	service("GET", "/compose/variables", member, s.serviceComposeVariables)
	service("POST", "/compose", member, s.serviceComposeSave)
	service("GET", "/settings", member, s.serviceSettings)
	service("POST", "/source", member, s.serviceSourceSave)
	service("GET", "/webhook-secret", member, s.serviceWebhookShow)
	service("POST", "/webhook-secret", member, s.serviceWebhookSecret)
	service("POST", "/deploy-token", member, s.serviceDeployToken)
	service("POST", "/endpoints/{eid}", member, s.serviceEndpointSave)
	service("POST", "/tags", member, s.serviceTagsSave)
	service("POST", "/delete", member, s.serviceDelete)

	handle("GET /tags", member, s.tagList)
	handle("POST /tags", member, s.tagCreate)
	handle("GET /tags/{tag}", member, s.tagShow)
	handle("POST /tags/{tag}", member, s.tagRename)
	handle("POST /tags/{tag}/delete", member, s.tagDelete)

	handle("GET /keys", member, s.keysPage)
	handle("GET /keys/tokens", member, s.tokensPage)
	handle("POST /keys/deploy-tokens", member, s.keysDeployToken)
	handle("POST /keys/webhook-secrets", member, s.keysWebhookSecret)
	handle("GET /sources", member, s.sourcesPage)
	handle("POST /sources/github", admin, s.githubStart)
	handle("GET /sources/github/callback", admin, s.githubCallback)
	handle("GET /sources/github/installed", member, s.githubInstalled)
	handle("GET /sources/github/{id}/repos", member, s.githubRepos)
	handle("POST /sources/github/{id}/delete", admin, s.sourceDelete)
	handle("POST /sources/gitlab", admin, s.gitlabCreate)
	handle("GET /sources/gitlab/{id}/repos", member, s.gitlabRepos)
	handle("GET /sources/github/{id}/branches", member, s.repoBranches(db.GitSourceGitHubApp))
	handle("GET /sources/gitlab/{id}/branches", member, s.repoBranches(db.GitSourceGitLab))
	handle("GET /sources/detect", member, s.gitDetect)
	handle("POST /sources/gitlab/{id}/delete", admin, s.sourceDelete)
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
	handle("GET /servers/{id}/variables/values", admin, s.sharedValues(s.sharedServer))
	handle("GET /servers/{id}/variables/edit", admin, s.sharedEdit(s.sharedServer))
	handle("POST /servers/{id}/variables", admin, s.sharedSave(s.sharedServer))
	handle("GET /team", member, s.teamPage)
	handle("POST /team", admin, s.teamRename)
	handle("GET /team/variables", member, s.sharedShow(s.sharedTeam))
	handle("GET /team/variables/values", admin, s.sharedValues(s.sharedTeam))
	handle("GET /team/variables/edit", admin, s.sharedEdit(s.sharedTeam))
	handle("POST /team/variables", admin, s.sharedSave(s.sharedTeam))
	handle("GET /team/invitations", member, s.invitationsPage)
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
	handle("GET /notifications", admin, s.notificationsPage)
	handle("POST /notifications", admin, s.notificationCreate)
	handle("POST /notifications/{cid}", admin, s.notificationSave)
	handle("POST /notifications/{cid}/test", admin, s.notificationTest)
	handle("POST /notifications/{cid}/delete", admin, s.notificationDelete)

	s.extraRoutes(handle, app, database, service)

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

// renderPolled answers the poll of an element that asks for itself again
// (ui.Poll). The request says what the page holds; when a fresh rendering
// is the same, the answer is 204 and htmx leaves the page as it is, so an
// element is put into the page again only when it has something new.
//
// The fragment is rendered before anything is sent, since which answer it
// is depends on it. It is a part of a page, a few kilobytes.
func (s *Server) renderPolled(w http.ResponseWriter, r *http.Request, c templ.Component) {
	ctx, mark := ui.WatchPoll(r.Context())
	var page bytes.Buffer
	if err := c.Render(ctx, &page); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if seen := r.URL.Query().Get("seen"); seen != "" && seen == *mark {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page.Bytes())
}

// shell builds the frame data for a signed-in page and consumes the flash.
func (s *Server) shell(w http.ResponseWriter, r *http.Request, title, active string, crumbs ...ui.Crumb) ui.Shell {
	sess := sessionFrom(r)
	return ui.Shell{
		Title:  title,
		Active: active,
		Team:   sess.TeamName,
		Name:   sess.User.Name,
		Email:  sess.User.Email,
		Admin:  db.RoleRank(sess.Role) >= db.RoleRank(db.RoleAdmin),
		Owner:  sess.Role == db.RoleOwner,
		CSRF:   sess.CSRFToken,
		Crumbs: crumbs,
		Flash:  takeFlash(w, r),
		Dev:    s.Cfg.Dev,
	}
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

// sentBefore reports whether the form of this request was acted on
// already, and answers the request itself when it was.
//
// A secret that is shown once is rendered in the answer to the POST that
// made it, so the page a person then looks at is the answer to a POST, and
// a browser's Refresh sends that POST again. Without this, a refresh made
// a second token of the same name, a second key, or replaced the deploy
// token that had just been copied. Such a form carries a value of its own
// (ui.Once), and the handler asks here right before it writes: a value
// that was seen before changes nothing and leads back to the page.
//
// The check and the record are one step under the limiter's lock, so two
// POSTs at the same moment cannot both pass. A form without the value is
// acted on as ever: this keeps a browser from repeating itself, and CSRF is
// what keeps strangers out.
//
// A handler whose write then fails, or that finds only afterwards that the
// form cannot be acted on, gives the value back with notSent: the note a
// repeat gets says something was made, and must not be told to somebody
// for whom nothing was.
func (s *Server) sentBefore(w http.ResponseWriter, r *http.Request, back string) bool {
	key := onceKey(r)
	if key == "" {
		return false
	}
	if ok, _ := s.sent.Take(key); ok {
		return false
	}
	setFlash(w, r, ui.ToneWarn, "That form had been sent already, so nothing more was done. What it made was shown once: if you did not copy it, remove it and make another.")
	redirect(w, r, back)
	return true
}

// notSent forgets that the form of this request was acted on, because in
// the end it was not: sent again, it is a first time.
func (s *Server) notSent(r *http.Request) {
	if key := onceKey(r); key != "" {
		s.sent.Reset(key)
	}
}

// onceKey is what a form is remembered by: whose it is, and its own value.
// The value is hashed, since it is the browser's and the limiter's keys are
// short. A form that carries none has no key.
func onceKey(r *http.Request) string {
	once := r.PostFormValue(ui.OnceField)
	if once == "" {
		return ""
	}
	return sessionFrom(r).UserID + ":" + secret.HashToken(once)
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
