package web

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// Environment names end up in container names and generated domains, so
// they are restricted to a DNS-label-safe form.
var envNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

const envNameRule = "Use lowercase letters, numbers and hyphens, up to 32 characters."

// projectForm reads and validates the name and description fields.
func projectForm(r *http.Request) (name, description string, f ui.Form) {
	name = strings.TrimSpace(r.PostFormValue("name"))
	description = strings.TrimSpace(r.PostFormValue("description"))
	f.Set("name", name)
	f.Set("description", description)
	if name == "" || len(name) > 60 || !plainText(name) {
		f.Fail("name", labelProblem(name, "Enter a name, up to 60 characters."))
	}
	if len(description) > 200 || !plainText(description) {
		f.Fail("description", labelProblem(description, "Keep the description under 200 characters."))
	}
	return name, description, f
}

// loadProject fetches the project in the path for the signed-in team. It
// answers 404 itself and returns false when the project is not visible.
func (s *Server) loadProject(w http.ResponseWriter, r *http.Request) (db.Project, bool) {
	p, err := s.DB.Project(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return p, false
	}
	if err != nil {
		s.fail(w, r, err)
		return p, false
	}
	return p, true
}

// renderProjects draws the Projects page; f is the New project form, with
// what was refused when it comes back.
func (s *Server) renderProjects(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	projects, err := s.DB.ListProjects(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.ProjectList(s.shell(w, r, "Projects", "projects"), projects, f))
}

func (s *Server) projectList(w http.ResponseWriter, r *http.Request) {
	s.renderProjects(w, r, http.StatusOK, ui.Form{})
}

func (s *Server) projectCreate(w http.ResponseWriter, r *http.Request) {
	name, description, f := projectForm(r)
	if !f.OK() {
		s.renderProjects(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	p, err := s.DB.CreateProject(r.Context(), sessionFrom(r).TeamID, name, description)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Project created.")
	redirect(w, r, "/projects/"+p.ID)
}

// projectShow is a project's first environment, which is production unless
// that one was deleted: where a link to the project itself leads.
func (s *Server) projectShow(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	envs, err := s.DB.ListEnvironments(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(envs) == 0 {
		s.fail(w, r, errors.New("project "+p.ID+" has no environment"))
		return
	}
	s.renderEnvironment(w, r, p, envs[0])
}

// environmentShow is one environment of a project, named in the path.
func (s *Server) environmentShow(w http.ResponseWriter, r *http.Request) {
	if p, env, ok := s.loadProjectEnv(w, r); ok {
		s.renderEnvironment(w, r, p, env)
	}
}

func (s *Server) renderEnvironment(w http.ResponseWriter, r *http.Request, p db.Project, env db.Environment) {
	res, err := s.envResources(r, env.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ProjectShow(s.shell(w, r, p.Name, "projects", envCrumbs(p, env)...), p, env, res))
}

// renderSettings draws the project settings page with the given form states.
func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, p db.Project, details, envForm ui.Form) {
	envs, err := s.DB.ListEnvironments(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.ProjectSettings(s.shell(w, r, p.Name, "projects", projectCrumbs(p, ui.Crumb{Label: "Settings"})...), p, envs, details, envForm))
}

func (s *Server) projectSettings(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.loadProject(w, r); ok {
		s.renderSettings(w, r, http.StatusOK, p, ui.Form{}, ui.Form{})
	}
}

func (s *Server) projectUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	name, description, f := projectForm(r)
	if !f.OK() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, p, f, ui.Form{})
		return
	}
	if err := s.DB.UpdateProject(r.Context(), p.TeamID, p.ID, name, description); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Project saved.")
	redirect(w, r, "/projects/"+p.ID+"/settings")
}

func (s *Server) projectDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	// The browser enforces the typed confirmation; check it again here so a
	// stray or scripted POST cannot delete a project.
	if strings.TrimSpace(r.PostFormValue("confirm")) != p.Name {
		setFlash(w, r, ui.ToneDanger, "The project was not deleted: the name you typed did not match.")
		redirect(w, r, "/projects/"+p.ID+"/settings")
		return
	}
	err := s.DB.DeleteProject(r.Context(), p.TeamID, p.ID)
	if db.IsForeignKey(err) {
		setFlash(w, r, ui.ToneDanger, "The project still has apps, databases or services in it. Delete them first.")
		redirect(w, r, "/projects/"+p.ID+"/settings")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Project deleted.")
	redirect(w, r, "/projects")
}

func (s *Server) environmentCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var f ui.Form
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	f.Set("name", name)
	var env db.Environment
	if !envNameRE.MatchString(name) {
		f.Fail("name", envNameRule)
	} else {
		var err error
		env, err = s.DB.CreateEnvironment(r.Context(), p.TeamID, p.ID, name)
		if db.IsUnique(err) {
			f.Fail("name", "This project already has an environment called "+name+".")
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, p, ui.Form{}, f)
		return
	}
	// Into the new environment: adding to it is what comes next.
	setFlash(w, r, ui.ToneOK, "Environment created.")
	redirect(w, r, envPath(p.ID, env.ID))
}

func (s *Server) environmentDelete(w http.ResponseWriter, r *http.Request) {
	teamID := sessionFrom(r).TeamID
	env, err := s.DB.Environment(r.Context(), teamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	back := "/projects/" + env.ProjectID + "/settings"
	if strings.TrimSpace(r.PostFormValue("confirm")) != env.Name {
		setFlash(w, r, ui.ToneDanger, "The environment was not deleted: the name you typed did not match.")
		redirect(w, r, back)
		return
	}
	err = s.DB.DeleteEnvironment(r.Context(), teamID, env.ID)
	switch {
	case errors.Is(err, db.ErrLastEnvironment):
		setFlash(w, r, ui.ToneDanger, "A project needs at least one environment.")
	case db.IsForeignKey(err):
		setFlash(w, r, ui.ToneDanger, "The environment still has apps, databases or services in it. Delete them first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, r, ui.ToneOK, "Environment deleted.")
	}
	redirect(w, r, back)
}

// renderProjectDomains draws a project's Domains tab; f is the Add domain
// form.
func (s *Server) renderProjectDomains(w http.ResponseWriter, r *http.Request, status int, p db.Project, f ui.Form) {
	ctx := r.Context()
	list, err := s.DB.ProjectDomains(ctx, p.TeamID, p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	apps, err := s.projectApps(r, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shell := s.shell(w, r, p.Name, "projects", projectCrumbs(p, ui.Crumb{Label: "Domains"})...)
	s.render(w, r, status, pages.ProjectDomains(shell, p, list, apps, f))
}

// projectApps is the apps a domain can be added to from the project's
// Domains tab: the project's own, in every environment. A preview's
// address is given to it, not chosen.
func (s *Server) projectApps(r *http.Request, p db.Project) ([]pages.AppChoice, error) {
	places, err := s.DB.Places(r.Context(), p.TeamID)
	if err != nil {
		return nil, err
	}
	apps, err := s.DB.TeamApps(r.Context(), p.TeamID)
	if err != nil {
		return nil, err
	}
	var out []pages.AppChoice
	for _, a := range apps {
		if pl := places[a.EnvironmentID]; pl.ProjectID == p.ID {
			out = append(out, pages.AppChoice{ID: a.ID, Name: a.Name, Env: pl.Env})
		}
	}
	return out, nil
}

func (s *Server) projectDomains(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.loadProject(w, r); ok {
		s.renderProjectDomains(w, r, http.StatusOK, p, ui.Form{})
	}
}

// projectDomainAdd gives a domain to one of the project's apps, with the
// checks the app's own Add domain form runs.
func (s *Server) projectDomainAdd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var f ui.Form
	appID := r.PostFormValue("app")
	f.Set("app", appID)
	// The app is the team's, in this project, and not a preview: the same
	// line the app's own route draws (ownSettings), which this route does
	// not pass through.
	app, err := s.DB.App(ctx, p.TeamID, appID)
	if err == nil {
		var env db.Environment
		if env, err = s.DB.Environment(ctx, p.TeamID, app.EnvironmentID); err == nil && (env.ProjectID != p.ID || app.IsPreview()) {
			err = db.ErrNotFound
		}
	}
	switch {
	case errors.Is(err, db.ErrNotFound):
		f.Fail("app", "Choose an app of this project.")
		// What was typed and ticked comes back as it was.
		for _, field := range []string{"host", "path", "auth_user"} {
			f.Set(field, strings.TrimSpace(r.PostFormValue(field)))
		}
		for _, box := range []string{"tls", "redirect_www", "strip_prefix"} {
			f.Set(box, map[bool]string{true: "1", false: "0"}[r.PostFormValue(box) == "1"])
		}
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		if err := s.addAppDomain(r, app, &f); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderProjectDomains(w, r, http.StatusUnprocessableEntity, p, f)
		return
	}
	s.syncRoutes(r, app.ServerID)
	setFlash(w, r, ui.ToneOK, "Domain added.")
	redirect(w, r, "/projects/"+p.ID+"/domains")
}
