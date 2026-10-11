package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// The search in the bar (ui/search.templ) asks here as it is typed in.

const (
	// searchLimit is how many rows an answer holds. A longer list is not
	// read; it is narrowed by typing more.
	searchLimit = 20
	// searchOffers is how many of them may be things of the catalogue that
	// could be added. It has several hundred, and the list is first of all
	// what the team has.
	searchOffers = 6
	// maxSearchText is how much of what was typed is looked at, in
	// characters. The dialog's field takes no more; a request made by hand
	// is cut to it and answered, not refused.
	maxSearchText = 100
)

// searchWords is what a search looks for: the words of what was typed, of
// its first maxSearchText characters, and no more of them than the
// database takes.
func searchWords(text string) []string {
	if r := []rune(text); len(r) > maxSearchText {
		text = string(r[:maxSearchText])
	}
	words := strings.Fields(text)
	if len(words) > db.MaxSearchWords {
		words = words[:db.MaxSearchWords]
	}
	return words
}

// search answers the dialog with the rows for what was typed: the pages
// this person can open, what their team has, and what the catalogue has
// that they could add, best first. With nothing
// typed it is the pages alone, from no query. Whose rows they are is the
// session's to say. The answer is a 200 whatever happened, as a
// switcher's is: htmx puts nothing else into the list, and the rows of the
// search before would be left standing under new text.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	v := pages.SearchResults{Words: searchWords(r.URL.Query().Get("q")), Limit: searchLimit}
	// The pages are the sidebar's for this role: a Member is not offered
	// the page their request would be refused at.
	v.Rows = pages.PageRows(ui.Destinations(ui.Shell{Admin: db.RoleRank(sess.Role) >= db.RoleRank(db.RoleAdmin), Dev: s.Cfg.Dev}), v.Words)
	if len(v.Words) > 0 {
		// One more than is shown, to know whether there are more.
		hits, err := s.DB.Search(r.Context(), sess.TeamID, v.Words, searchLimit+1)
		if err != nil {
			s.Log.Error("search", "route", logRoute(r), "err", err)
			s.render(w, r, http.StatusOK, pages.SearchOptions(pages.SearchResults{Problem: "The search could not be done. Try again."}))
			return
		}
		for _, h := range hits {
			v.Rows = append(v.Rows, pages.HitRow(h))
		}
		// Pages and hits are each in their order already; by rank alone,
		// a page stays before a hit that fits as well.
		sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Rank < v.Rows[j].Rank })
		// What could be added is a list of its own, after what the team
		// has, however well a name fits: a database found by its engine is
		// what "postgres" means before the engine itself is. It has its
		// few rows of the answer whatever else was found.
		projectID, envID := searchedFrom(r)
		v.Offers = pages.OfferRows(v.Words, searchOffers, projectID, envID)
		if room := searchLimit - len(v.Offers); len(v.Rows) > room {
			v.Rows, v.More = v.Rows[:room], true
		}
	}
	s.render(w, r, http.StatusOK, pages.SearchOptions(v))
}
