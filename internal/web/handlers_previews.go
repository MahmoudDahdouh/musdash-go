package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// ownSettings wraps the pages and actions that change what an app is
// configured with. A preview has no configuration of its own: it is built
// and run with its parent's, so there is nothing to change on it.
func (s *Server) ownSettings(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, err := s.DB.App(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
		if err == nil && app.IsPreview() {
			setFlash(w, r, ui.ToneWarn, "A preview takes its settings, variables and files from the app it previews. Change them there.")
			redirect(w, r, placeInPath(r, db.KindApp))
			return
		}
		// Anything else, a missing app included, is the handler's to answer.
		next(w, r)
	}
}

// loadPreviews fills in an app's previews with their addresses.
func (s *Server) loadPreviews(ctx context.Context, v *pages.AppView) error {
	if v.App.Source != db.SourceGit || v.App.IsPreview() {
		return nil
	}
	list, err := s.DB.Previews(ctx, v.App.ID)
	if err != nil {
		return err
	}
	for _, p := range list {
		row := pages.PreviewRow{App: p}
		domains, err := s.DB.ListDomains(ctx, db.KindApp, p.ID)
		if err != nil {
			return err
		}
		if len(domains) > 0 {
			row.URL = "http://" + domains[0].Host
			if domains[0].TLS {
				row.URL = "https://" + domains[0].Host
			}
		}
		v.Previews = append(v.Previews, row)
	}
	return nil
}

// appPreviewsSave turns previews of pull requests on or off for an app.
func (s *Server) appPreviewsSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	if v.App.Source != db.SourceGit || v.App.IsPreview() {
		s.notFound(w, r)
		return
	}
	var f ui.Form
	on := r.PostFormValue("previews") == "1"
	raw := strings.TrimSpace(r.PostFormValue("preview_domain"))
	f.Set("_submitted", "1")
	f.Set("previews", r.PostFormValue("previews"))
	f.Set("preview_domain", raw)
	domain := ""
	if raw != "" {
		domain = proxy.NormalizeHost(strings.TrimPrefix(raw, "*."))
		// The longest name a preview under it can get must still be a host
		// name.
		longest := deploy.PreviewHost(db.App{Name: v.App.Name, PreviewDomain: domain}, db.Server{}, 1<<31-1)
		switch {
		case !proxy.ValidHost(domain) || !strings.Contains(domain, "."):
			f.Fail("preview_domain", "Enter a domain such as preview.example.com, without http:// or a path.")
		case isGeneratedDomain(domain):
			f.Fail("preview_domain", "Leave this empty to use generated addresses.")
		case !proxy.ValidHost(longest):
			f.Fail("preview_domain", "This domain is too long to put a preview's name in front of.")
		}
	}
	if !f.OK() {
		s.renderAppSettingsWith(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	err := s.DB.SetAppPreviews(r.Context(), sessionFrom(r).TeamID, v.App.ID, on, domain)
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if on {
		setFlash(w, r, ui.ToneOK, "Pull requests get a preview from now on.")
	} else {
		setFlash(w, r, ui.ToneOK, "Previews are off. The ones that exist stay until their pull requests are closed or you remove them.")
	}
	redirect(w, r, v.Path()+"/settings#previews")
}

// appPreviewDelete removes one preview of an app by hand.
func (s *Server) appPreviewDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		s.notFound(w, r)
		return
	}
	preview, err := s.DB.Preview(r.Context(), v.App.ID, number)
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err == nil {
		err = s.Deploy.ClosePreview(r.Context(), preview)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "The preview of pull request #"+strconv.Itoa(number)+" is being removed.")
	redirect(w, r, v.Path()+"/settings#previews")
}
