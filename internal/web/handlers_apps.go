package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"

	"golang.org/x/crypto/bcrypt"
)

const appNameRule = "Use lowercase letters, numbers and hyphens, up to 32 characters."

// maxDomains bounds how many domains one app may have, which in turn bounds
// the size of the proxy's routes file.
const maxDomains = 20

// detached returns a context for work that must finish even if the browser
// that asked for it goes away: withdrawing a route, removing a container.
func detached(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
}

// maxFileMount bounds the content of one file mount.
const maxFileMount = 256 << 10

// loadApp fetches the app in the path with its project, environment and
// domains, for the signed-in team. It answers 404 itself.
func (s *Server) loadApp(w http.ResponseWriter, r *http.Request) (pages.AppView, bool) {
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	var v pages.AppView
	app, err := s.DB.App(ctx, teamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, false
	}
	if err == nil {
		v.App = app
		v.Env, err = s.DB.Environment(ctx, teamID, app.EnvironmentID)
	}
	if err == nil {
		v.Project, err = s.DB.Project(ctx, teamID, v.Env.ProjectID)
	}
	if err == nil {
		v.Domains, err = s.DB.ListDomains(ctx, db.KindApp, app.ID)
	}
	if err == nil && app.IsPreview() {
		var parent db.App
		if parent, err = s.DB.App(ctx, teamID, app.PreviewOf); err == nil {
			v.Parent = &parent
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return v, false
	}
	return v, true
}

// appCrumbs is an app's trail. An app is a switcher to what else is in its
// environment; a preview is listed nowhere but with its parent, so its
// trail goes through the parent instead.
func appCrumbs(v pages.AppView) []ui.Crumb {
	if v.Parent != nil {
		return envCrumbs(v.Project, v.Env,
			ui.Crumb{Label: v.Parent.Name, Href: "/apps/" + v.Parent.ID, Icon: pages.KindIcon(db.KindApp)},
			ui.Crumb{Label: v.App.Name})
	}
	return envCrumbs(v.Project, v.Env, resourceCrumb(v.Env, db.KindApp, v.App.ID, v.App.Name))
}

func (s *Server) appShell(w http.ResponseWriter, r *http.Request, v pages.AppView) ui.Shell {
	return s.shell(w, r, v.App.Name, "projects", appCrumbs(v)...)
}

// sentence turns an error into a message for a form field: capitalised and
// ending with a full stop.
func sentence(err error) string {
	msg := err.Error()
	if msg == "" {
		return ""
	}
	msg = strings.ToUpper(msg[:1]) + msg[1:]
	if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "?") {
		msg += "."
	}
	return msg
}

// parsePort reads a port number field.
func parsePort(f *ui.Form, key, value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > 65535 {
		f.Fail(key, "Enter a port number between 1 and 65535.")
		return 0
	}
	return n
}

// parseLimits reads the memory and CPU limit fields. Empty means no limit.
func parseLimits(f *ui.Form, memory, cpus string) (memoryMB int, cores float64) {
	if memory != "" {
		// Docker refuses limits below 6 MB.
		if n, err := strconv.Atoi(memory); err != nil || n < 6 || n > 1<<20 {
			f.Fail("memory_mb", "Enter a whole number of megabytes, 6 or more, or leave it empty.")
		} else {
			memoryMB = n
		}
	}
	if cpus != "" {
		// Written so that NaN, which compares false with everything, fails.
		if c, err := strconv.ParseFloat(cpus, 64); err != nil || !(c >= 0.01 && c <= 512) {
			f.Fail("cpus", "Enter a number of cores such as 0.5 or 2, or leave it empty.")
		} else {
			cores = c
		}
	}
	return memoryMB, cores
}

// generatedDomain builds an address that resolves to the server without any
// DNS setup: sslip.io answers <anything>.<ip>.sslip.io with <ip>.
func generatedDomain(server db.Server) string { return deploy.GeneratedDomain(server) }

// isGeneratedDomain reports whether a host is one of the shared wildcard
// DNS names. They are served over plain HTTP: the certificate authority
// limits how many certificates such a shared suffix may get, and a refused
// certificate would look like a broken deploy.
func isGeneratedDomain(host string) bool {
	return strings.HasSuffix(host, ".sslip.io") || strings.HasSuffix(host, ".nip.io")
}

// checkHost normalises and validates a host name for routing.
//
// The dashboard's own address is refused whatever the path: a page served
// from there would be the dashboard's origin to a browser, with its
// cookies.
func (s *Server) checkHost(ctx context.Context, f *ui.Form, key, value string) string {
	host := proxy.NormalizeHost(value)
	if !proxy.ValidHost(host) || !strings.Contains(host, ".") {
		f.Fail(key, "Enter a domain such as app.example.com, without http:// or a path.")
		return ""
	}
	instance, err := s.DB.Setting(ctx, db.SettingInstanceDomain)
	if err != nil {
		s.Log.Error("read the dashboard's domain", "err", err)
		f.Fail(key, "The domain could not be checked just now. Try again.")
		return ""
	}
	if instance == host {
		f.Fail(key, "This domain is the dashboard's own address.")
		return ""
	}
	return host
}

// domainTaken is what a person is told when a host, or the path of it they
// asked for, cannot be theirs. It says the same whoever has it.
const domainTaken = "This domain is already routed to something on this install."

// checkDomain is checkHost for a resource that takes a whole host: nothing
// else may be routed on it.
func (s *Server) checkDomain(ctx context.Context, f *ui.Form, key, value string) string {
	host := s.checkHost(ctx, f, key, value)
	if host == "" {
		return ""
	}
	if used, err := s.DB.HostInUse(ctx, host); err == nil && used {
		f.Fail(key, domainTaken)
		return ""
	}
	return host
}

// Limits of the user name and password in front of a domain. bcrypt reads
// 72 bytes of a password and no more.
const (
	maxDomainUser     = 64
	minDomainPassword = 8
	maxDomainPassword = 72
	// domainHashCost is what one sign-in costs the proxy, which checks
	// passwords one at a time: less than the dashboard's own, because the
	// proxy also has every site's traffic to serve.
	domainHashCost = 10
)

// domainPath normalises a path prefix as a person types it: "api/" is
// "/api".
func domainPath(f *ui.Form, key, value string) string {
	p := strings.TrimSpace(value)
	if p == "" || p == "/" {
		return ""
	}
	p = "/" + strings.Trim(p, "/")
	if !proxy.ValidPath(p) {
		f.Fail(key, "Enter a path such as /api. It cannot hold spaces, %, ?, # or dots that climb.")
		return ""
	}
	return p
}

// domainAuth validates the user name and password for a domain and returns
// the name with the password's hash, or two empty strings when neither was
// given.
func domainAuth(f *ui.Form, user, password string) (string, string) {
	user = strings.TrimSpace(user)
	if user == "" && password == "" {
		return "", ""
	}
	if user == "" || len(user) > maxDomainUser || strings.ContainsFunc(user, func(c rune) bool { return c == ':' || c < ' ' || c == 0x7f }) {
		f.Fail("auth_user", "Enter a user name of up to 64 characters without a colon.")
	}
	if len(password) < minDomainPassword || len(password) > maxDomainPassword {
		f.Fail("auth_password", "Enter a password of 8 to 72 characters.")
	}
	if !f.OK() {
		return "", ""
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), domainHashCost)
	if err != nil {
		f.Fail("auth_password", "This password cannot be used.")
		return "", ""
	}
	return user, string(hash)
}

// newAppShell frames the New app form.
func (s *Server) newAppShell(w http.ResponseWriter, r *http.Request, p db.Project, env db.Environment) ui.Shell {
	return s.shell(w, r, "New app", "projects", envCrumbs(p, env,
		ui.Crumb{Label: "Add resource", Href: envPath(p.ID, env.ID) + "/new"}, ui.Crumb{Label: "App"})...)
}

func (s *Server) appNew(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return
	}
	teamID := sessionFrom(r).TeamID
	serverList, err := s.serverChoices(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	server := serverList[0]
	src := db.SourceImage
	if r.URL.Query().Get("source") == db.SourceGit {
		src = db.SourceGit
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The Add resource page may have asked for a way into the repository.
	var f ui.Form
	if access := preferredAccess(r, choices); access != "" {
		f.Set("access", access)
	}
	s.render(w, r, http.StatusOK, pages.AppNew(s.newAppShell(w, r, p, env), p, env, f, generatedDomain(server), src, choices, serverList))
}

func (s *Server) appCreate(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	serverList, err := s.serverChoices(ctx, teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	src := db.SourceImage
	if r.PostFormValue("source") == db.SourceGit {
		src = db.SourceGit
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var f ui.Form
	server := targetServer(r, &f, serverList)
	newApp := db.App{EnvironmentID: env.ID, ServerID: server.ID, Source: src}
	newApp.Name = strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	rawDomain := strings.TrimSpace(r.PostFormValue("domain"))
	// The address suggested in the form was built for this machine. On
	// another server it would name the wrong one.
	if server.ID != serverList[0].ID && isGeneratedDomain(rawDomain) {
		rawDomain = generatedDomain(server)
	}
	f.Set("name", newApp.Name)
	f.Set("port", r.PostFormValue("port"))
	f.Set("domain", rawDomain)

	if !envNameRE.MatchString(newApp.Name) {
		f.Fail("name", appNameRule)
	}
	if src == db.SourceGit {
		parseGitForm(r, &f, choices, &newApp)
	} else {
		newApp.Image = strings.TrimSpace(r.PostFormValue("image"))
		f.Set("image", newApp.Image)
		if !docker.ValidImage(newApp.Image) {
			f.Fail("image", "Enter an image name such as nginx:alpine or ghcr.io/you/app:1.4.")
		}
	}
	newApp.Port = parsePort(&f, "port", r.PostFormValue("port"))
	if newApp.BuildPack == deploy.PackStatic {
		// The generated static image always listens on 80.
		newApp.Port = 80
	}
	host := ""
	if rawDomain != "" {
		host = s.checkDomain(ctx, &f, "domain", rawDomain)
	}

	rerender := func(status int) {
		s.render(w, r, status, pages.AppNew(s.newAppShell(w, r, p, env), p, env, f, rawDomain, src, choices, serverList))
	}
	if !f.OK() {
		rerender(http.StatusUnprocessableEntity)
		return
	}

	app, err := s.DB.CreateApp(ctx, teamID, newApp)
	if db.IsUnique(err) || errors.Is(err, db.ErrNameTaken) {
		f.Fail("name", "This environment already has an app, database or service called "+newApp.Name+".")
		rerender(http.StatusUnprocessableEntity)
		return
	}
	if errors.Is(err, db.ErrNotFound) {
		// The GitHub App or deploy key is not this team's.
		f.Fail("access", "Choose how the repository is read.")
		rerender(http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	domainTaken := false
	if host != "" {
		_, err := s.DB.AddDomain(ctx, teamID, app.ServerID, db.Domain{ResourceKind: db.KindApp, ResourceID: app.ID, Host: host, TLS: !isGeneratedDomain(host)})
		switch {
		case db.IsUnique(err), errors.Is(err, db.ErrHostTaken), errors.Is(err, db.ErrHostElsewhere):
			// Taken between the check above and now. The app exists; say
			// that its domain still has to be added.
			domainTaken = true
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}

	if r.PostFormValue("deploy") == "1" {
		dep, err := s.Deploy.Enqueue(ctx, app, "manual")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		redirect(w, r, "/apps/"+app.ID+"/deployments/"+dep.ID)
		return
	}
	if domainTaken {
		setFlash(w, r, ui.ToneWarn, "App created, but "+host+" was taken in the meantime. Add another on its Domains tab.")
	} else {
		setFlash(w, r, ui.ToneOK, "App created. Choose Deploy to start it.")
	}
	redirect(w, r, "/apps/"+app.ID)
}

func (s *Server) appOverview(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	server, err := s.DB.ServerByID(r.Context(), v.App.ServerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.DB.ListDeployments(r.Context(), v.App.ID, 1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var last *db.Deployment
	if len(list) > 0 {
		last = &list[0]
	}
	s.render(w, r, http.StatusOK, pages.AppOverview(s.appShell(w, r, v), v, server, last))
}

// appStatus serves the header fragment that app pages poll.
func (s *Server) appStatus(w http.ResponseWriter, r *http.Request) {
	app, err := s.DB.App(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.AppHeader(sessionFrom(r).CSRFToken, app))
}

func (s *Server) appDeploy(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	dep, err := s.Deploy.Enqueue(r.Context(), v.App, "manual")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/apps/"+v.App.ID+"/deployments/"+dep.ID)
}

func (s *Server) appStop(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	err := s.Deploy.Stop(r.Context(), v.App.ID)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "A deployment is in progress. Stop the app once it has finished.")
	case err != nil:
		s.Log.Error("stop app", "app", v.App.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The app could not be stopped: "+err.Error())
	default:
		setFlash(w, r, ui.ToneOK, "App stopped.")
	}
	redirect(w, r, "/apps/"+v.App.ID)
}

func (s *Server) appDeployments(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	list, err := s.DB.ListDeployments(r.Context(), v.App.ID, 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.AppDeployments(s.appShell(w, r, v), v, list))
}

// appRollback runs an earlier deployment's image again.
func (s *Server) appRollback(w http.ResponseWriter, r *http.Request) {
	v, to, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	dep, err := s.Deploy.Rollback(r.Context(), v.App, to)
	if errors.Is(err, deploy.ErrNoRollback) {
		setFlash(w, r, ui.ToneWarn, "This deployment cannot be rolled back to: it did not succeed, or its image was not kept.")
		redirect(w, r, "/apps/"+v.App.ID+"/deployments/"+to.ID)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/apps/"+v.App.ID+"/deployments/"+dep.ID)
}

// loadDeployment fetches the deployment in the path, which must belong to
// the app in the path.
func (s *Server) loadDeployment(w http.ResponseWriter, r *http.Request) (pages.AppView, db.Deployment, bool) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return v, db.Deployment{}, false
	}
	dep, err := s.DB.Deployment(r.Context(), v.App.ID, r.PathValue("dep"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, dep, false
	}
	if err != nil {
		s.fail(w, r, err)
		return v, dep, false
	}
	return v, dep, true
}

func (s *Server) appDeployment(w http.ResponseWriter, r *http.Request) {
	v, dep, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, pages.AppDeployment(s.appShell(w, r, v), v, dep))
}

func (s *Server) appDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	v, dep, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, pages.DeploymentStatus(v.App.ID, dep))
}

// streamContext is the context of a live stream: it ends when the browser
// leaves and also when musdash starts shutting down. Without the second, an
// open log view would hold the shutdown for its whole grace period.
func (s *Server) streamContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	if s.Closing == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(s.Closing, cancel)
	return ctx, func() { stop(); cancel() }
}

// takeStream reserves one of the live-stream slots, answering 503 when all
// are in use.
func (s *Server) takeStream(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case s.streams <- struct{}{}:
		return func() { <-s.streams }, true
	default:
		w.Header().Set("Retry-After", "10")
		http.Error(w, "Too many live log views are open. Close one and try again.", http.StatusServiceUnavailable)
		return nil, false
	}
}

// appDeploymentStream follows a deployment's log file as Server-Sent Events
// until the deployment finishes or the browser leaves.
func (s *Server) appDeploymentStream(w http.ResponseWriter, r *http.Request) {
	_, dep, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	release, ok := s.takeStream(w)
	if !ok {
		return
	}
	defer release()

	ctx, stop := s.streamContext(r)
	defer stop()
	out := startSSE(w)
	finished := func() bool {
		cur, err := s.DB.DeploymentByID(ctx, dep.ID)
		return err != nil || cur.Status == db.DeploySuccess || cur.Status == db.DeployFailed
	}
	// The log file appears when the job starts; a queued deployment has none.
	path := s.Cfg.DeployLogPath(dep.ID)
	err := deploy.FollowWhenReady(ctx, path, out, finished)
	if err == nil {
		out.finish()
	}
}

func (s *Server) appLogs(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.render(w, r, http.StatusOK, pages.AppLogs(s.appShell(w, r, v), v))
	}
}

// appLogsStream streams the running container's output. The docker process
// behind it is killed when the browser disconnects.
func (s *Server) appLogsStream(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	if v.App.Container == "" {
		http.Error(w, "Nothing is running.", http.StatusConflict)
		return
	}
	server, err := s.DB.ServerByID(r.Context(), v.App.ServerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	dk, err := s.Pool.Docker(r.Context(), server)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	release, ok := s.takeStream(w)
	if !ok {
		return
	}
	defer release()

	ctx, stop := s.streamContext(r)
	defer stop()
	out := startSSE(w)
	// Returns when the container stops or the stream is cancelled.
	dk.Logs(ctx, v.App.Container, 200, true, out)
	if ctx.Err() == nil {
		out.finish()
	}
}

// appVars lists an app's variables in their two groups. Their values are
// opened only when asked for: without that, what is returned holds names
// and nothing a page could leak.
func (s *Server) appVars(r *http.Request, v pages.AppView, shown bool) (runtime, build []db.EnvVar, err error) {
	sealed, err := s.DB.ListEnvVars(r.Context(), db.KindApp, v.App.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, ev := range sealed {
		plain := ""
		if shown {
			if plain, err = s.Box.OpenString(ev.Value); err != nil {
				return nil, nil, errors.New("environment variable " + ev.Key + " cannot be decrypted")
			}
		}
		if ev.BuildTime {
			build = append(build, db.EnvVar{Key: ev.Key, Value: plain})
		} else {
			runtime = append(runtime, db.EnvVar{Key: ev.Key, Value: plain})
		}
	}
	return runtime, build, nil
}

// appVarsCard is the list on the Environment tab, with values or without.
func (s *Server) appVarsCard(r *http.Request, v pages.AppView, shown bool) (pages.VarsCard, error) {
	runtime, build, err := s.appVars(r, v, shown)
	c := pages.VarsCard{ID: "app-vars", Title: "Environment variables", Shown: shown,
		Values: "/apps/" + v.App.ID + "/environment/values", Edit: "/apps/" + v.App.ID + "/environment/edit",
		Groups: []pages.VarGroup{{Vars: runtime}}}
	if v.App.Source == db.SourceGit {
		c.Groups = []pages.VarGroup{{Title: "Given to the running app", Vars: runtime}, {Title: "Given to the build", Vars: build}}
	}
	return c, err
}

// appEnvironment is the Environment tab: names, and no value.
func (s *Server) appEnvironment(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	c, err := s.appVarsCard(r, v, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.AppEnvironment(s.appShell(w, r, v), v, c))
}

// appEnvironmentValues answers Show values and Hide values: the list again,
// with the values when they were asked for.
func (s *Server) appEnvironmentValues(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	c, err := s.appVarsCard(r, v, r.URL.Query().Get("hide") == "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.Variables(c))
}

// appEnvironmentEdit is the editor, which holds the values.
func (s *Server) appEnvironmentEdit(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	runtime, build, err := s.appVars(r, v, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var f ui.Form
	f.Set("vars", deploy.FormatEnv(runtime))
	f.Set("build_vars", deploy.FormatEnv(build))
	s.render(w, r, http.StatusOK, pages.AppEnvironmentEdit(s.appShell(w, r, v), v, f))
}

func (s *Server) appEnvironmentSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var f ui.Form
	text, buildText := r.PostFormValue("vars"), r.PostFormValue("build_vars")
	f.Set("vars", text)
	f.Set("build_vars", buildText)

	vars, err := deploy.ParseEnv(text)
	if err != nil {
		f.Fail("vars", sentence(err))
	} else if _, err := deploy.EnvFile(vars); err != nil {
		f.Fail("vars", sentence(err))
	}
	var buildVars []db.EnvVar
	if v.App.Source == db.SourceGit {
		if buildVars, err = deploy.ParseEnv(buildText); err != nil {
			f.Fail("build_vars", sentence(err))
		}
		for _, bv := range buildVars {
			// Build arguments reach the build through its environment.
			if docker.ReservedBuildArg(bv.Key) {
				f.Fail("build_vars", bv.Key+" cannot be a build-time variable: the build tools themselves read it. Choose another name.")
			}
		}
	}
	if !f.OK() {
		s.render(w, r, http.StatusUnprocessableEntity, pages.AppEnvironmentEdit(s.appShell(w, r, v), v, f))
		return
	}
	// One name cannot be both: the table holds each name once per app.
	runtimeKeys := make(map[string]bool, len(vars))
	for _, rv := range vars {
		runtimeKeys[rv.Key] = true
	}
	for i := range buildVars {
		if runtimeKeys[buildVars[i].Key] {
			f.Fail("build_vars", buildVars[i].Key+" is already a runtime variable. Use a different name for the build-time one.")
			s.render(w, r, http.StatusUnprocessableEntity, pages.AppEnvironmentEdit(s.appShell(w, r, v), v, f))
			return
		}
		buildVars[i].BuildTime = true
	}
	all := append(vars, buildVars...)
	values := make([]string, 0, len(all))
	for i := range all {
		values = append(values, all[i].Value)
		if all[i].Value, err = s.Box.SealString(all[i].Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := s.DB.ReplaceEnvVars(r.Context(), db.KindApp, v.App.ID, all); err != nil {
		s.fail(w, r, err)
		return
	}
	s.warnMissingShared(w, r, v.App.EnvironmentID, v.App.ServerID, values, "Variables saved. Redeploy to apply them.")
	redirect(w, r, "/apps/"+v.App.ID+"/environment")
}

func (s *Server) appStorage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	s.renderStorage(w, r, http.StatusOK, v, ui.Form{})
}

func (s *Server) renderStorage(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, f ui.Form) {
	list, err := s.DB.ListStorages(r.Context(), db.KindApp, v.App.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.AppStorage(s.appShell(w, r, v), v, list, f))
}

func (s *Server) appStorageAdd(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var f ui.Form
	st := db.Storage{
		ResourceKind: db.KindApp, ResourceID: v.App.ID,
		Kind:   r.PostFormValue("kind"),
		Source: strings.TrimSpace(r.PostFormValue("source")),
		Target: strings.TrimSpace(r.PostFormValue("target")),
	}
	content := r.PostFormValue("content")
	f.Set("kind", st.Kind)
	f.Set("source", st.Source)
	f.Set("target", st.Target)
	f.Set("content", content)

	if !docker.ValidMountPath(st.Target) {
		f.Fail("target", "Enter an absolute path inside the container, such as /data, without commas.")
	}
	switch st.Kind {
	case db.StorageVolume:
		if !docker.ValidName(st.Source) || len(st.Source) > 64 {
			f.Fail("source", "Enter a volume name using letters, numbers, dots, hyphens and underscores.")
		}
	case db.StorageBind:
		if err := docker.CheckBindSource(st.Source, s.Cfg.DataDir); err != nil {
			f.Fail("source", sentence(err))
		}
	case db.StorageFile:
		st.Source = ""
		if len(content) > maxFileMount {
			f.Fail("content", "Keep the file under 256 KB.")
		}
		sealed, err := s.Box.SealString(content)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		st.Content = sealed
	default:
		f.Fail("kind", "Choose a storage type.")
	}
	if f.OK() {
		if _, err := s.DB.AddStorage(r.Context(), st); db.IsUnique(err) {
			f.Fail("target", "Something is already mounted at this path.")
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderStorage(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	setFlash(w, r, ui.ToneOK, "Storage added. Redeploy to mount it.")
	redirect(w, r, "/apps/"+v.App.ID+"/storage")
}

func (s *Server) appStorageDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	err := s.DB.DeleteStorage(r.Context(), db.KindApp, v.App.ID, r.PathValue("sid"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Storage removed. The data itself is kept on the server. Redeploy to unmount it.")
	redirect(w, r, "/apps/"+v.App.ID+"/storage")
}

func (s *Server) appSettings(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.renderAppSettings(w, r, http.StatusOK, v, ui.Form{}, ui.Form{})
	}
}

func (s *Server) appSettingsSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var f ui.Form
	form := func(key string) string {
		val := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, val)
		return val
	}
	app := v.App
	app.Name = strings.ToLower(form("name"))
	if !envNameRE.MatchString(app.Name) {
		f.Fail("name", appNameRule)
	}
	// A Git app's image is whatever its last build produced.
	if app.Source != db.SourceGit {
		app.Image = form("image")
		if !docker.ValidImage(app.Image) {
			f.Fail("image", "Enter an image name such as nginx:alpine or ghcr.io/you/app:1.4.")
		}
	}
	app.StartCommand = form("start_command")
	if len(app.StartCommand) > 1000 || strings.ContainsAny(app.StartCommand, "\n\r\x00") {
		f.Fail("start_command", "Keep the command on one line, under 1000 characters.")
	}
	app.DockerOptions = form("docker_options")
	if _, err := deploy.ParseRunOptions(app.DockerOptions); err != nil {
		f.Fail("docker_options", sentence(err))
	}
	app.Port = parsePort(&f, "port", form("port"))

	app.MemoryMB, app.CPUs = parseLimits(&f, form("memory_mb"), form("cpus"))
	app.HealthPath = form("health_path")
	if app.HealthPath != "" && !deploy.ValidHealthPath(app.HealthPath) {
		f.Fail("health_path", "Enter a path that starts with a single /, such as /healthz.")
	}
	app.HealthCmd = form("health_cmd")
	if len(app.HealthCmd) > 500 || strings.ContainsAny(app.HealthCmd, "\n\r") {
		f.Fail("health_cmd", "Keep the command on one line, under 500 characters.")
	}
	if n, err := strconv.Atoi(form("health_timeout")); err != nil || n < 5 || n > 900 {
		f.Fail("health_timeout", "Enter a number of seconds between 5 and 900.")
	} else {
		app.HealthTimeout = n
	}

	if f.OK() {
		err := s.DB.UpdateAppSettings(r.Context(), sessionFrom(r).TeamID, app)
		switch {
		case db.IsUnique(err), errors.Is(err, db.ErrNameTaken):
			f.Fail("name", "This environment already has an app, database or service called "+app.Name+".")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderAppSettings(w, r, http.StatusUnprocessableEntity, v, f, ui.Form{})
		return
	}
	setFlash(w, r, ui.ToneOK, "Settings saved. Redeploy to apply them.")
	redirect(w, r, "/apps/"+app.ID+"/settings")
}

// syncRoutes republishes the routes of an app's server after its domains
// changed. A proxy that is not running is not an error here: the routes are
// written and load when it starts. The database already holds the change,
// so publishing it is not abandoned if the browser disconnects.
func (s *Server) syncRoutes(r *http.Request, serverID string) {
	ctx, cancel := detached(r, 30*time.Second)
	defer cancel()
	server, err := s.DB.ServerByID(ctx, serverID)
	if err == nil {
		err = s.Deploy.SyncRoutes(ctx, server)
	}
	if err != nil && !errors.Is(err, deploy.ErrProxyDown) {
		s.Log.Error("sync routes", "server", serverID, "err", err)
	}
}

// addAppDomain reads the Add domain form and gives the app the domain. What
// is wrong with the form is recorded in f; an error is one of the server's.
// app is one a loader returned for the team.
func (s *Server) addAppDomain(r *http.Request, app db.App, f *ui.Form) error {
	raw := strings.TrimSpace(r.PostFormValue("host"))
	tls := r.PostFormValue("tls") == "1"
	www := r.PostFormValue("redirect_www") == "1"
	strip := r.PostFormValue("strip_prefix") == "1"
	onOff := map[bool]string{true: "1", false: "0"}
	f.Set("host", raw)
	f.Set("path", strings.TrimSpace(r.PostFormValue("path")))
	f.Set("tls", onOff[tls])
	f.Set("redirect_www", onOff[www])
	f.Set("strip_prefix", onOff[strip])
	// The password is not put back in the form: it is typed again.
	f.Set("auth_user", strings.TrimSpace(r.PostFormValue("auth_user")))

	host := s.checkHost(r.Context(), f, "host", raw)
	path := domainPath(f, "path", r.PostFormValue("path"))
	authUser, authHash := domainAuth(f, r.PostFormValue("auth_user"), r.PostFormValue("auth_password"))
	// "www." is added in front for the redirect; the result must still fit
	// in a host name.
	if f.OK() && www && !strings.HasPrefix(host, "www.") && !proxy.ValidHost("www."+host) {
		f.Fail("host", "This name is too long to also carry a www form. Untick the redirect or use a shorter name.")
	}
	if n, err := s.DB.CountDomains(r.Context(), db.KindApp, app.ID); err == nil && n >= maxDomains {
		f.Fail("host", "An app can have up to 20 domains.")
	}
	if !f.OK() {
		return nil
	}
	if isGeneratedDomain(host) {
		tls = false
	}
	_, err := s.DB.AddDomain(r.Context(), sessionFrom(r).TeamID, app.ServerID, db.Domain{
		ResourceKind: db.KindApp, ResourceID: app.ID, Host: host, Path: path, StripPrefix: strip && path != "",
		TLS: tls, RedirectWWW: www, AuthUser: authUser, AuthHash: authHash,
	})
	switch {
	case db.IsUnique(err) && path != "":
		f.Fail("path", "This path of the domain is already routed to something.")
	case db.IsUnique(err), errors.Is(err, db.ErrHostTaken):
		f.Fail("host", domainTaken)
	case errors.Is(err, db.ErrHostElsewhere):
		f.Fail("host", "This domain is routed on another server. A domain's paths are all served by the server its DNS points at.")
	case err != nil:
		return err
	}
	return nil
}

// renderAppDomains draws an app's Domains tab; f is the Add domain form.
func (s *Server) renderAppDomains(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, f ui.Form) {
	s.render(w, r, status, pages.AppDomains(s.appShell(w, r, v), v, f))
}

func (s *Server) appDomains(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.renderAppDomains(w, r, http.StatusOK, v, ui.Form{})
	}
}

func (s *Server) appDomainAdd(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var f ui.Form
	if err := s.addAppDomain(r, v.App, &f); err != nil {
		s.fail(w, r, err)
		return
	}
	if !f.OK() {
		s.renderAppDomains(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	s.syncRoutes(r, v.App.ServerID)
	setFlash(w, r, ui.ToneOK, "Domain added.")
	redirect(w, r, "/apps/"+v.App.ID+"/domains")
}

func (s *Server) appDomainDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	err := s.DB.DeleteDomain(r.Context(), db.KindApp, v.App.ID, r.PathValue("did"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.syncRoutes(r, v.App.ServerID)
	setFlash(w, r, ui.ToneOK, "Domain removed.")
	redirect(w, r, "/apps/"+v.App.ID+"/domains")
}

func (s *Server) appDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	back := "/apps/" + v.App.ID + "/settings"
	if strings.TrimSpace(r.PostFormValue("confirm")) != v.App.Name {
		setFlash(w, r, ui.ToneDanger, "The app was not deleted: the name you typed did not match.")
		redirect(w, r, back)
		return
	}
	err := s.Deploy.Destroy(r.Context(), v.App.ID)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "A deployment is in progress. Delete the app once it has finished.")
		redirect(w, r, back)
		return
	case err != nil:
		s.Log.Error("delete app", "app", v.App.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The app could not be deleted: "+err.Error())
		redirect(w, r, back)
		return
	}
	if v.App.IsPreview() {
		setFlash(w, r, ui.ToneOK, "Preview removed.")
		redirect(w, r, "/apps/"+v.App.PreviewOf+"/settings#previews")
		return
	}
	setFlash(w, r, ui.ToneOK, "App deleted.")
	redirect(w, r, envPath(v.Project.ID, v.Env.ID))
}
