package web

import (
	"errors"
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// maxSharedVars bounds how many variables one team, project, environment
// or server holds: all of a resource's four sets are read at a deployment.
const maxSharedVars = 200

// sharedTarget is the team, project, environment or server whose shared
// variables a request is about.
type sharedTarget struct {
	scope  string
	id     string
	view   pages.SharedView
	active string // navigation item
	crumbs []ui.Crumb
}

// A sharedLoader finds the target a request names, for the signed-in
// team. It answers 404 itself.
type sharedLoader func(w http.ResponseWriter, r *http.Request) (sharedTarget, bool)

func (s *Server) sharedTeam(w http.ResponseWriter, r *http.Request) (sharedTarget, bool) {
	return sharedTarget{
		scope: db.ScopeTeam, id: sessionFrom(r).TeamID, active: "team",
		view: pages.SharedView{
			Title: "Team", Intro: "Who can sign in to this musdash, and what each of them may do.",
			Scope: db.ScopeTeam, Action: "/team/variables", CanEdit: may(r, admin), AdminSet: true,
			Tabs: pages.TeamTabs(), TabActive: "variables",
		},
	}, true
}

func (s *Server) sharedProject(w http.ResponseWriter, r *http.Request) (sharedTarget, bool) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return sharedTarget{}, false
	}
	return sharedTarget{
		scope: db.ScopeProject, id: p.ID, active: "projects",
		crumbs: projectCrumbs(p, ui.Crumb{Label: "Settings", Href: "/projects/" + p.ID + "/settings"}, ui.Crumb{Label: "Shared variables"}),
		view: pages.SharedView{
			Title: "Variables of " + p.Name, Intro: "Shared by everything in this project, in every environment.",
			Scope: db.ScopeProject, Action: "/projects/" + p.ID + "/variables", CanEdit: true,
		},
	}, true
}

func (s *Server) sharedEnvironment(w http.ResponseWriter, r *http.Request) (sharedTarget, bool) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return sharedTarget{}, false
	}
	return sharedTarget{
		scope: db.ScopeEnvironment, id: env.ID, active: "projects",
		crumbs: envCrumbs(p, env, ui.Crumb{Label: "Settings", Href: pages.EnvPath(p.ID, env.ID) + "/settings"}, ui.Crumb{Label: "Shared variables"}),
		view: pages.SharedView{
			Title: "Variables of " + env.Name, Intro: "Shared by everything in the " + env.Name + " environment of " + p.Name + ".",
			Scope: db.ScopeEnvironment, Action: pages.EnvPath(p.ID, env.ID) + "/variables", CanEdit: true,
		},
	}, true
}

func (s *Server) sharedServer(w http.ResponseWriter, r *http.Request) (sharedTarget, bool) {
	server, err := s.DB.Server(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return sharedTarget{}, false
	}
	if err != nil {
		s.fail(w, r, err)
		return sharedTarget{}, false
	}
	return sharedTarget{
		scope: db.ScopeServer, id: server.ID, active: "servers",
		crumbs: []ui.Crumb{{Label: "Servers", Href: "/servers"}, {Label: server.Name + " variables"}},
		view: pages.SharedView{
			Title: "Variables of " + server.Name, Intro: "Shared by everything that runs on this server.",
			Scope: db.ScopeServer, Action: "/servers/" + server.ID + "/variables", CanEdit: may(r, admin), AdminSet: true,
		},
	}, true
}

// sharedVars lists a scope's variables. Their values are opened only when
// asked for, and never for a reader who may not change them.
func (s *Server) sharedVars(r *http.Request, t sharedTarget, shown bool) ([]db.EnvVar, error) {
	sealed, err := s.DB.SharedVars(r.Context(), sessionFrom(r).TeamID, t.scope, t.id)
	if err != nil {
		return nil, err
	}
	out := make([]db.EnvVar, 0, len(sealed))
	for _, v := range sealed {
		value := ""
		if shown && t.view.CanEdit {
			if value, err = s.Box.OpenString(v.Value); err != nil {
				return nil, errors.New("shared variable " + v.Key + " cannot be decrypted")
			}
		}
		out = append(out, db.EnvVar{Key: v.Key, Value: value})
	}
	return out, nil
}

func sharedCard(t sharedTarget, vars []db.EnvVar, shown bool) pages.VarsCard {
	return pages.VarsCard{ID: "shared-vars", Title: "Variables", Shown: shown,
		Intro:  "Stored encrypted. A resource gets the value that is here when it is deployed.",
		Values: t.view.Action + "/values", Edit: t.view.Action + "/edit", Groups: []pages.VarGroup{{Vars: vars}}}
}

// sharedShow is the page of one scope's shared variables: their names. A
// reader who may change them can ask for the values; one who may not is
// given the names and no way to ask.
//
// That keeps a value off a page its reader cannot edit. It does not keep
// the value from them: a Member may name a team or server variable in an
// app they deploy, which is what those variables are for, and the app
// reads it. Refusing such names to Members would take shared variables
// away from the people who deploy; the page tells the Admin instead.
func (s *Server) sharedShow(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := load(w, r)
		if !ok {
			return
		}
		vars, err := s.sharedVars(r, t, false)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, v := range vars {
			t.view.Names = append(t.view.Names, v.Key)
		}
		s.render(w, r, http.StatusOK, pages.SharedVariables(s.shell(w, r, t.view.Title, t.active, t.crumbs...), t.view, sharedCard(t, vars, false)))
	}
}

// sharedValues answers Show values and Hide values. Its route asks for the
// role that may change the scope's variables.
func (s *Server) sharedValues(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := load(w, r)
		if !ok {
			return
		}
		shown := r.URL.Query().Get("hide") == ""
		vars, err := s.sharedVars(r, t, shown)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, http.StatusOK, pages.Variables(sharedCard(t, vars, shown && t.view.CanEdit)))
	}
}

// renderSharedEdit draws a scope's editor, which holds the values.
func (s *Server) renderSharedEdit(w http.ResponseWriter, r *http.Request, status int, t sharedTarget, f ui.Form) {
	if _, typed := f.Values["vars"]; !typed {
		vars, err := s.sharedVars(r, t, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		f.Set("vars", deploy.FormatEnv(vars))
	}
	crumbs := append(append([]ui.Crumb{}, t.crumbs...), ui.Crumb{Label: "Edit"})
	if n := len(crumbs); n >= 2 {
		crumbs[n-2].Href = t.view.Action
	}
	s.render(w, r, status, pages.SharedVariablesEdit(s.shell(w, r, t.view.Title, t.active, crumbs...), t.view, f))
}

func (s *Server) sharedEdit(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, ok := load(w, r); ok {
			s.renderSharedEdit(w, r, http.StatusOK, t, ui.Form{})
		}
	}
}

// sharedSave replaces one scope's shared variables. The route decides who
// may: anybody for a project's and an environment's, an Admin for the
// team's and a server's.
func (s *Server) sharedSave(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := load(w, r)
		if !ok {
			return
		}
		var f ui.Form
		text := r.PostFormValue("vars")
		f.Set("vars", text)
		vars, err := deploy.ParseEnv(text)
		switch {
		case err != nil:
			f.Fail("vars", sentence(err))
		case len(vars) > maxSharedVars:
			f.Fail("vars", "Keep to "+itoa(maxSharedVars)+" variables here.")
		}
		for _, v := range vars {
			// A shared value is put in as it is and not read again, so a
			// name inside one would reach the container as text.
			if refs := deploy.SharedRefs(v.Value); len(refs) > 0 {
				f.Fail("vars", v.Key+" names "+refs[0].String()+". A shared variable cannot take its value from another one; write the value itself.")
				break
			}
		}
		if !f.OK() {
			s.renderSharedEdit(w, r, http.StatusUnprocessableEntity, t, f)
			return
		}
		for i := range vars {
			if vars[i].Value, err = s.Box.SealString(vars[i].Value); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		if err := s.DB.ReplaceSharedVars(r.Context(), sessionFrom(r).TeamID, t.scope, t.id, vars); err != nil {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, "Variables saved. What uses them gets the new values at its next deploy.")
		redirect(w, r, t.view.Action)
	}
}

// warnMissingShared tells a person who just saved variables which shared
// variables they name that do not exist yet. It only warns: the shared
// one may be added next, and a deployment refuses to run without it.
func (s *Server) warnMissingShared(w http.ResponseWriter, r *http.Request, environmentID, serverID string, values []string, saved string) {
	missing, err := s.Deploy.MissingShared(r.Context(), environmentID, serverID, values)
	if err != nil {
		s.Log.Error("check shared variables", "err", err)
	}
	if len(missing) == 0 {
		setFlash(w, r, ui.ToneOK, saved)
		return
	}
	names := ""
	for i, ref := range missing {
		if i == 5 {
			names += " and more"
			break
		}
		if i > 0 {
			names += ", "
		}
		names += ref.String()
	}
	setFlash(w, r, ui.ToneWarn, saved+" These shared variables do not exist yet, and a deploy fails until they do: "+names+".")
}
