package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// loadProjectEnv fetches the project and the environment in the path
// (/projects/{project}/env/{env}/…) for the signed-in team. An environment
// of another project is not found, like one that does not exist.
func (s *Server) loadProjectEnv(w http.ResponseWriter, r *http.Request) (db.Project, db.Environment, bool) {
	p, ok := s.projectByID(w, r, r.PathValue("project"))
	if !ok {
		return p, db.Environment{}, false
	}
	env, err := s.DB.Environment(r.Context(), p.TeamID, r.PathValue("env"))
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

// projectCrumb is a project's step of the trail: a switcher to the team's
// other projects.
func projectCrumb(p db.Project) ui.Crumb {
	return ui.Crumb{Label: p.Name, Icon: "folder", Filter: "Find a project", Menu: "/switch/projects?at=" + p.ID}
}

// projectCrumbs is the trail of a page that is the project's own, whatever
// the environment: its settings, its variables.
func projectCrumbs(p db.Project, here ...ui.Crumb) []ui.Crumb {
	return append([]ui.Crumb{{Label: "Projects", Href: "/projects"}, projectCrumb(p)}, here...)
}

// envCrumbs is the trail of a page inside one environment: the project,
// then the environment as a switcher to the project's others. The marked
// option of that switcher is the way back to the environment's own page.
func envCrumbs(p db.Project, env db.Environment, here ...ui.Crumb) []ui.Crumb {
	return append([]ui.Crumb{
		{Label: "Projects", Href: "/projects"},
		projectCrumb(p),
		{Label: env.Name, Menu: "/projects/" + p.ID + "/switch/environments?at=" + env.ID},
	}, here...)
}

// resourceCrumb is a resource's step of the trail: a switcher to what else
// is in its environment.
func resourceCrumb(env db.Environment, kind, id, name string) ui.Crumb {
	return ui.Crumb{Label: name, Icon: pages.KindIcon(kind), Filter: "Find a resource",
		Menu: pages.EnvPath(env.ProjectID, env.ID) + "/switch/resources?at=" + kind + ":" + id}
}

// menuNote answers a switcher that cannot list its options. The answer is
// a 200: htmx swaps nothing else in, and the menu would say "Loading…" for
// good.
func (s *Server) menuNote(w http.ResponseWriter, r *http.Request, text string) {
	s.render(w, r, http.StatusOK, ui.MenuNote(text, true))
}

// switchTeams answers the team switcher, the first step of every trail. An
// install has one team, so the list is the one the person is in: the step
// is where another would be listed. The name is the session's own, which
// was read for this request.
func (s *Server) switchTeams(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, pages.TeamOptions(sessionFrom(r).TeamName))
}

// switchProjects answers the project switcher: the team's projects, each a
// link to its page. Whose they are is the session's to say, so the path
// names no team.
func (s *Server) switchProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.DB.ListProjects(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.Log.Error("switcher", "route", logRoute(r), "err", err)
		s.menuNote(w, r, "The projects could not be listed.")
		return
	}
	s.render(w, r, http.StatusOK, pages.ProjectOptions(projects, r.URL.Query().Get("at")))
}

// switchEnvironments answers the environment switcher: the project's
// environments, each a link to its page.
func (s *Server) switchEnvironments(w http.ResponseWriter, r *http.Request) {
	teamID := sessionFrom(r).TeamID
	p, err := s.DB.Project(r.Context(), teamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.menuNote(w, r, "The project is gone.")
		return
	}
	var envs []db.Environment
	if err == nil {
		envs, err = s.DB.ListEnvironments(r.Context(), p.ID)
	}
	if err != nil {
		s.Log.Error("switcher", "route", logRoute(r), "err", err)
		s.menuNote(w, r, "The environments could not be listed.")
		return
	}
	s.render(w, r, http.StatusOK, pages.EnvironmentOptions(p, envs, r.URL.Query().Get("at")))
}

// switchResources answers the resource switcher: what is in the
// environment, each a link to its page.
func (s *Server) switchResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, err := s.DB.Environment(ctx, sessionFrom(r).TeamID, r.PathValue("env"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && env.ProjectID != r.PathValue("project")) {
		s.menuNote(w, r, "The environment is gone.")
		return
	}
	var res pages.Resources
	if err == nil {
		res, err = s.envResources(r, env)
	}
	if err != nil {
		s.Log.Error("switcher", "route", logRoute(r), "err", err)
		s.menuNote(w, r, "The resources could not be listed.")
		return
	}
	s.render(w, r, http.StatusOK, pages.ResourceOptions(env, res, r.URL.Query().Get("at")))
}

// envResources is everything in one environment. Call it with an
// environment a loader returned for the team.
func (s *Server) envResources(r *http.Request, env db.Environment) (pages.Resources, error) {
	res := pages.Resources{Places: map[string]db.Place{env.ID: {ProjectID: env.ProjectID}}}
	var err error
	if res.Apps, err = s.DB.ListApps(r.Context(), env.ID); err != nil {
		return res, err
	}
	if res.Databases, err = s.DB.ListDatabases(r.Context(), env.ID); err != nil {
		return res, err
	}
	res.Services, err = s.DB.ListServices(r.Context(), env.ID)
	return res, err
}

// resourceNew is the one page a resource of any kind is added from: every
// way to make an app, every database engine and every service template.
func (s *Server) resourceNew(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v := pages.NewResource{Project: p, Env: env, Engines: catalog.Databases(), Templates: catalog.Services()}
	for _, g := range choices.Sources {
		v.HasApp = v.HasApp || g.Kind == db.GitSourceGitHubApp
		v.HasGitLab = v.HasGitLab || g.Kind == db.GitSourceGitLab
	}
	for _, a := range choices.Access {
		v.HasKey = v.HasKey || strings.HasPrefix(a.Value, "key:")
	}
	shell := s.shell(w, r, "Add resource", "projects", envCrumbs(p, env, ui.Crumb{Label: "Add resource"})...)
	s.render(w, r, http.StatusOK, pages.ResourceNew(shell, v))
}

// preferredAccess turns the kind of access a tile of the Add resource page
// asked for ("app", "gitlab", "key") into the first of the team's that is
// of that kind, as the Git form names it. "" when there is none, or nothing
// was asked: the form then starts at its first option.
func preferredAccess(r *http.Request, c pages.GitChoices) string {
	asked := r.URL.Query().Get("access")
	if kind := map[string]string{"app": db.GitSourceGitHubApp, "gitlab": db.GitSourceGitLab}[asked]; kind != "" {
		for _, g := range c.Sources {
			if g.Kind == kind {
				return "source:" + g.ID
			}
		}
		return ""
	}
	if asked != "key" {
		return ""
	}
	for _, a := range c.Access {
		if strings.HasPrefix(a.Value, "key:") {
			return a.Value
		}
	}
	return ""
}
