package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// latestDeployment is the deployment the Overview shows, the one in
// progress or else the newest, or nil for a service that has none on
// record.
func (s *Server) latestDeployment(ctx context.Context, serviceID string) (*db.ServiceDeployment, error) {
	dep, err := s.DB.CurrentServiceDeployment(ctx, serviceID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dep, nil
}

// serviceLatestDeployment answers the Overview's card about the latest
// deployment when it asks for itself. It reads the database and nothing
// else.
func (s *Server) serviceLatestDeployment(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	last, err := s.latestDeployment(r.Context(), v.Service.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderPolled(w, r, pages.ServiceLatest(v, last))
}

func (s *Server) serviceDeployments(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	list, err := s.DB.ListServiceDeployments(r.Context(), v.Service.ID, 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ServiceDeployments(s.serviceShell(w, r, v), v, list))
}

// loadServiceDeployment fetches the deployment in the path, which must
// belong to the service in the path.
func (s *Server) loadServiceDeployment(w http.ResponseWriter, r *http.Request) (pages.ServiceView, db.ServiceDeployment, bool) {
	v, ok := s.loadService(w, r)
	if !ok {
		return v, db.ServiceDeployment{}, false
	}
	dep, err := s.DB.ServiceDeployment(r.Context(), v.Service.ID, r.PathValue("dep"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, dep, false
	}
	if err != nil {
		s.fail(w, r, err)
		return v, dep, false
	}
	return v, dep, true
}

func (s *Server) serviceDeployment(w http.ResponseWriter, r *http.Request) {
	v, dep, ok := s.loadServiceDeployment(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, pages.ServiceDeployment(under(s.serviceShell(w, r, v), v.Path()+"/deployments"), v, dep))
}

func (s *Server) serviceDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	v, dep, ok := s.loadServiceDeployment(w, r)
	if !ok {
		return
	}
	s.renderPolled(w, r, pages.ServiceDeploymentStatus(v, dep))
}

// serviceDeploymentStream follows a deployment's log file as Server-Sent
// Events: all of it at once when the deployment is over, as it is written
// while it runs.
func (s *Server) serviceDeploymentStream(w http.ResponseWriter, r *http.Request) {
	_, dep, ok := s.loadServiceDeployment(w, r)
	if !ok {
		return
	}
	release, ok := s.takeStream(w)
	if !ok {
		return
	}
	defer release()
	ctx, stop := s.streamContext(r)
	defer stop()
	out := startSSE(w)
	finished := func() bool {
		cur, err := s.DB.ServiceDeploymentByID(ctx, dep.ID)
		return err != nil || cur.Over()
	}
	// A deployment that waits its turn has no log yet: the page says so,
	// where it would show an empty box for as long as the ones before it
	// take (the queue's workers are few, and a server builds one stack
	// from Git at a time).
	// One that ended without a log, stopped or failed before it began,
	// says that too.
	path := s.Cfg.ServiceLogPath(dep.ID)
	if _, err := os.Stat(path); err != nil {
		if finished() {
			io.WriteString(out, "This deployment ended before it wrote anything.\n")
		} else {
			io.WriteString(out, "Waiting for its turn.\n")
		}
	}
	if err := deploy.FollowWhenReady(ctx, path, out, finished); err == nil {
		out.finish()
	}
}
