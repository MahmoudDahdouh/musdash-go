package web

import (
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// saveTags stores the tags a settings form names for an app or a service
// and sends the browser back to that page.
func (s *Server) saveTags(w http.ResponseWriter, r *http.Request, kind, id, back string) {
	tags, bad := db.ParseTags(r.PostFormValue("tags"))
	switch {
	case bad != "":
		// The word is not echoed: it can be anything, and a flash is a cookie.
		setFlash(w, r, ui.ToneDanger, "The tags were not saved: one of them is not written as a tag. Use lowercase letters, numbers, dots and hyphens, up to 32 characters each.")
	case len(tags) > db.MaxTags:
		setFlash(w, r, ui.ToneDanger, "The tags were not saved: keep to "+itoa(db.MaxTags)+" tags.")
	default:
		if err := s.DB.SetTags(r.Context(), sessionFrom(r).TeamID, kind, id, tags); err != nil {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, "Tags saved.")
	}
	redirect(w, r, back+"#tags")
}

func (s *Server) appTagsSave(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.saveTags(w, r, db.KindApp, v.App.ID, "/apps/"+v.App.ID+"/settings")
	}
}

func (s *Server) serviceTagsSave(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.saveTags(w, r, db.KindService, v.Service.ID, "/services/"+v.Service.ID+"/settings")
	}
}

func (s *Server) tagList(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListTags(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.Tags(s.shell(w, r, "Tags", "tags"), list))
}

// loadTag fetches what the team has under the tag in the path. A tag
// exists while something has it, so one that nothing has is a 404.
func (s *Server) loadTag(w http.ResponseWriter, r *http.Request) (string, []db.App, []db.Service, bool) {
	tag := r.PathValue("tag")
	if !db.ValidTag(tag) {
		s.notFound(w, r)
		return "", nil, nil, false
	}
	apps, services, err := s.DB.Tagged(r.Context(), sessionFrom(r).TeamID, tag)
	if err != nil {
		s.fail(w, r, err)
		return "", nil, nil, false
	}
	if len(apps)+len(services) == 0 {
		s.notFound(w, r)
		return "", nil, nil, false
	}
	return tag, apps, services, true
}

func (s *Server) tagShow(w http.ResponseWriter, r *http.Request) {
	tag, apps, services, ok := s.loadTag(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, pages.Tag(s.shell(w, r, tag, "tags", ui.Crumb{Label: "Tags", Href: "/tags"}, ui.Crumb{Label: tag}),
		tag, apps, services, s.publicBase(r)))
}

func (s *Server) tagDeploy(w http.ResponseWriter, r *http.Request) {
	tag, _, _, ok := s.loadTag(w, r)
	if !ok {
		return
	}
	queued, tagged, err := s.Deploy.DeployTag(r.Context(), sessionFrom(r).TeamID, tag, "tag")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch {
	case queued == tagged:
		setFlash(w, r, ui.ToneOK, "Deploying "+itoa(queued)+" of "+itoa(tagged)+".")
	case queued == 0:
		setFlash(w, r, ui.ToneNeutral, "Nothing was queued: each of them already has a deployment waiting.")
	default:
		setFlash(w, r, ui.ToneOK, "Deploying "+itoa(queued)+" of "+itoa(tagged)+". The others already have a deployment waiting.")
	}
	redirect(w, r, "/tags/"+tag)
}
