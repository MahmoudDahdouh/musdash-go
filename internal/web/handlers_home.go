package web

import (
	"net/http"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// How much Home shows. The lists are short on purpose: the page is read
// at a glance, and each has a page of its own behind it.
const (
	homeEvents   = 12
	homeProjects = 6
	// homeEvery is how often Home asks again while something is in
	// progress.
	homeEvery = "5s"
	// homeReadingAge is how old a server's stored reading may be and still
	// be shown as what it uses now. The sampler stores one a minute, so an
	// older one means the server stopped answering.
	homeReadingAge = 5 * time.Minute
)

// homeView reads what Home shows. It asks the database only: what a
// server uses comes from the samples already stored, so opening Home
// never waits on a server.
func (s *Server) homeView(r *http.Request) (pages.HomeView, error) {
	ctx, sess := r.Context(), sessionFrom(r)
	v := pages.HomeView{Admin: db.RoleRank(sess.Role) >= db.RoleRank(db.RoleAdmin)}
	var err error
	if v.Totals, err = s.DB.TeamTotals(ctx, sess.TeamID); err != nil {
		return v, err
	}
	if v.Events, err = s.DB.RecentEvents(ctx, sess.TeamID, homeEvents); err != nil {
		return v, err
	}
	if v.Projects, err = s.DB.ActiveProjects(ctx, sess.TeamID, homeProjects); err != nil {
		return v, err
	}
	servers, err := s.DB.ListServers(ctx, sess.TeamID)
	if err != nil {
		return v, err
	}
	samples, err := s.DB.LatestServerSamples(ctx, sess.TeamID, time.Now().Add(-homeReadingAge).Unix())
	if err != nil {
		return v, err
	}
	for _, server := range servers {
		h := pages.HomeServer{Server: server}
		if sample, ok := samples[server.ID]; ok {
			h.Meters = []ui.MeterProps{
				{Label: "Processor", Value: ui.Percent(float64(sample.CPU) / 100), Share: float64(sample.CPU) / 10000},
				{Label: "Memory", Value: ui.Bytes(float64(sample.Mem)) + " of " + ui.Bytes(float64(sample.MemTotal)), Share: share(sample.Mem, sample.MemTotal)},
				{Label: "Disk", Value: ui.Bytes(float64(sample.DiskUsed)) + " of " + ui.Bytes(float64(sample.DiskTotal)), Share: share(sample.DiskUsed, sample.DiskTotal)},
			}
		}
		v.Servers = append(v.Servers, h)
	}
	for _, e := range v.Events {
		if e.Status == db.DeployQueued || e.Status == db.DeployRunning {
			v.Next, v.Every = nextPoll(r, "/home/live"), homeEvery
			break
		}
	}
	return v, nil
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	v, err := s.homeView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.Home(s.shell(w, r, "Home", "home"), v))
}

// homeLive answers Home's own question while something is in progress.
func (s *Server) homeLive(w http.ResponseWriter, r *http.Request) {
	v, err := s.homeView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.HomeLive(v))
}
