package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// One variable at a time: a row's value is shown and hidden by itself, and
// a variable is added, changed and deleted by its name
// (docs/plans/variables-one-at-a-time.md). A value is opened for the one
// request that asked for it; every other row of the list stays sealed.

// readVar reads the form that adds or changes one variable. The value is
// kept as typed, apart from line breaks: one at an end is what came with a
// paste, and one inside cannot be carried by an env file or by a line of
// the developer view.
func readVar(r *http.Request) (db.EnvVar, ui.Form) {
	var f ui.Form
	key := strings.TrimSpace(r.PostFormValue("key"))
	value := strings.Trim(r.PostFormValue("value"), "\r\n")
	// A whole line pasted into the name is taken apart, as the developer
	// view would read it.
	if strings.Contains(key, "=") && value == "" {
		if one, err := deploy.ParseEnv(key); err == nil && len(one) == 1 {
			key, value = one[0].Key, one[0].Value
		}
	}
	build := r.PostFormValue("build") != ""
	f.Set("key", key)
	f.Set("value", value)
	if build {
		f.Set("build", "1")
	}
	switch {
	case key == "":
		f.Fail("key", "Give the variable a name.")
	case !deploy.ValidEnvName(key):
		f.Fail("key", "Use letters, numbers and underscores, and do not start with a number.")
	}
	if strings.ContainsAny(value, "\r\n") {
		f.Fail("value", "A value cannot contain a line break.")
	}
	return db.EnvVar{Key: key, Value: value, BuildTime: build}, f
}

// varFormOf is the form of the dialog that changes a stored variable.
func varFormOf(key, plain string, build bool) ui.Form {
	var f ui.Form
	f.Set("key", key)
	f.Set("value", plain)
	if build {
		f.Set("build", "1")
	}
	return f
}

// varRow is a list's row of one stored variable: with its value, opened
// here, when it was asked for, and with none otherwise.
func (s *Server) varRow(sealed db.EnvVar, shown bool) (pages.VarRow, error) {
	row := pages.VarRow{Key: sealed.Key}
	if !shown {
		return row, nil
	}
	plain, err := s.Box.OpenString(sealed.Value)
	if err != nil {
		return row, errors.New("variable " + sealed.Key + " cannot be decrypted")
	}
	row.Value, row.Shown = plain, true
	return row, nil
}

// maxAppVars bounds how many variables one app holds: every one of them is
// read for the tab, for the developer view and at each deployment. Adding
// one at a time would otherwise have no end, where the developer view has
// the size of its form.
const maxAppVars = 500

const appValueHint = "Stored encrypted. It can take a shared variable by name, such as {{project.NAME}}."

// appVarsFrame is the card of an app's variables without its rows: what a
// row needs of it.
func appVarsFrame(r *http.Request, v pages.AppView) pages.VarsCard {
	return pages.VarsCard{ID: "app-vars", Title: "Environment variables", Base: v.Path() + "/environment",
		CSRF: sessionFrom(r).CSRFToken, Gone: "The app no longer gets it from its next deploy on. Its value cannot be brought back."}
}

func appVarDialog(r *http.Request, v pages.AppView, edits string, f ui.Form, open bool) pages.VarDialog {
	d := pages.VarDialog{ID: pages.NewVarDialog, Title: "Add variable", Submit: "Add variable", Action: v.Path() + "/environment/vars",
		CSRF: sessionFrom(r).CSRFToken, F: f, Build: v.App.Source == db.SourceGit, ValueHint: appValueHint, Open: open}
	if edits != "" {
		d.ID, d.Title, d.Submit, d.Edits = pages.EditVarDialog, "Edit variable", "Save variable", true
		d.Action += "/" + edits
	}
	return d
}

// renderAppEnvironment draws the Environment tab: names and no value. forms
// carries a dialog that came back refused; otherwise the page has the empty
// one that adds.
func (s *Server) renderAppEnvironment(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, forms pages.VarForms) {
	c, err := s.appVarsCard(r, v, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if forms.Add.ID == "" {
		forms.Add = appVarDialog(r, v, "", ui.Form{}, false)
	}
	shell := s.appShell(w, r, v)
	// A refused dialog is the answer to a POST at an address of its own.
	shell.Address = v.Path() + "/environment"
	s.render(w, r, status, pages.AppEnvironment(shell, v, c, forms))
}

// appVarStored loads the variable an address names, for the app it names.
func (s *Server) appVarStored(w http.ResponseWriter, r *http.Request) (pages.AppView, db.EnvVar, bool) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return v, db.EnvVar{}, false
	}
	ev, err := s.DB.EnvVarByKey(r.Context(), db.KindApp, v.App.ID, r.PathValue("name"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, ev, false
	}
	if err != nil {
		s.fail(w, r, err)
		return v, ev, false
	}
	return v, ev, true
}

// appVar answers a row's eye: the row with its value, or without (?hide=1).
func (s *Server) appVar(w http.ResponseWriter, r *http.Request) {
	v, ev, ok := s.appVarStored(w, r)
	if !ok {
		return
	}
	row, err := s.varRow(ev, r.URL.Query().Get("hide") == "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.VariableRow(appVarsFrame(r, v), row))
}

// appVarEdit answers Edit: the dialog that changes one variable, which
// holds its value.
func (s *Server) appVarEdit(w http.ResponseWriter, r *http.Request) {
	v, ev, ok := s.appVarStored(w, r)
	if !ok {
		return
	}
	plain, err := s.Box.OpenString(ev.Value)
	if err != nil {
		s.fail(w, r, errors.New("variable "+ev.Key+" cannot be decrypted"))
		return
	}
	s.render(w, r, http.StatusOK, pages.VariableDialog(appVarDialog(r, v, ev.Key, varFormOf(ev.Key, plain, ev.BuildTime), true)))
}

// appVarAdd adds one variable to an app.
func (s *Server) appVarAdd(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.appVarStore(w, r, v, "")
	}
}

// appVarSave changes the variable its address names, which may rename it.
func (s *Server) appVarSave(w http.ResponseWriter, r *http.Request) {
	if v, ev, ok := s.appVarStored(w, r); ok {
		s.appVarStore(w, r, v, ev.Key)
	}
}

// appVarStore adds a variable (old is "") or changes the one called old.
func (s *Server) appVarStore(w http.ResponseWriter, r *http.Request, v pages.AppView, old string) {
	ev, f := readVar(r)
	// Only an app that is built has build-time variables.
	ev.BuildTime = ev.BuildTime && v.App.Source == db.SourceGit
	if ev.BuildTime && docker.ReservedBuildArg(ev.Key) {
		// Build arguments reach the build through its environment.
		f.Fail("key", ev.Key+" cannot be a build-time variable: the build tools themselves read it. Choose another name.")
	}
	if f.OK() {
		sealed, err := s.Box.SealString(ev.Value)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		stored := db.EnvVar{Key: ev.Key, Value: sealed, BuildTime: ev.BuildTime}
		if old == "" {
			err = s.DB.AddEnvVar(r.Context(), db.KindApp, v.App.ID, stored, maxAppVars)
		} else {
			err = s.DB.UpdateEnvVar(r.Context(), db.KindApp, v.App.ID, old, stored)
		}
		switch {
		case errors.Is(err, db.ErrVarExists):
			f.Fail("key", ev.Key+" exists already. Change that one, or choose another name.")
		case errors.Is(err, db.ErrTooManyVars):
			f.Fail("key", "Keep to "+itoa(maxAppVars)+" variables for one app.")
		case errors.Is(err, db.ErrNotFound):
			s.notFound(w, r)
			return
		case err != nil:
			s.fail(w, r, err)
			return
		default:
			s.warnMissingShared(w, r, v.App.EnvironmentID, v.App.ServerID, []string{ev.Value}, ev.Key+" saved. Redeploy to apply it.")
			redirect(w, r, v.Path()+"/environment")
			return
		}
	}
	var forms pages.VarForms
	if old == "" {
		forms.Add = appVarDialog(r, v, "", f, true)
	} else {
		d := appVarDialog(r, v, old, f, true)
		forms.Edit = &d
	}
	s.renderAppEnvironment(w, r, http.StatusUnprocessableEntity, v, forms)
}

// appVarDelete deletes one variable of an app.
func (s *Server) appVarDelete(w http.ResponseWriter, r *http.Request) {
	v, ev, ok := s.appVarStored(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteEnvVar(r.Context(), db.KindApp, v.App.ID, ev.Key); err != nil && !errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, ev.Key+" deleted. Redeploy to apply it.")
	redirect(w, r, v.Path()+"/environment")
}

const sharedValueHint = "Stored encrypted. The value itself: a shared variable cannot take its value from another one."

// sharedFrame is the card of a scope's variables without its rows.
func sharedFrame(r *http.Request, t sharedTarget) pages.VarsCard {
	return pages.VarsCard{ID: "shared-vars", Title: "Variables", Base: t.view.Action, CSRF: sessionFrom(r).CSRFToken,
		Intro: "Stored encrypted. A resource gets the value that is here when it is deployed.",
		Gone:  "What names it fails to deploy until a variable of this name is here again. Its value cannot be brought back."}
}

func sharedVarDialog(r *http.Request, t sharedTarget, edits string, f ui.Form, open bool) pages.VarDialog {
	d := pages.VarDialog{ID: pages.NewVarDialog, Title: "Add variable", Submit: "Add variable", Action: t.view.Action + "/vars",
		CSRF: sessionFrom(r).CSRFToken, F: f, ValueHint: sharedValueHint, Open: open}
	if edits != "" {
		d.ID, d.Title, d.Submit, d.Edits = pages.EditVarDialog, "Edit variable", "Save variable", true
		d.Action += "/" + edits
	}
	return d
}

// renderShared draws a scope's page of variables: names and no value.
func (s *Server) renderShared(w http.ResponseWriter, r *http.Request, status int, t sharedTarget, forms pages.VarForms) {
	rows, err := s.sharedRows(r, t, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, row := range rows {
		t.view.Names = append(t.view.Names, row.Key)
	}
	if forms.Add.ID == "" {
		forms.Add = sharedVarDialog(r, t, "", ui.Form{}, false)
	}
	shell := s.shell(w, r, t.view.Title, t.active, t.crumbs...)
	// A refused dialog is the answer to a POST at an address of its own.
	shell.Address = t.view.Action
	s.render(w, r, status, pages.SharedVariables(shell, t.view, sharedCard(r, t, rows, false), forms))
}

// sharedStored loads the variable an address names, in the scope it names.
// A reader who may not change the scope's variables finds none: the route
// table refuses them before this, and this does not rely on it.
func (s *Server) sharedStored(w http.ResponseWriter, r *http.Request, load sharedLoader) (sharedTarget, db.EnvVar, bool) {
	t, ok := load(w, r)
	if !ok {
		return t, db.EnvVar{}, false
	}
	if !t.view.CanEdit {
		s.notFound(w, r)
		return t, db.EnvVar{}, false
	}
	ev, err := s.DB.SharedVar(r.Context(), sessionFrom(r).TeamID, t.scope, t.id, r.PathValue("name"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return t, ev, false
	}
	if err != nil {
		s.fail(w, r, err)
		return t, ev, false
	}
	return t, ev, true
}

// sharedVar answers a row's eye.
func (s *Server) sharedVar(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ev, ok := s.sharedStored(w, r, load)
		if !ok {
			return
		}
		row, err := s.varRow(ev, r.URL.Query().Get("hide") == "")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, http.StatusOK, pages.VariableRow(sharedFrame(r, t), row))
	}
}

// sharedVarEdit answers Edit: the dialog, which holds the value.
func (s *Server) sharedVarEdit(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ev, ok := s.sharedStored(w, r, load)
		if !ok {
			return
		}
		plain, err := s.Box.OpenString(ev.Value)
		if err != nil {
			s.fail(w, r, errors.New("shared variable "+ev.Key+" cannot be decrypted"))
			return
		}
		s.render(w, r, http.StatusOK, pages.VariableDialog(sharedVarDialog(r, t, ev.Key, varFormOf(ev.Key, plain, false), true)))
	}
}

// sharedVarAdd adds one variable to a scope.
func (s *Server) sharedVarAdd(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := load(w, r)
		if !ok {
			return
		}
		if !t.view.CanEdit {
			s.notFound(w, r)
			return
		}
		s.sharedVarStore(w, r, t, "")
	}
}

// sharedVarSave changes the variable its address names.
func (s *Server) sharedVarSave(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, ev, ok := s.sharedStored(w, r, load); ok {
			s.sharedVarStore(w, r, t, ev.Key)
		}
	}
}

// sharedVarStore adds a variable to a scope (old is "") or changes the one
// called old.
func (s *Server) sharedVarStore(w http.ResponseWriter, r *http.Request, t sharedTarget, old string) {
	ev, f := readVar(r)
	// A shared value is put in as it is and not read again, so a name
	// inside one would reach the container as text.
	if refs := deploy.SharedRefs(ev.Value); len(refs) > 0 {
		f.Fail("value", "This names "+refs[0].String()+". A shared variable cannot take its value from another one; write the value itself.")
	}
	if f.OK() {
		team := sessionFrom(r).TeamID
		sealed, err := s.Box.SealString(ev.Value)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		stored := db.EnvVar{Key: ev.Key, Value: sealed}
		if old == "" {
			err = s.DB.AddSharedVar(r.Context(), team, t.scope, t.id, stored, maxSharedVars)
		} else {
			err = s.DB.UpdateSharedVar(r.Context(), team, t.scope, t.id, old, stored)
		}
		switch {
		case errors.Is(err, db.ErrVarExists):
			f.Fail("key", ev.Key+" exists already. Change that one, or choose another name.")
		case errors.Is(err, db.ErrTooManyVars):
			f.Fail("key", "Keep to "+itoa(maxSharedVars)+" variables here.")
		case errors.Is(err, db.ErrNotFound):
			s.notFound(w, r)
			return
		case err != nil:
			s.fail(w, r, err)
			return
		default:
			setFlash(w, r, ui.ToneOK, ev.Key+" saved. What uses it gets the new value at its next deploy.")
			redirect(w, r, t.view.Action)
			return
		}
	}
	var forms pages.VarForms
	if old == "" {
		forms.Add = sharedVarDialog(r, t, "", f, true)
	} else {
		d := sharedVarDialog(r, t, old, f, true)
		forms.Edit = &d
	}
	s.renderShared(w, r, http.StatusUnprocessableEntity, t, forms)
}

// sharedVarDelete deletes one variable of a scope.
func (s *Server) sharedVarDelete(load sharedLoader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ev, ok := s.sharedStored(w, r, load)
		if !ok {
			return
		}
		if err := s.DB.DeleteSharedVar(r.Context(), sessionFrom(r).TeamID, t.scope, t.id, ev.Key); err != nil && !errors.Is(err, db.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, ev.Key+" deleted. What names it fails to deploy until it is here again.")
		redirect(w, r, t.view.Action)
	}
}
