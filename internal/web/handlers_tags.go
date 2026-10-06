package web

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

const tagRule = "Use lowercase letters, numbers, dots and hyphens, up to 32 characters."

// saveTags stores the tags a settings form names for an app or a service
// and sends the browser back to that page. The form names them twice: the
// team's tags that were ticked, and new ones typed out.
func (s *Server) saveTags(w http.ResponseWriter, r *http.Request, kind, id, back string) {
	tags, bad := db.ParseTags(r.PostFormValue("tags"))
	// A ticked value is one tag as it stands: it is not split into words.
	for _, picked := range r.PostForm["tag"] {
		if !db.ValidTag(picked) {
			bad = picked
		} else if !slices.Contains(tags, picked) {
			tags = append(tags, picked)
		}
	}
	slices.Sort(tags)
	switch {
	case bad != "":
		// The word is not echoed: it can be anything, and a flash is a cookie.
		setFlash(w, r, ui.ToneDanger, "The tags were not saved: one of them is not written as a tag. Use lowercase letters, numbers, dots and hyphens, up to 32 characters each.")
	case len(tags) > db.MaxTags:
		setFlash(w, r, ui.ToneDanger, "The tags were not saved: keep to "+itoa(db.MaxTags)+" tags.")
	default:
		err := s.DB.SetTags(r.Context(), sessionFrom(r).TeamID, kind, id, tags)
		if errors.Is(err, db.ErrTooMany) {
			setFlash(w, r, ui.ToneDanger, "The tags were not saved: the team has "+itoa(db.MaxTeamTags)+" tags already. Delete one on the Tags page first.")
			break
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, "Tags saved.")
	}
	redirect(w, r, back+"#tags")
}

// tagChoices is what a resource's tags dialog shows: the tags it has, and
// every tag of the team to choose from.
func (s *Server) tagChoices(r *http.Request, kind, id string) (has, all []string, err error) {
	teamID := sessionFrom(r).TeamID
	if has, err = s.DB.TagsOf(r.Context(), teamID, kind, id); err != nil {
		return nil, nil, err
	}
	list, err := s.DB.ListTags(r.Context(), teamID)
	for _, t := range list {
		all = append(all, t.Tag)
	}
	return has, all, err
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

// renderTags draws the Tags page; f is the New tag form.
func (s *Server) renderTags(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	list, err := s.DB.ListTags(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.Tags(s.shell(w, r, "Tags", "tags"), list, f))
}

func (s *Server) tagList(w http.ResponseWriter, r *http.Request) {
	s.renderTags(w, r, http.StatusOK, ui.Form{})
}

// tagName reads the name field of the forms that make and rename a tag.
func tagName(r *http.Request, f *ui.Form) string {
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	f.Set("name", name)
	if !db.ValidTag(name) {
		f.Fail("name", tagRule)
	}
	return name
}

// tagCreate makes a tag that nothing has yet.
func (s *Server) tagCreate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := tagName(r, &f)
	if f.OK() {
		switch err := s.DB.CreateTag(r.Context(), sessionFrom(r).TeamID, name); {
		case errors.Is(err, db.ErrTagExists):
			f.Fail("name", "The team already has a tag called "+name+".")
		case errors.Is(err, db.ErrTooMany):
			f.Fail("name", "The team has "+itoa(db.MaxTeamTags)+" tags already. Delete one first.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderTags(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	setFlash(w, r, ui.ToneOK, "Tag created.")
	redirect(w, r, "/tags/"+name)
}

// loadTag fetches the tag in the path and what the team has under it. A
// tag exists from when it is made until it is deleted, with or without
// anything that has it; another team's is not found.
func (s *Server) loadTag(w http.ResponseWriter, r *http.Request) (string, []db.App, []db.Service, bool) {
	tag := r.PathValue("tag")
	teamID := sessionFrom(r).TeamID
	if !db.ValidTag(tag) {
		s.notFound(w, r)
		return "", nil, nil, false
	}
	exists, err := s.DB.TagExists(r.Context(), teamID, tag)
	if err == nil && !exists {
		s.notFound(w, r)
		return "", nil, nil, false
	}
	var apps []db.App
	var services []db.Service
	if err == nil {
		apps, services, err = s.DB.Tagged(r.Context(), teamID, tag)
	}
	if err != nil {
		s.fail(w, r, err)
		return "", nil, nil, false
	}
	return tag, apps, services, true
}

func (s *Server) renderTag(w http.ResponseWriter, r *http.Request, status int, tag string, apps []db.App, services []db.Service, f ui.Form) {
	shell := s.shell(w, r, tag, "tags", ui.Crumb{Label: "Tags", Href: "/tags"}, ui.Crumb{Label: tag, Icon: "tag"})
	s.render(w, r, status, pages.Tag(shell, tag, apps, services, f))
}

func (s *Server) tagShow(w http.ResponseWriter, r *http.Request) {
	if tag, apps, services, ok := s.loadTag(w, r); ok {
		s.renderTag(w, r, http.StatusOK, tag, apps, services, ui.Form{})
	}
}

// tagRename gives a tag another name; what has the tag keeps it.
func (s *Server) tagRename(w http.ResponseWriter, r *http.Request) {
	tag, apps, services, ok := s.loadTag(w, r)
	if !ok {
		return
	}
	var f ui.Form
	name := tagName(r, &f)
	if f.OK() {
		switch err := s.DB.RenameTag(r.Context(), sessionFrom(r).TeamID, tag, name); {
		case errors.Is(err, db.ErrTagExists):
			f.Fail("name", "The team already has a tag called "+name+".")
		case errors.Is(err, db.ErrNotFound):
			s.notFound(w, r)
			return
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderTag(w, r, http.StatusUnprocessableEntity, tag, apps, services, f)
		return
	}
	setFlash(w, r, ui.ToneOK, "Tag renamed.")
	redirect(w, r, "/tags/"+name)
}

// tagDelete removes a tag and takes it off what has it. Nothing that had
// it is touched otherwise.
func (s *Server) tagDelete(w http.ResponseWriter, r *http.Request) {
	tag, _, _, ok := s.loadTag(w, r)
	if !ok {
		return
	}
	err := s.DB.DeleteTag(r.Context(), sessionFrom(r).TeamID, tag)
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Tag deleted.")
	redirect(w, r, "/tags")
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
	case tagged == 0:
		setFlash(w, r, ui.ToneNeutral, "Nothing has this tag yet, so nothing was deployed.")
	case queued == tagged:
		setFlash(w, r, ui.ToneOK, "Deploying "+itoa(queued)+" of "+itoa(tagged)+".")
	case queued == 0:
		setFlash(w, r, ui.ToneNeutral, "Nothing was queued: each of them already has a deployment waiting.")
	default:
		setFlash(w, r, ui.ToneOK, "Deploying "+itoa(queued)+" of "+itoa(tagged)+". The others already have a deployment waiting.")
	}
	redirect(w, r, "/tags/"+tag)
}
