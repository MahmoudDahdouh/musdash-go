package web

import (
	"net/http"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/ops"
	"github.com/MahmoudDahdouh/musdash-go/internal/update"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// updatesAt is where the Settings page says what is known about releases.
const updatesAt = "/settings#updates"

// newerRelease is the tag of a release newer than the one that runs, for
// the sidebar of every page, or "". It reads one stored value and asks
// nobody; a failure costs the note, not the page.
func (s *Server) newerRelease(r *http.Request) string {
	if s.Ops == nil {
		return ""
	}
	latest, err := s.DB.Setting(r.Context(), db.SettingUpdateLatest)
	if err != nil || !update.Newer(latest, s.Ops.Version) {
		return ""
	}
	return latest
}

func updateView(u ops.UpdateState) pages.Update {
	return pages.Update{
		Current: u.Current, Release: update.Release(u.Current),
		Latest: u.Latest, Checked: u.Checked, Auto: u.Auto, Available: u.Available(),
		Command: update.InstallCommand, Notes: update.NotesURL(u.Latest),
	}
}

// updateCheck is the Owner's Check for updates: it asks now and says what
// came of it. The answer is stored, so the sidebar of everybody's next page
// has it.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	latest, err := s.Ops.CheckUpdate(r.Context(), time.Now())
	current := s.Ops.Version
	switch {
	case err != nil:
		// What went wrong is the server's business (a proxy, a name that
		// did not resolve) and goes to its log.
		s.Log.Warn("check for updates", "err", err)
		setFlash(w, r, ui.ToneDanger, "The latest release could not be looked up on GitHub. Check that this server reaches github.com, and try again.")
	case update.Newer(latest, current):
		setFlash(w, r, ui.ToneOK, "musdash "+latest+" is available.")
	case !update.Release(current):
		setFlash(w, r, ui.ToneOK, "The latest release is "+latest+". This is a development build, which is not compared with it.")
	default:
		setFlash(w, r, ui.ToneOK, "musdash "+current+" is up to date.")
	}
	redirect(w, r, updatesAt)
}

// updateAuto switches the daily check on or off.
func (s *Server) updateAuto(w http.ResponseWriter, r *http.Request) {
	on := r.PostFormValue("auto") == "on"
	if err := s.Ops.SetUpdateAuto(r.Context(), on); err != nil {
		s.fail(w, r, err)
		return
	}
	if on {
		setFlash(w, r, ui.ToneOK, "musdash looks for a new release once a day.")
	} else {
		setFlash(w, r, ui.ToneOK, "musdash no longer looks for a new release by itself.")
	}
	redirect(w, r, updatesAt)
}
