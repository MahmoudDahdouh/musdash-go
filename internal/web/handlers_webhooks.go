package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
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

// hubProof is the one proof a GitHub App's webhook offers.
func hubProof(r *http.Request) source.Proof {
	return source.Proof{Hub256: r.Header.Get("X-Hub-Signature-256")}
}

// anyProof gathers every way a Git host shows that it knows a webhook's
// secret. Which host is calling is not asked: the headers that would say
// so are not signed, and the secret is the same whoever presents it.
func anyProof(r *http.Request) source.Proof {
	p := hubProof(r)
	p.Hub = r.Header.Get("X-Hub-Signature")
	p.Token = r.Header.Get("X-Gitlab-Token")
	if p.Bare = r.Header.Get("X-Gitea-Signature"); p.Bare == "" {
		p.Bare = r.Header.Get("X-Gogs-Signature")
	}
	return p
}

// readSigned reads a webhook body and checks the proof that came with it
// against the secret. Bodies are read one at a time: each is bounded, and
// so is what all of them together can hold in memory.
func (s *Server) readSigned(w http.ResponseWriter, r *http.Request, secret []byte, proof source.Proof) (source.Event, bool) {
	// Best effort: not every ResponseWriter can set a deadline.
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(hookReadTimeout))
	select {
	case s.hookBodies <- struct{}{}:
		defer func() { <-s.hookBodies }()
	case <-r.Context().Done():
		return source.Event{}, false
	}
	body := http.MaxBytesReader(w, r.Body, maxWebhookBytes)
	if len(secret) == 0 {
		// Nothing can verify against a key nobody knows, but the body is
		// read and hashed all the same: otherwise how long a large request
		// takes would tell an id that has a secret from one that has none
		// or does not exist.
		source.ReadHook(body, hookDummyKey, proof)
		return source.Event{}, false
	}
	return source.ReadHook(body, secret, proof)
}

// hookDummyKey stands in for the secret of a resource that has none.
var hookDummyKey = secret.RandomBytes(32)

// deliveryHeaders are where the hosts put the id of a delivery: one that
// stays the same when the delivery is sent again. GitHub, Gitea and Gogs
// each have their own; GitLab's is Idempotency-Key and Bitbucket's
// X-Request-UUID.
var deliveryHeaders = []string{"X-GitHub-Delivery", "X-Gitea-Delivery", "X-Gogs-Delivery", "Idempotency-Key", "X-Request-UUID"}

// deliveryID returns the delivery id of a webhook request, or "" when it
// carries none that can be recorded.
func deliveryID(r *http.Request) string {
	for _, h := range deliveryHeaders {
		if id := r.Header.Get(h); deliveryIDRE.MatchString(id) {
			return id
		}
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
func (s *Server) enqueuePush(w http.ResponseWriter, r *http.Request, apps []db.App, services []db.Service) {
	ctx := r.Context()
	if len(apps) == 0 && len(services) == 0 {
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
	for _, svc := range services {
		started, err := s.deployServiceFromOutside(ctx, svc)
		if err != nil {
			forget, cancel := detached(r, 10*time.Second)
			if ferr := s.DB.ForgetDelivery(forget, delivery); ferr != nil {
				s.Log.Error("forget webhook delivery", "err", ferr)
			}
			cancel()
			s.hookFail(w, r, err)
			return
		}
		if started {
			queued++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": queued})
}

// pullRequest acts on a pull request event: a preview is made or deployed
// again when one is opened or pushed to, and removed when it is closed.
//
// only is the one app a webhook added by hand belongs to; without it the
// event came through a GitHub App and concerns every app of the repository
// that App serves.
func (s *Server) pullRequest(w http.ResponseWriter, r *http.Request, sourceID string, pr source.PullRequest, only *db.App) {
	ctx := r.Context()
	none := func(note string) {
		answer := map[string]any{"previews": 0}
		if note != "" {
			answer["note"] = note
		}
		writeJSON(w, http.StatusOK, answer)
	}
	if pr.Number <= 0 || pr.Repo == "" {
		none("")
		return
	}
	// "synchronized" is how Gitea and Forgejo spell new commits.
	opened := pr.Action == "opened" || pr.Action == "reopened" || pr.Action == "synchronize" || pr.Action == "synchronized"
	if !opened && pr.Action != "closed" {
		// Labels, reviews, edits: nothing that changes what is deployed.
		none("")
		return
	}
	var parents, previews []db.App
	var err error
	switch {
	case only != nil && opened:
		parents = []db.App{*only}
	case only != nil:
		var child db.App
		if child, err = s.DB.Preview(ctx, only.ID, pr.Number); err == nil {
			previews = []db.App{child}
		} else if errors.Is(err, db.ErrNotFound) {
			err = nil
		}
	case opened:
		parents, err = s.DB.AppsForPullRequest(ctx, sourceID, pr.Repo, pr.BaseBranch)
	default:
		previews, err = s.DB.PreviewsForPullRequest(ctx, sourceID, pr.Repo, pr.Number)
	}
	if err != nil {
		s.hookFail(w, r, err)
		return
	}
	if len(parents) == 0 && len(previews) == 0 {
		none("")
		return
	}
	delivery := deliveryID(r)
	if seen, err := s.DB.SeenDelivery(ctx, delivery); err != nil {
		s.hookFail(w, r, err)
		return
	} else if seen {
		none("this delivery was already handled")
		return
	}
	fail := func(err error) {
		forget, cancel := detached(r, 10*time.Second)
		if ferr := s.DB.ForgetDelivery(forget, delivery); ferr != nil {
			s.Log.Error("forget webhook delivery", "err", ferr)
		}
		cancel()
		s.hookFail(w, r, err)
	}
	done, note := 0, ""
	for _, parent := range parents {
		_, err := s.Deploy.SyncPreview(ctx, parent, pr)
		switch {
		case errors.Is(err, deploy.ErrNoPreview):
			// From a fork, into another branch, previews switched off.
		case errors.Is(err, db.ErrPreviewLimit):
			note = "this app has as many previews as it may have"
		case err != nil:
			fail(err)
			return
		default:
			done++
		}
	}
	for _, preview := range previews {
		if err := s.Deploy.ClosePreview(ctx, preview); err != nil {
			fail(err)
			return
		}
		done++
	}
	answer := map[string]any{"previews": done}
	if note != "" {
		answer["note"] = note
	}
	writeJSON(w, http.StatusOK, answer)
}

// deployServiceFromOutside queues a deployment of a service for a push or
// an API call. One that is already waiting will read the repository when
// it starts, so nothing more is queued behind it.
func (s *Server) deployServiceFromOutside(ctx context.Context, svc db.Service) (bool, error) {
	waiting, err := s.DB.ServiceDeployWaiting(ctx, svc.ID)
	if err != nil || waiting {
		return false, err
	}
	// "again": a deployment that is running has already read the
	// repository as it was.
	return true, s.Deploy.EnqueueService(ctx, svc, true)
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
	ev, ok := s.readSigned(w, r, key, hubProof(r))
	if err != nil || !ok {
		http.Error(w, "The signature does not match.", http.StatusUnauthorized)
		return
	}
	push := ev.Push
	if r.Header.Get("X-GitHub-Event") == "pull_request" {
		s.pullRequest(w, r, src.ID, ev.PR, nil)
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
	services, err := s.DB.ServicesForPush(r.Context(), src.ID, push.Repo, push.Branch)
	if err != nil {
		s.hookFail(w, r, err)
		return
	}
	s.enqueuePush(w, r, apps, services)
}

// gitWebhook receives a push webhook that a person added to a repository by
// hand, for apps and services that clone with a deploy key or from a public
// repository. The id in the address is the app's or the service's. The
// repository can be on GitHub, GitLab, Bitbucket, Gitea or Forgejo: each
// proves the secret in its own header and words its events in its own way.
func (s *Server) gitWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.hookAllowed(w, r) {
		return
	}
	// What a push is compared with, whichever kind of resource it is for.
	var (
		git, auto    bool
		branch, repo string
		sealed       string
		svc          db.Service
	)
	app, err := s.DB.AppByID(r.Context(), r.PathValue("id"))
	if err == nil {
		git, auto, branch, repo, sealed = app.Source == db.SourceGit, app.AutoDeploy, app.Branch, app.RepoName, app.WebhookSecret
	} else if errors.Is(err, db.ErrNotFound) {
		if svc, err = s.DB.ServiceByID(r.Context(), r.PathValue("id")); err == nil {
			git, auto, branch, repo, sealed = svc.FromGit(), svc.AutoDeploy, svc.Branch, svc.RepoName, svc.WebhookSecret
		}
	}
	var key []byte
	if err == nil && sealed != "" {
		key, err = s.Box.Open(sealed)
	}
	ev, ok := s.readSigned(w, r, key, anyProof(r))
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
	// A webhook added by hand that also sends pull request events. What
	// the event is about is read from its body: hosts name their event
	// headers differently.
	if ev.PR.Number > 0 {
		if svc.ID != "" || !git {
			writeJSON(w, http.StatusOK, map[string]any{"previews": 0})
			return
		}
		s.pullRequest(w, r, "", ev.PR, &app)
		return
	}
	// Only a push to the resource's own branch deploys. The repository
	// name is compared when the event carries one. One event can name
	// several branches.
	pushed := false
	for _, push := range ev.Pushes {
		if push.Branch == branch && (push.Repo == "" || strings.EqualFold(push.Repo, repo)) {
			pushed = true
		}
	}
	if !git || !auto || branch == "" || !pushed {
		writeJSON(w, http.StatusOK, map[string]any{"deployments": 0})
		return
	}
	if svc.ID != "" {
		s.enqueuePush(w, r, nil, []db.Service{svc})
		return
	}
	s.enqueuePush(w, r, []db.App{app}, nil)
}

// apiDeploy lets an external system, such as a CI pipeline that has just
// pushed an image, start a deployment:
//
//	curl -X POST -H "Authorization: Bearer <token>" "https://<dashboard>/api/v1/deploy?uuid=<app id>"
//
// With a person's API token in place of an app's deploy token, it deploys
// whatever of the team the call names by id or by tag (apiDeployMany).
func (s *Server) apiDeploy(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(bearer(r), apiTokenPrefix) {
		s.api(db.AbilityDeploy, s.apiDeployMany)(w, r)
		return
	}
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
		// Not an app's token: a service's, or nobody's.
		svc, serr := s.DB.ServiceByDeployToken(r.Context(), r.URL.Query().Get("uuid"), secret.HashToken(token))
		if errors.Is(serr, db.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The token does not match this app."})
			return
		}
		if serr == nil {
			_, serr = s.deployServiceFromOutside(r.Context(), svc)
		}
		if serr != nil {
			s.hookFail(w, r, serr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"service_id": svc.ID, "status": "queued"})
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
