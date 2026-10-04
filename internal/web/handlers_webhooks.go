package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// These endpoints are called by other machines, not by a signed-in browser.
// They carry no session and no CSRF token; each proves itself with a
// signature or a bearer token instead, and each is rate-limited by address.

// maxWebhookBytes is the largest webhook body accepted. A push event is a
// few kilobytes to a few hundred; the bound is what one request can make
// this process read and hash.
const maxWebhookBytes = 5 << 20

// hookReadTimeout bounds how long a caller may take to send its body.
const hookReadTimeout = 30 * time.Second

// deliveryIDRE is the shape of a delivery id worth remembering. GitHub
// sends a UUID. The value is stored, so anything else is treated as absent.
var deliveryIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// readSigned reads a webhook body and checks its signature against the
// secret. Bodies are read one at a time: each is bounded, and so is what all
// of them together can hold in memory.
func (s *Server) readSigned(w http.ResponseWriter, r *http.Request, secret []byte) (source.Push, bool) {
	// Best effort: not every ResponseWriter can set a deadline.
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(hookReadTimeout))
	select {
	case s.hookBodies <- struct{}{}:
		defer func() { <-s.hookBodies }()
	case <-r.Context().Done():
		return source.Push{}, false
	}
	return source.ReadPush(http.MaxBytesReader(w, r.Body, maxWebhookBytes), secret, r.Header.Get("X-Hub-Signature-256"))
}

// deliveryID returns the delivery id of a webhook request, or "" when it
// carries none that can be recorded.
func deliveryID(r *http.Request) string {
	if id := r.Header.Get("X-GitHub-Delivery"); deliveryIDRE.MatchString(id) {
		return id
	}
	return ""
}

// notJSON reports whether a webhook was sent in a form encoding, which is
// signed correctly but cannot be read as an event.
func notJSON(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
}

const notJSONNote = "set the webhook's content type to application/json"

// hookAllowed applies the per-address rate limit of the machine endpoints.
func (s *Server) hookAllowed(w http.ResponseWriter, r *http.Request) bool {
	if ok, wait := s.hooks.Take("hook:" + limiterIP(clientIP(r))); !ok {
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		http.Error(w, "Too many requests.", http.StatusTooManyRequests)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// enqueuePush queues a deployment for each app, skipping apps that already
// have one waiting. A delivery is acted on once: GitHub redelivers on
// timeouts and on request. A delivery that failed is forgotten again, so
// its redelivery is not mistaken for a repeat.
func (s *Server) enqueuePush(w http.ResponseWriter, r *http.Request, apps []db.App) {
	ctx := r.Context()
	if len(apps) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0})
		return
	}
	delivery := deliveryID(r)
	if seen, err := s.DB.SeenDelivery(ctx, delivery); err != nil {
		s.hookFail(w, r, err)
		return
	} else if seen {
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0, "note": "this delivery was already handled"})
		return
	}
	queued := 0
	for _, app := range apps {
		_, err := s.DB.QueuedDeployment(ctx, app.ID)
		if errors.Is(err, db.ErrNotFound) {
			if _, err = s.Deploy.Enqueue(ctx, app, "push"); err == nil {
				queued++
			}
		}
		if err != nil {
			forget, cancel := detached(r, 10*time.Second)
			if ferr := s.DB.ForgetDelivery(forget, delivery); ferr != nil {
				s.Log.Error("forget webhook delivery", "err", ferr)
			}
			cancel()
			s.hookFail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": queued})
}

// githubWebhook receives events from a GitHub App created by musdash.
func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.hookAllowed(w, r) {
		return
	}
	// An unknown source and a bad signature answer the same way, so the
	// endpoint does not reveal which source ids exist.
	src, err := s.DB.GitSourceByID(r.Context(), r.PathValue("id"))
	var key []byte
	if err == nil {
		key, err = s.Box.Open(src.WebhookSecret)
	}
	push, ok := s.readSigned(w, r, key)
	if err != nil || !ok {
		http.Error(w, "The signature does not match.", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" || push.Branch == "" || push.Repo == "" {
		// ping, installation, a tag, a deleted branch: acknowledged, nothing
		// to do.
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0})
		return
	}
	apps, err := s.DB.AppsForPush(r.Context(), src.ID, push.Repo, push.Branch)
	if err != nil {
		s.hookFail(w, r, err)
		return
	}
	s.enqueuePush(w, r, apps)
}

// gitWebhook receives a push webhook that a person added to a repository by
// hand, for apps that clone with a deploy key or from a public repository.
func (s *Server) gitWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.hookAllowed(w, r) {
		return
	}
	app, err := s.DB.AppByID(r.Context(), r.PathValue("id"))
	var key []byte
	if err == nil && app.WebhookSecret != "" {
		key, err = s.Box.Open(app.WebhookSecret)
	}
	push, ok := s.readSigned(w, r, key)
	if err != nil || !ok {
		http.Error(w, "The signature does not match.", http.StatusUnauthorized)
		return
	}
	if notJSON(r) {
		// Signed correctly, but the event cannot be read in this encoding.
		// Say so where the person looks: the host's delivery log.
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0, "note": notJSONNote})
		return
	}
	// Only a push to the app's own branch deploys. The repository name is
	// compared when the event carries one.
	if app.Source != db.SourceGit || !app.AutoDeploy || push.Branch == "" || push.Branch != app.Branch ||
		(push.Repo != "" && !strings.EqualFold(push.Repo, app.RepoName)) {
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0})
		return
	}
	s.enqueuePush(w, r, []db.App{app})
}

// apiDeploy lets an external system, such as a CI pipeline that has just
// pushed an image, start a deployment:
//
//	curl -X POST -H "Authorization: Bearer <token>" "https://<dashboard>/api/v1/deploy?uuid=<app id>"
func (s *Server) apiDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.hookAllowed(w, r) {
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || len(token) < 20 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Send the app's deploy token as: Authorization: Bearer <token>"})
		return
	}
	// The token is looked up by its hash together with the app id, so a
	// wrong token and an unknown app are indistinguishable.
	app, err := s.DB.AppByDeployToken(r.Context(), r.URL.Query().Get("uuid"), secret.HashToken(token))
	if errors.Is(err, db.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The token does not match this app."})
		return
	}
	if err != nil {
		s.hookFail(w, r, err)
		return
	}
	// A deployment that has not started yet will deploy whatever this call
	// wanted deployed, so a looping pipeline cannot pile up a queue.
	if waiting, err := s.DB.QueuedDeployment(r.Context(), app.ID); err == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"deployment_id": waiting.ID, "status": waiting.Status})
		return
	} else if !errors.Is(err, db.ErrNotFound) {
		s.hookFail(w, r, err)
		return
	}
	dep, err := s.Deploy.Enqueue(r.Context(), app, "api")
	if err != nil {
		s.hookFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"deployment_id": dep.ID, "status": dep.Status})
}

func (s *Server) hookFail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "route", logRoute(r), "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The request could not be handled. See the musdash log."})
}
