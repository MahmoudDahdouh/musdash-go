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
	if name == "" || len(name) > 60 {
		f.Fail("name", "Enter a name, up to 60 characters.")
	}
	if len(description) > 200 {
		f.Fail("description", "Keep the description under 200 characters.")
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

func projectCrumbs(p db.Project) []ui.Crumb {
	return []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: p.Name}}
}

func (s *Server) projectList(w http.ResponseWriter, r *http.Request) {
	projects, err := s.DB.ListProjects(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ProjectList(s.shell(w, r, "Projects", "projects"), projects))
}

func (s *Server) projectNew(w http.ResponseWriter, r *http.Request) {
	crumbs := []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: "New project"}}
	s.render(w, r, http.StatusOK, pages.ProjectNew(s.shell(w, r, "New project", "projects", crumbs...), ui.Form{}))
}

func (s *Server) projectCreate(w http.ResponseWriter, r *http.Request) {
	name, description, f := projectForm(r)
	if !f.OK() {
		crumbs := []ui.Crumb{{Label: "Projects", Href: "/"}, {Label: "New project"}}
		s.render(w, r, http.StatusUnprocessableEntity, pages.ProjectNew(s.shell(w, r, "New project", "projects", crumbs...), f))
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
	current := envs[0]
	if want := r.URL.Query().Get("env"); want != "" {
		found := false
		for _, e := range envs {
			if e.ID == want {
				current, found = e, true
				break
			}
		}
		if !found {
			s.notFound(w, r)
			return
		}
	}
	apps, err := s.DB.ListApps(r.Context(), current.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ProjectShow(s.shell(w, r, p.Name, "projects", projectCrumbs(p)...), p, envs, current, apps))
}

// renderSettings draws the project settings page with the given form states.
func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, p db.Project, details, envForm ui.Form) {
	envs, err := s.DB.ListEnvironments(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.ProjectSettings(s.shell(w, r, p.Name, "projects", projectCrumbs(p)...), p, envs, details, envForm))
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
		setFlash(w, r, ui.ToneDanger, "The project still has apps deployed in it. Delete them first.")
		redirect(w, r, "/projects/"+p.ID+"/settings")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Project deleted.")
	redirect(w, r, "/")
}

func (s *Server) environmentCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var f ui.Form
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	f.Set("name", name)
	if !envNameRE.MatchString(name) {
		f.Fail("name", envNameRule)
	} else if _, err := s.DB.CreateEnvironment(r.Context(), p.TeamID, p.ID, name); db.IsUnique(err) {
		f.Fail("name", "This project already has an environment called "+name+".")
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	if !f.OK() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, p, ui.Form{}, f)
		return
	}
	setFlash(w, r, ui.ToneOK, "Environment added.")
	redirect(w, r, "/projects/"+p.ID+"/settings")
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
		setFlash(w, r, ui.ToneDanger, "The environment still has apps deployed in it. Delete them first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, r, ui.ToneOK, "Environment deleted.")
	}
	redirect(w, r, back)
}
