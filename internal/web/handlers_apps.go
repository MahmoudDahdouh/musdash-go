package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
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
	if err != nil {
		s.fail(w, r, err)
		return v, false
	}
	return v, true
}

func appCrumbs(v pages.AppView) []ui.Crumb {
	return []ui.Crumb{
		{Label: "Projects", Href: "/"},
		{Label: v.Project.Name, Href: "/projects/" + v.Project.ID + "?env=" + v.Env.ID},
		{Label: v.App.Name},
	}
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

// generatedDomain builds an address that resolves to the server without any
// DNS setup: sslip.io answers <anything>.<ip>.sslip.io with <ip>.
func generatedDomain(server db.Server) string {
	ip := server.IP
	if net.ParseIP(ip) == nil {
		ip = "127.0.0.1"
	}
	// IPv6 addresses use dashes in sslip.io names.
	ip = strings.ReplaceAll(ip, ":", "-")
	return strings.ToLower(secret.RandomID()[:8]) + "." + ip + ".sslip.io"
}

// isGeneratedDomain reports whether a host is one of the shared wildcard
// DNS names. They are served over plain HTTP: the certificate authority
// limits how many certificates such a shared suffix may get, and a refused
// certificate would look like a broken deploy.
func isGeneratedDomain(host string) bool {
	return strings.HasSuffix(host, ".sslip.io") || strings.HasSuffix(host, ".nip.io")
}

// checkDomain normalises and validates a host name for routing.
func (s *Server) checkDomain(ctx context.Context, f *ui.Form, key, value string) string {
	host := proxy.NormalizeHost(value)
	if !proxy.ValidHost(host) || !strings.Contains(host, ".") {
		f.Fail(key, "Enter a domain such as app.example.com, without http:// or a path.")
		return ""
	}
	instance, err := s.DB.Setting(ctx, db.SettingInstanceDomain)
	if err == nil && instance == host {
		f.Fail(key, "This domain is the dashboard's own address.")
		return ""
	}
	if used, err := s.DB.HostInUse(ctx, host); err == nil && used {
		f.Fail(key, "This domain is already routed to something on this install.")
		return ""
	}
	return host
}

func (s *Server) appNew(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	teamID := sessionFrom(r).TeamID
	env, err := s.DB.Environment(r.Context(), teamID, r.URL.Query().Get("env"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && env.ProjectID != p.ID) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	server, err := s.DB.EnsureLocalServer(r.Context(), teamID, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	src := db.SourceImage
	if r.URL.Query().Get("source") == db.SourceGit {
		src = db.SourceGit
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: p.Name, Href: "/projects/" + p.ID + "?env=" + env.ID}, {Label: "New app"}}
	s.render(w, r, http.StatusOK, pages.AppNew(s.shell(w, r, "New app", "projects", crumbs...), p, env, ui.Form{}, generatedDomain(server), src, choices))
}

func (s *Server) appCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	env, err := s.DB.Environment(ctx, teamID, r.PostFormValue("env"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && env.ProjectID != p.ID) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	server, err := s.DB.EnsureLocalServer(ctx, teamID, "")
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
	newApp := db.App{EnvironmentID: env.ID, ServerID: server.ID, Source: src}
	newApp.Name = strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	rawDomain := strings.TrimSpace(r.PostFormValue("domain"))
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
		crumbs := []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: p.Name, Href: "/projects/" + p.ID + "?env=" + env.ID}, {Label: "New app"}}
		s.render(w, r, status, pages.AppNew(s.shell(w, r, "New app", "projects", crumbs...), p, env, f, rawDomain, src, choices))
	}
	if !f.OK() {
		rerender(http.StatusUnprocessableEntity)
		return
	}

	app, err := s.DB.CreateApp(ctx, teamID, newApp)
	if db.IsUnique(err) || errors.Is(err, db.ErrNameTaken) {
		f.Fail("name", "This environment already has an app or database called "+newApp.Name+".")
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
		_, err := s.DB.AddDomain(ctx, db.KindApp, app.ID, host, !isGeneratedDomain(host), false)
		switch {
		case db.IsUnique(err):
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
		setFlash(w, r, ui.ToneWarn, "App created, but "+host+" was taken in the meantime. Add another domain under Settings.")
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

func (s *Server) appEnvironment(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	sealed, err := s.DB.ListEnvVars(r.Context(), db.KindApp, v.App.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var runtime, build []db.EnvVar
	for _, ev := range sealed {
		plain, err := s.Box.OpenString(ev.Value)
		if err != nil {
			s.fail(w, r, errors.New("environment variable "+ev.Key+" cannot be decrypted"))
			return
		}
		if ev.BuildTime {
			build = append(build, db.EnvVar{Key: ev.Key, Value: plain})
		} else {
			runtime = append(runtime, db.EnvVar{Key: ev.Key, Value: plain})
		}
	}
	var f ui.Form
	f.Set("vars", deploy.FormatEnv(runtime))
	f.Set("build_vars", deploy.FormatEnv(build))
	s.render(w, r, http.StatusOK, pages.AppEnvironment(s.appShell(w, r, v), v, f))
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
		s.render(w, r, http.StatusUnprocessableEntity, pages.AppEnvironment(s.appShell(w, r, v), v, f))
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
			s.render(w, r, http.StatusUnprocessableEntity, pages.AppEnvironment(s.appShell(w, r, v), v, f))
			return
		}
		buildVars[i].BuildTime = true
	}
	all := append(vars, buildVars...)
	for i := range all {
		if all[i].Value, err = s.Box.SealString(all[i].Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := s.DB.ReplaceEnvVars(r.Context(), db.KindApp, v.App.ID, all); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Variables saved. Redeploy to apply them.")
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
		s.renderAppSettings(w, r, http.StatusOK, v, ui.Form{}, ui.Form{}, ui.Form{}, "")
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

	app.MemoryMB = 0
	if raw := form("memory_mb"); raw != "" {
		// Docker refuses limits below 6 MB.
		if n, err := strconv.Atoi(raw); err != nil || n < 6 || n > 1<<20 {
			f.Fail("memory_mb", "Enter a whole number of megabytes, 6 or more, or leave it empty.")
		} else {
			app.MemoryMB = n
		}
	}
	app.CPUs = 0
	if raw := form("cpus"); raw != "" {
		// Written so that NaN, which compares false with everything, fails.
		if c, err := strconv.ParseFloat(raw, 64); err != nil || !(c >= 0.01 && c <= 512) {
			f.Fail("cpus", "Enter a number of cores such as 0.5 or 2, or leave it empty.")
		} else {
			app.CPUs = c
		}
	}
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
			f.Fail("name", "This environment already has an app or database called "+app.Name+".")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderAppSettings(w, r, http.StatusUnprocessableEntity, v, f, ui.Form{}, ui.Form{}, "")
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

func (s *Server) appDomainAdd(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var f ui.Form
	raw := strings.TrimSpace(r.PostFormValue("host"))
	tls := r.PostFormValue("tls") == "1"
	www := r.PostFormValue("redirect_www") == "1"
	f.Set("host", raw)
	f.Set("tls", map[bool]string{true: "1", false: "0"}[tls])
	f.Set("redirect_www", map[bool]string{true: "1", false: "0"}[www])

	host := s.checkDomain(r.Context(), &f, "host", raw)
	// "www." is added in front for the redirect; the result must still fit
	// in a host name.
	if f.OK() && www && !strings.HasPrefix(host, "www.") && !proxy.ValidHost("www."+host) {
		f.Fail("host", "This name is too long to also carry a www form. Untick the redirect or use a shorter name.")
	}
	if n, err := s.DB.CountDomains(r.Context(), db.KindApp, v.App.ID); err == nil && n >= maxDomains {
		f.Fail("host", "An app can have up to 20 domains.")
	}
	if f.OK() {
		if isGeneratedDomain(host) {
			tls = false
		}
		if _, err := s.DB.AddDomain(r.Context(), db.KindApp, v.App.ID, host, tls, www); db.IsUnique(err) {
			f.Fail("host", "This domain is already routed to something on this install.")
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderAppSettings(w, r, http.StatusUnprocessableEntity, v, ui.Form{}, f, ui.Form{}, "")
		return
	}
	s.syncRoutes(r, v.App.ServerID)
	setFlash(w, r, ui.ToneOK, "Domain added.")
	redirect(w, r, "/apps/"+v.App.ID+"/settings#domains")
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
	redirect(w, r, "/apps/"+v.App.ID+"/settings#domains")
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
	setFlash(w, r, ui.ToneOK, "App deleted.")
	redirect(w, r, "/projects/"+v.Project.ID+"?env="+v.Env.ID)
}
