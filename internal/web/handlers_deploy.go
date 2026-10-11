package web

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// What the search finds in the catalogue (pages.OfferRows) is in no
// environment yet. Its row leads here: a page that asks which project and
// which of its environments the thing goes into, and then leads to the form
// that makes it there. Nothing is made here, so every request is a GET.

// deployOffer is what a /deploy/{kind}/{key} address names, read from the
// catalogue and from nothing else: a kind that is neither, or a key the
// catalogue does not have, is no page. A person's own Compose file and one
// in a repository are keys of the form, not of the catalogue, and have no
// page here.
func deployOffer(r *http.Request) (pages.DeployChoice, bool) {
	v := pages.DeployChoice{Kind: r.PathValue("kind"), Key: r.PathValue("key")}
	switch v.Kind {
	case db.KindService:
		for _, t := range catalog.Services() {
			if t.Key == v.Key {
				v.Name, v.About, v.Docs, v.Website = t.Name, t.About, t.Docs, t.Website
				return v, true
			}
		}
	case db.KindDatabase:
		if t, ok := catalog.Database(v.Key); ok {
			v.Name, v.About, v.Docs, v.Website = t.Label, t.About, t.Docs, t.Website
			return v, true
		}
	}
	return v, false
}

// deployChoose is the page, and with go=1 the step after it: the project
// and the environment that were chosen are looked up for the team, and a
// pair that belongs together leads to the form. A pair that does not is
// said on the page, which then starts from the choice it can make.
func (s *Server) deployChoose(w http.ResponseWriter, r *http.Request) {
	v, ok := deployOffer(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	ctx, teamID, q := r.Context(), sessionFrom(r).TeamID, r.URL.Query()
	var err error
	if v.Projects, err = s.DB.ListProjects(ctx, teamID); err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if len(v.Projects) > 0 {
		// The project the address names if it is the team's, else the one
		// something was last deployed in, which is the likeliest answer.
		for _, p := range v.Projects {
			if p.ID == q.Get("project") {
				v.ProjectID = p.ID
			}
		}
		named := v.ProjectID != ""
		if !named {
			active, err := s.DB.ActiveProjects(ctx, teamID, 1)
			if err != nil || len(active) == 0 {
				s.fail(w, r, errors.Join(err, errors.New("no active project among the team's projects")))
				return
			}
			v.ProjectID = active[0].ID
		}
		if v.Envs, err = s.DB.ListEnvironments(ctx, v.ProjectID); err != nil {
			s.fail(w, r, err)
			return
		}
		for _, e := range v.Envs {
			if named && e.ID == q.Get("env") {
				v.EnvID = e.ID
			}
		}
		if q.Get("go") != "" {
			if v.EnvID != "" {
				redirect(w, r, v.Form(v.ProjectID, v.EnvID))
				return
			}
			v.Problem, status = "Choose a project and one of its environments.", http.StatusUnprocessableEntity
		}
		if v.EnvID == "" && len(v.Envs) > 0 {
			v.EnvID = v.Envs[0].ID
		}
	}
	shell := s.shell(w, r, "Deploy "+v.Name, "projects", ui.Crumb{Label: "Projects", Href: "/projects"}, ui.Crumb{Label: "Deploy " + v.Name})
	s.render(w, r, status, pages.Deploy(shell, v))
}

// deployEnvironments answers the page when another project is chosen: that
// project's environments, for the Environment field. The answer is a 200
// whatever happened, as a switcher's is: htmx puts nothing else into the
// page, and the environments of the project chosen before would be left
// standing under the new one.
func (s *Server) deployEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.Project(ctx, sessionFrom(r).TeamID, r.URL.Query().Get("project"))
	if errors.Is(err, db.ErrNotFound) {
		s.render(w, r, http.StatusOK, pages.DeployEnvironments("", nil, "", "The project is gone. Choose another."))
		return
	}
	var envs []db.Environment
	if err == nil {
		envs, err = s.DB.ListEnvironments(ctx, p.ID)
	}
	if err != nil {
		s.Log.Error("deploy", "route", logRoute(r), "err", err)
		s.render(w, r, http.StatusOK, pages.DeployEnvironments("", nil, "", "The environments could not be listed. Choose the project again."))
		return
	}
	chosen := ""
	if len(envs) > 0 {
		chosen = envs[0].ID
	}
	s.render(w, r, http.StatusOK, pages.DeployEnvironments(p.ID, envs, chosen, ""))
}

// insideEnv reads the project and the environment out of the address of a
// page inside one, and the project alone out of a project's own.
var insideEnv = regexp.MustCompile(`^/projects/([a-z][a-z2-7]{11})(?:/env/([a-z][a-z2-7]{11}))?(?:/|$)`)

// searchedFrom is where the search was opened: the project and environment
// of the page, when it is inside one. htmx says which page asked. It is a
// hint for the first choice of the page above and nothing more: that page
// looks both ids up for the team before it uses them.
func searchedFrom(r *http.Request) (projectID, envID string) {
	at := r.Header.Get("HX-Current-URL")
	if at == "" {
		return "", ""
	}
	u, err := r.URL.Parse(at)
	if err != nil {
		return "", ""
	}
	if m := insideEnv.FindStringSubmatch(u.Path); m != nil {
		return m[1], m[2]
	}
	return "", ""
}
