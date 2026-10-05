package web

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// databasePasswordLen is the length of a generated database password:
// letters and digits only, so it needs no escaping in a URL, a shell or any
// engine's own syntax, with about 190 bits of entropy.
const databasePasswordLen = 32

// loadDatabase fetches the database in the path with its project and
// environment, for the signed-in team. It answers 404 itself.
func (s *Server) loadDatabase(w http.ResponseWriter, r *http.Request) (pages.DatabaseView, bool) {
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	var v pages.DatabaseView
	m, err := s.DB.Database(ctx, teamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, false
	}
	if err == nil {
		v.DB = m
		v.Env, err = s.DB.Environment(ctx, teamID, m.EnvironmentID)
	}
	if err == nil {
		v.Project, err = s.DB.Project(ctx, teamID, v.Env.ProjectID)
	}
	if err != nil {
		s.fail(w, r, err)
		return v, false
	}
	// An engine this version does not know still gets its pages, so the
	// database can be looked at and deleted.
	v.Engine, _ = catalog.Database(m.Engine)
	if v.Engine.Label == "" {
		v.Engine.Label = m.Engine
	}
	return v, true
}

func (s *Server) databaseShell(w http.ResponseWriter, r *http.Request, v pages.DatabaseView) ui.Shell {
	return s.shell(w, r, v.DB.Name, "projects",
		ui.Crumb{Label: "Projects", Href: "/"},
		ui.Crumb{Label: v.Project.Name, Href: "/projects/" + v.Project.ID + "?env=" + v.Env.ID},
		ui.Crumb{Label: v.DB.Name},
	)
}

// newDatabaseTarget loads the project and environment a new database goes
// into. The environment id comes from the query or the form.
func (s *Server) newDatabaseTarget(w http.ResponseWriter, r *http.Request, envID string) (db.Project, db.Environment, bool) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return p, db.Environment{}, false
	}
	env, err := s.DB.Environment(r.Context(), sessionFrom(r).TeamID, envID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && env.ProjectID != p.ID) {
		s.notFound(w, r)
		return p, env, false
	}
	if err != nil {
		s.fail(w, r, err)
		return p, env, false
	}
	return p, env, true
}

func newDatabaseCrumbs(p db.Project, env db.Environment) []ui.Crumb {
	return []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: p.Name, Href: "/projects/" + p.ID + "?env=" + env.ID}, {Label: "New database"}}
}

func (s *Server) databaseNew(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.newDatabaseTarget(w, r, r.URL.Query().Get("env"))
	if !ok {
		return
	}
	shell := s.shell(w, r, "New database", "projects", newDatabaseCrumbs(p, env)...)
	engine := r.URL.Query().Get("engine")
	if engine == "" {
		s.render(w, r, http.StatusOK, pages.DatabaseEngines(shell, p, env, catalog.Databases()))
		return
	}
	tpl, known := catalog.Database(engine)
	if !known {
		s.notFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, pages.DatabaseNew(shell, p, env, tpl, ui.Form{}))
}

func (s *Server) databaseCreate(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.newDatabaseTarget(w, r, r.PostFormValue("env"))
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	tpl, known := catalog.Database(r.PostFormValue("engine"))
	if !known {
		s.notFound(w, r)
		return
	}
	server, err := s.DB.EnsureLocalServer(ctx, teamID, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var f ui.Form
	m := db.Database{
		EnvironmentID: env.ID, ServerID: server.ID, Engine: tpl.Engine,
		Name:     strings.ToLower(strings.TrimSpace(r.PostFormValue("name"))),
		Image:    strings.TrimSpace(r.PostFormValue("image")),
		Username: tpl.DefaultUser, DBName: tpl.DefaultDB,
	}
	f.Set("name", m.Name)
	f.Set("image", m.Image)
	f.Set("public", r.PostFormValue("public"))
	if !envNameRE.MatchString(m.Name) {
		f.Fail("name", appNameRule)
	}
	if !docker.ValidImage(m.Image) {
		f.Fail("image", "Enter an image name such as "+tpl.Image+".")
	}
	if r.PostFormValue("public") == "1" {
		m.PublicPort = db.PickPort
	}
	if f.OK() {
		if m.Password, err = s.Box.SealString(secret.RandomAlnum(databasePasswordLen)); err != nil {
			s.fail(w, r, err)
			return
		}
		name := m.Name
		m, err = s.DB.CreateDatabase(ctx, teamID, m)
		switch {
		case errors.Is(err, db.ErrNameTaken), db.IsUnique(err):
			f.Fail("name", "This environment already has an app, database or service called "+name+".")
		case errors.Is(err, db.ErrNoFreePort):
			f.Fail("public", "No public port is free on this server. Create the database without one and choose a port in its settings.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		shell := s.shell(w, r, "New database", "projects", newDatabaseCrumbs(p, env)...)
		s.render(w, r, http.StatusUnprocessableEntity, pages.DatabaseNew(shell, p, env, tpl, f))
		return
	}
	if err := s.Deploy.EnqueueDatabase(ctx, m, false); err != nil {
		// The database exists; its page says what went wrong and offers
		// Start again.
		s.Log.Error("queue database start", "database", m.ID, "err", err)
	}
	redirect(w, r, "/databases/"+m.ID)
}

// publicHost is the address a database's public port is reached at: the
// server's address when it is known, otherwise a placeholder the page
// explains.
func publicHost(server db.Server) string {
	ip := net.ParseIP(server.IP)
	switch {
	case ip == nil:
		return "SERVER_IP"
	case ip.To4() == nil:
		return "[" + ip.String() + "]"
	}
	return ip.String()
}

func (s *Server) databaseOverview(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	server, err := s.DB.ServerByID(r.Context(), v.DB.ServerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The one place a database's password is opened for a person to read.
	c := pages.Connection{Host: v.DB.Name, Port: v.Engine.Port, User: v.DB.Username, DBName: v.DB.DBName}
	pass, err := s.Box.OpenString(v.DB.Password)
	if err != nil {
		c.Undecryptable = true
	} else {
		creds := catalog.Creds{User: v.DB.Username, Pass: pass, DB: v.DB.DBName}
		c.Password = pass
		c.URL = v.Engine.URL(creds, v.DB.Name, v.Engine.Port)
		if v.DB.PublicPort > 0 {
			c.PublicHost = publicHost(server)
			c.PublicURL = v.Engine.URL(creds, c.PublicHost, v.DB.PublicPort)
		}
	}
	s.render(w, r, http.StatusOK, pages.DatabaseOverview(s.databaseShell(w, r, v), v, server, c, deploy.DatabaseVolume(v.DB.ID)))
}

// databaseStatus serves the header fragment that database pages poll.
func (s *Server) databaseStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.DB.Database(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	refreshWhenSettled(w, r, m.Status)
	s.render(w, r, http.StatusOK, pages.DatabaseHeader(sessionFrom(r).CSRFToken, m))
}

// databaseStart starts a stopped database, or restarts a running one.
func (s *Server) databaseStart(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	err := s.Deploy.EnqueueDatabase(r.Context(), v.DB, false)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The database is already starting.")
	case err != nil:
		s.Log.Error("queue database start", "database", v.DB.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The start could not be queued. Try again.")
	}
	redirect(w, r, "/databases/"+v.DB.ID)
}

func (s *Server) databaseStop(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	err := s.Deploy.StopDatabase(r.Context(), v.DB.ID)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The database is starting. Stop it once that has finished.")
	case err != nil:
		s.Log.Error("stop database", "database", v.DB.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The database could not be stopped: "+err.Error())
	default:
		setFlash(w, r, ui.ToneOK, "Database stopped. Its data is kept.")
	}
	redirect(w, r, "/databases/"+v.DB.ID)
}

func (s *Server) databaseLogs(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.render(w, r, http.StatusOK, pages.DatabaseLogs(s.databaseShell(w, r, v), v))
	}
}

// databaseLogsStream streams the database container's output.
func (s *Server) databaseLogsStream(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	if v.DB.Container == "" {
		http.Error(w, "Nothing is running.", http.StatusConflict)
		return
	}
	server, err := s.DB.ServerByID(r.Context(), v.DB.ServerID)
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
	dk.Logs(ctx, v.DB.Container, 200, true, out)
	if ctx.Err() == nil {
		out.finish()
	}
}

func (s *Server) databaseSettings(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.render(w, r, http.StatusOK, pages.DatabaseSettings(s.databaseShell(w, r, v), v, ui.Form{}, deploy.DatabaseVolume(v.DB.ID)))
	}
}

func (s *Server) databaseSettingsSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var f ui.Form
	form := func(key string) string {
		val := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, val)
		return val
	}
	f.Set("_submitted", "1")
	m := v.DB
	if m.Image = form("image"); !docker.ValidImage(m.Image) {
		f.Fail("image", "Enter an image name with its tag, such as postgres:17-alpine.")
	}
	m.MemoryMB, m.CPUs = parseLimits(&f, form("memory_mb"), form("cpus"))

	rawPort := form("public_port")
	if form("public") != "1" {
		m.PublicPort = 0
	} else if rawPort != "" {
		n, err := strconv.Atoi(rawPort)
		if err != nil || !deploy.ValidPublicPort(n) {
			f.Fail("public_port", "Enter a port from 1024 to 65535, outside 20000 to 29999, or leave it empty.")
		}
		m.PublicPort = n
	} else if m.PublicPort == 0 {
		// Switched on without a number: musdash picks one.
		m.PublicPort = db.PickPort
	}

	if f.OK() {
		port, err := s.DB.UpdateDatabaseSettings(ctx, sessionFrom(r).TeamID, m)
		switch {
		case errors.Is(err, db.ErrPortTaken), db.IsUnique(err):
			f.Fail("public_port", "Another database on this server already uses port "+strconv.Itoa(m.PublicPort)+".")
		case errors.Is(err, db.ErrNoFreePort):
			f.Fail("public_port", "No port is free in the range musdash picks from. Enter one yourself.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
		m.PublicPort = port
	}
	if !f.OK() {
		s.render(w, r, http.StatusUnprocessableEntity, pages.DatabaseSettings(s.databaseShell(w, r, v), v, f, deploy.DatabaseVolume(v.DB.ID)))
		return
	}
	// A database with a container gets the new settings at once, by being
	// restarted. Leaving them for later would leave the page describing a
	// database that is not the one running: above all a public port that
	// was switched off here and is still open on the server.
	changed := m.Image != v.DB.Image || m.PublicPort != v.DB.PublicPort || m.MemoryMB != v.DB.MemoryMB || m.CPUs != v.DB.CPUs
	live := v.DB.Container != "" || v.DB.Status == db.AppDeploying
	switch {
	case !changed:
		setFlash(w, r, ui.ToneOK, "Nothing changed.")
	case !live:
		setFlash(w, r, ui.ToneOK, "Settings saved. They apply when the database is started.")
	default:
		if err := s.Deploy.EnqueueDatabase(ctx, m, true); err != nil {
			s.Log.Error("queue database restart", "database", m.ID, "err", err)
			setFlash(w, r, ui.ToneDanger, "Settings saved, but the restart that applies them could not be queued. Restart the database yourself.")
			break
		}
		setFlash(w, r, ui.ToneOK, "Settings saved. The database is restarting to apply them.")
	}
	redirect(w, r, "/databases/"+m.ID+"/settings")
}

func (s *Server) databaseDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	back := "/databases/" + v.DB.ID + "/settings"
	if strings.TrimSpace(r.PostFormValue("confirm")) != v.DB.Name {
		setFlash(w, r, ui.ToneDanger, "The database was not deleted: the name you typed did not match.")
		redirect(w, r, back)
		return
	}
	deleteData := r.PostFormValue("delete_data") == "1"
	err := s.Deploy.DestroyDatabase(r.Context(), v.DB.ID, deleteData)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The database is starting. Delete it once that has finished.")
		redirect(w, r, back)
		return
	case err != nil:
		s.Log.Error("delete database", "database", v.DB.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The database could not be deleted: "+err.Error())
		redirect(w, r, back)
		return
	}
	if deleteData {
		setFlash(w, r, ui.ToneOK, "Database and its data deleted.")
	} else {
		setFlash(w, r, ui.ToneOK, "Database deleted. Its data is still in the volume "+deploy.DatabaseVolume(v.DB.ID)+".")
	}
	redirect(w, r, "/projects/"+v.Project.ID+"?env="+v.Env.ID)
}
