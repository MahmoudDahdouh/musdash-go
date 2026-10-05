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
// (/projects/{id}/e/{env}/…) for the signed-in team. An environment of
// another project is not found, like one that does not exist.
func (s *Server) loadProjectEnv(w http.ResponseWriter, r *http.Request) (db.Project, db.Environment, bool) {
	p, ok := s.loadProject(w, r)
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

// envPath is the page of one environment of a project.
func envPath(projectID, envID string) string { return "/projects/" + projectID + "/e/" + envID }

// projectCrumbs is the trail of a page that is the project's own, whatever
// the environment: its domains, its settings.
func projectCrumbs(p db.Project, here ...ui.Crumb) []ui.Crumb {
	if len(here) == 0 {
		return []ui.Crumb{{Label: "Projects", Href: "/projects"}, {Label: p.Name, Icon: "folder"}}
	}
	return append([]ui.Crumb{{Label: "Projects", Href: "/projects"}, {Label: p.Name, Href: "/projects/" + p.ID, Icon: "folder"}}, here...)
}

// envCrumbs is the trail of a page inside one environment: the project,
// then the environment as a switcher to the project's others.
func envCrumbs(p db.Project, env db.Environment, here ...ui.Crumb) []ui.Crumb {
	return append([]ui.Crumb{
		{Label: "Projects", Href: "/projects"},
		{Label: p.Name, Href: envPath(p.ID, env.ID), Icon: "folder"},
		{Label: env.Name, Menu: "/projects/" + p.ID + "/switch/environments?at=" + env.ID},
	}, here...)
}

// resourceCrumb is a resource's step of the trail: a switcher to what else
// is in its environment.
func resourceCrumb(env db.Environment, kind, id, name string) ui.Crumb {
	return ui.Crumb{Label: name, Icon: pages.KindIcon(kind), Filter: "Find a resource",
		Menu: "/environments/" + env.ID + "/switch/resources?at=" + kind + ":" + id}
}

// menuNote answers a switcher that cannot list its options. The answer is
// a 200: htmx swaps nothing else in, and the menu would say "Loading…" for
// good.
func (s *Server) menuNote(w http.ResponseWriter, r *http.Request, text string) {
	s.render(w, r, http.StatusOK, ui.MenuNote(text, true))
}

// switchEnvironments answers the environment switcher: the project's
// environments, each a link to its page.
func (s *Server) switchEnvironments(w http.ResponseWriter, r *http.Request) {
	teamID := sessionFrom(r).TeamID
	p, err := s.DB.Project(r.Context(), teamID, r.PathValue("id"))
	if err != nil {
		s.menuNote(w, r, "The project is gone.")
		return
	}
	envs, err := s.DB.ListEnvironments(r.Context(), p.ID)
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
	env, err := s.DB.Environment(ctx, sessionFrom(r).TeamID, r.PathValue("id"))
	if err != nil {
		s.menuNote(w, r, "The environment is gone.")
		return
	}
	res, err := s.envResources(r, env.ID)
	if err != nil {
		s.Log.Error("switcher", "route", logRoute(r), "err", err)
		s.menuNote(w, r, "The resources could not be listed.")
		return
	}
	s.render(w, r, http.StatusOK, pages.ResourceOptions(env, res, r.URL.Query().Get("at")))
}

// envResources is everything in one environment. Call it with an
// environment a loader returned for the team.
func (s *Server) envResources(r *http.Request, envID string) (pages.Resources, error) {
	var res pages.Resources
	var err error
	if res.Apps, err = s.DB.ListApps(r.Context(), envID); err != nil {
		return res, err
	}
	if res.Databases, err = s.DB.ListDatabases(r.Context(), envID); err != nil {
		return res, err
	}
	res.Services, err = s.DB.ListServices(r.Context(), envID)
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
	for _, a := range choices.Access {
		v.HasApp = v.HasApp || strings.HasPrefix(a.Value, "source:")
		v.HasKey = v.HasKey || strings.HasPrefix(a.Value, "key:")
	}
	shell := s.shell(w, r, "Add resource", "projects", envCrumbs(p, env, ui.Crumb{Label: "Add resource"})...)
	s.render(w, r, http.StatusOK, pages.ResourceNew(shell, v))
}

// preferredAccess turns the kind of access a tile of the Add resource page
// asked for ("app", "key") into the first of the team's that is of that
// kind, as the Git form names it. "" when there is none, or nothing was
// asked: the form then starts at its first option.
func preferredAccess(r *http.Request, c pages.GitChoices) string {
	prefix := map[string]string{"app": "source:", "key": "key:"}[r.URL.Query().Get("access")]
	if prefix == "" {
		return ""
	}
	for _, a := range c.Access {
		if strings.HasPrefix(a.Value, prefix) {
			return a.Value
		}
	}
	return ""
}
