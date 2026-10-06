package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// The API reads what the pages show and does what their buttons do. It is
// for scripts: a request proves itself with a person's API token and with
// nothing else. The session cookie is never looked at here, which is what
// keeps a page on another site from calling the API through a signed-in
// browser; and no page accepts a token.
//
// Answers are written out field by field from the structs below. A row is
// never marshalled as it is: rows hold sealed values and hashes.

const (
	// apiTokenPrefix starts every API token, so that one can be told from
	// an app's own deploy token, and found by a scanner if it leaks.
	apiTokenPrefix = "msd_"

	// apiPerMinute is how many calls one token may make in a minute, and
	// apiPerMinuteByAddress how many may come from one address whatever
	// they carry: several pipelines can share an address, and each of
	// their tokens has its own allowance.
	apiPerMinute          = 120
	apiPerMinuteByAddress = 600

	// What one call to the deploy endpoint may name.
	maxDeployIDs  = 20
	maxDeployTags = 10
)

// One wording for every request that does not carry a usable token: a
// wrong one, an expired one, and one whose person was removed.
const apiNoToken = "Send an API token as: Authorization: Bearer <token>. Tokens are made under Keys & tokens."

func apiError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": message})
}

func apiOK(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, v)
}

// bearer returns the token a request carries, or "".
func bearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

// apiToken checks the request's API token and both rate limits. It writes
// the answer itself when the request may not go on.
func (s *Server) apiToken(w http.ResponseWriter, r *http.Request) (db.APIToken, bool) {
	// By address first, before anything is looked up: guesses and floods
	// cost a map lookup to refuse.
	if ok, wait := s.apiAddrs.Take(limiterIP(clientIP(r))); !ok {
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, "Too many requests from this address. Try again shortly.")
		return db.APIToken{}, false
	}
	raw := bearer(r)
	if !strings.HasPrefix(raw, apiTokenPrefix) || len(raw) < len(apiTokenPrefix)+32 || len(raw) > 200 {
		apiError(w, http.StatusUnauthorized, apiNoToken)
		return db.APIToken{}, false
	}
	token, err := s.DB.TokenByHash(r.Context(), secret.HashToken(raw))
	if errors.Is(err, db.ErrNotFound) {
		apiError(w, http.StatusUnauthorized, apiNoToken)
		return db.APIToken{}, false
	}
	if err != nil {
		s.apiFail(w, r, err)
		return db.APIToken{}, false
	}
	if ok, wait := s.apiCalls.Take("token:" + token.ID); !ok {
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, "This token has made "+itoa(apiPerMinute)+" calls in a minute. Try again shortly.")
		return db.APIToken{}, false
	}
	// When it was last used is written at most once a minute, so reading
	// through the API does not become a database write per call.
	if time.Now().Unix()-token.LastUsedAt >= 60 {
		if err := s.DB.TouchAPIToken(r.Context(), token.ID); err != nil {
			s.Log.Error("touch api token", "err", err)
		}
	}
	return token, true
}

// apiHandler is an API route's handler: it is given whose token the
// request carried.
type apiHandler func(w http.ResponseWriter, r *http.Request, t db.APIToken)

// apiNeeds says, for a refusal, what a permission is called on the page
// where tokens are made.
var apiNeeds = map[string]string{
	db.AbilityRead:   "Read",
	db.AbilityWrite:  "Write",
	db.AbilityDeploy: "Deploy",
}

// api wraps an API route. needs is the permission its token must have:
// db.AbilityRead for a route that only reads, db.AbilityDeploy for one
// that starts a deployment, db.AbilityWrite for one that starts or stops
// something else. A permission is not implied by another: only root, and
// reading sensitive data for reading, cover more than their own name
// (db.APIToken.May).
func (s *Server) api(needs string, h apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.apiToken(w, r)
		if !ok {
			return
		}
		if !token.May(needs) {
			apiError(w, http.StatusForbidden, "This token lacks the "+apiNeeds[needs]+" permission. Make one that has it under Keys & tokens.")
			return
		}
		// Every route of this version is something a Member may do. A
		// route for more must check token.Role here: a token never does
		// what its person may not.
		h(w, r, token)
	}
}

func (s *Server) apiFail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "route", logRoute(r), "err", err)
	apiError(w, http.StatusInternalServerError, "The request could not be handled. See the musdash log.")
}

// apiMissing answers a lookup error: 404 for a row that is not there or
// not the team's, which look the same from outside.
func (s *Server) apiMissing(w http.ResponseWriter, r *http.Request, err error, what string) {
	if errors.Is(err, db.ErrNotFound) {
		apiError(w, http.StatusNotFound, "There is no "+what+" with this id.")
		return
	}
	s.apiFail(w, r, err)
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	apiError(w, http.StatusNotFound, "There is no such API route. See the README for the list.")
}

// What the API returns.

type apiApp struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	EnvironmentID string   `json:"environment_id"`
	ServerID      string   `json:"server_id"`
	Source        string   `json:"source"`
	Image         string   `json:"image,omitempty"`
	Repository    string   `json:"repository,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	Status        string   `json:"status"`
	DeployedImage string   `json:"deployed_image,omitempty"`
	Domains       []string `json:"domains,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

func toAPIApp(a db.App) apiApp {
	return apiApp{
		ID: a.ID, Name: a.Name, EnvironmentID: a.EnvironmentID, ServerID: a.ServerID, Source: a.Source, Image: a.Image,
		Repository: a.RepoName, Branch: a.Branch, Status: a.Status, DeployedImage: a.DeployedImage,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

type apiDeployment struct {
	ID         string `json:"id"`
	AppID      string `json:"app_id"`
	Status     string `json:"status"`
	Trigger    string `json:"trigger"`
	Image      string `json:"image,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Error      string `json:"error,omitempty"`
	RollbackOf string `json:"rollback_of,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

func toAPIDeployment(d db.Deployment) apiDeployment {
	return apiDeployment{
		ID: d.ID, AppID: d.AppID, Status: d.Status, Trigger: d.Trigger, Image: d.Image, Commit: d.CommitSHA, Error: d.Error,
		RollbackOf: d.RollbackOf, CreatedAt: d.CreatedAt, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt,
	}
}

type apiDatabase struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	EnvironmentID string `json:"environment_id"`
	ServerID      string `json:"server_id"`
	Engine        string `json:"engine"`
	Image         string `json:"image"`
	Status        string `json:"status"`
	PublicPort    int    `json:"public_port,omitempty"`
	// Password is there only for a token that reads sensitive data.
	Password  string `json:"password,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func toAPIDatabase(m db.Database) apiDatabase {
	return apiDatabase{
		ID: m.ID, Name: m.Name, EnvironmentID: m.EnvironmentID, ServerID: m.ServerID, Engine: m.Engine, Image: m.Image,
		Status: m.Status, PublicPort: m.PublicPort, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

type apiService struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	EnvironmentID string   `json:"environment_id"`
	ServerID      string   `json:"server_id"`
	Template      string   `json:"template"`
	Repository    string   `json:"repository,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	Status        string   `json:"status"`
	Domains       []string `json:"domains,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

func toAPIService(m db.Service) apiService {
	return apiService{
		ID: m.ID, Name: m.Name, EnvironmentID: m.EnvironmentID, ServerID: m.ServerID, Template: m.Template,
		Repository: m.RepoName, Branch: m.Branch, Status: m.Status, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

type apiServer struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	IP            string `json:"ip,omitempty"`
	Status        string `json:"status,omitempty"`
	DockerVersion string `json:"docker_version,omitempty"`
}

type apiEnvironment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiProject struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	Environments []apiEnvironment `json:"environments"`
}

type apiTag struct {
	Tag      string `json:"tag"`
	Apps     int    `json:"apps"`
	Services int    `json:"services"`
}

// apiEnv is one of an app's variables. Value is nil, and so left out, for
// a token that does not read sensitive data: a variable that is set to
// nothing and one whose value is withheld must not look the same.
type apiEnv struct {
	Key   string  `json:"key"`
	Value *string `json:"value,omitempty"`
	Build bool    `json:"build"`
}

// apiQueued is one thing the deploy endpoint was asked to deploy.
type apiQueued struct {
	Kind         string `json:"kind"` // "app" or "service"
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id,omitempty"`
	// Status is "queued", or "waiting" when a deployment that had not
	// started yet will do what this call asked for.
	Status string `json:"status"`
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	team, err := s.DB.Team(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	apiOK(w, http.StatusOK, map[string]any{
		"user":  map[string]string{"id": t.UserID, "name": t.UserName, "email": t.UserEmail},
		"team":  map[string]string{"id": team.ID, "name": team.Name},
		"role":  t.Role,
		"token": map[string]any{"name": t.Name, "abilities": t.AbilityList(), "expires_at": t.ExpiresAt},
	})
}

func (s *Server) apiServers(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	list, err := s.DB.ListServers(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiServer, 0, len(list))
	for _, m := range list {
		out = append(out, apiServer{ID: m.ID, Name: m.Name, Kind: m.Kind, IP: m.IP, Status: m.Status, DockerVersion: m.DockerVersion})
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	list, err := s.DB.ListProjects(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiProject, 0, len(list))
	for _, p := range list {
		envs, err := s.DB.ListEnvironments(r.Context(), p.ID)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		item := apiProject{ID: p.ID, Name: p.Name, Description: p.Description, Environments: make([]apiEnvironment, 0, len(envs))}
		for _, e := range envs {
			item.Environments = append(item.Environments, apiEnvironment{ID: e.ID, Name: e.Name})
		}
		out = append(out, item)
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiTags(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	list, err := s.DB.ListTags(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiTag, 0, len(list))
	for _, m := range list {
		out = append(out, apiTag{Tag: m.Tag, Apps: m.Apps, Services: m.Services})
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiApps(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	var list []db.App
	var err error
	if tag := r.URL.Query().Get("tag"); tag != "" {
		if !db.ValidTag(tag) {
			apiError(w, http.StatusBadRequest, "That is not written as a tag.")
			return
		}
		list, _, err = s.DB.Tagged(r.Context(), t.TeamID, tag)
	} else {
		list, err = s.DB.TeamApps(r.Context(), t.TeamID)
	}
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiApp, 0, len(list))
	for _, a := range list {
		out = append(out, toAPIApp(a))
	}
	apiOK(w, http.StatusOK, out)
}

// apiLoadApp fetches the app in the path for the token's team.
func (s *Server) apiLoadApp(w http.ResponseWriter, r *http.Request, t db.APIToken) (db.App, bool) {
	app, err := s.DB.App(r.Context(), t.TeamID, r.PathValue("id"))
	if err != nil {
		s.apiMissing(w, r, err, "app")
		return app, false
	}
	return app, true
}

func (s *Server) apiApp(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	app, ok := s.apiLoadApp(w, r, t)
	if !ok {
		return
	}
	out := toAPIApp(app)
	domains, err := s.DB.ListDomains(r.Context(), db.KindApp, app.ID)
	if err == nil {
		for _, d := range domains {
			scheme := "http://"
			if d.TLS {
				scheme = "https://"
			}
			out.Domains = append(out.Domains, scheme+d.Host+d.Path)
		}
		out.Tags, err = s.DB.TagsOf(r.Context(), t.TeamID, db.KindApp, app.ID)
	}
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	apiOK(w, http.StatusOK, out)
}

// apiAppEnvs lists an app's variables: their names for any token that
// reads, their values as well for one that reads sensitive data. A value
// is what was stored, so one that names a shared variable shows the name.
// A preview has none of its own and answers with its parent's, which is
// what it runs with.
func (s *Server) apiAppEnvs(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	app, ok := s.apiLoadApp(w, r, t)
	if !ok {
		return
	}
	sealed, err := s.DB.ListEnvVars(r.Context(), db.KindApp, app.ConfigOwner())
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiEnv, 0, len(sealed))
	for _, ev := range sealed {
		item := apiEnv{Key: ev.Key, Build: ev.BuildTime}
		if t.May(db.AbilitySensitive) {
			plain, err := s.Box.OpenString(ev.Value)
			if err != nil {
				s.apiFail(w, r, errors.New("environment variable "+ev.Key+" cannot be decrypted"))
				return
			}
			item.Value = &plain
		}
		out = append(out, item)
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiAppDeployments(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	app, ok := s.apiLoadApp(w, r, t)
	if !ok {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			apiError(w, http.StatusBadRequest, "limit is a number from 1 to 100.")
			return
		}
		limit = n
	}
	list, err := s.DB.ListDeployments(r.Context(), app.ID, limit)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiDeployment, 0, len(list))
	for _, d := range list {
		out = append(out, toAPIDeployment(d))
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiDeployment(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	dep, err := s.DB.DeploymentByID(r.Context(), r.PathValue("id"))
	if err == nil {
		// A deployment is the team's when its app is.
		_, err = s.DB.App(r.Context(), t.TeamID, dep.AppID)
	}
	if err != nil {
		s.apiMissing(w, r, err, "deployment")
		return
	}
	apiOK(w, http.StatusOK, toAPIDeployment(dep))
}

// queueApp queues a deployment of an app as a call from outside does: one
// that has not started yet will deploy whatever this call wanted, so a
// looping pipeline cannot pile up a queue.
func (s *Server) queueApp(ctx context.Context, app db.App, trigger string) (apiQueued, error) {
	out := apiQueued{Kind: "app", ID: app.ID}
	waiting, err := s.DB.QueuedDeployment(ctx, app.ID)
	if err == nil {
		out.DeploymentID, out.Status = waiting.ID, "waiting"
		return out, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return out, err
	}
	dep, err := s.Deploy.Enqueue(ctx, app, trigger)
	if err != nil {
		return out, err
	}
	out.DeploymentID, out.Status = dep.ID, dep.Status
	return out, nil
}

// queueService does the same for a service.
func (s *Server) queueService(ctx context.Context, svc db.Service) (apiQueued, error) {
	out := apiQueued{Kind: "service", ID: svc.ID, Status: "waiting"}
	started, err := s.deployServiceFromOutside(ctx, svc)
	if started {
		out.Status = db.DeployQueued
	}
	return out, err
}

func (s *Server) apiAppDeploy(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	app, ok := s.apiLoadApp(w, r, t)
	if !ok {
		return
	}
	if app.IsPreview() {
		apiError(w, http.StatusConflict, "A preview is deployed by its pull request.")
		return
	}
	queued, err := s.queueApp(r.Context(), app, "api")
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	apiOK(w, http.StatusAccepted, queued)
}

// apiStopped answers a stop: done, refused because a deployment is under
// way, or failed.
func (s *Server) apiStopped(w http.ResponseWriter, r *http.Request, err error, id string) {
	switch {
	case errors.Is(err, deploy.ErrBusy):
		apiError(w, http.StatusConflict, "A deployment is in progress. Try again once it has finished.")
	case err != nil:
		s.apiFail(w, r, err)
	default:
		apiOK(w, http.StatusOK, map[string]string{"id": id, "status": db.AppStopped})
	}
}

func (s *Server) apiAppStop(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	if app, ok := s.apiLoadApp(w, r, t); ok {
		s.apiStopped(w, r, s.Deploy.Stop(r.Context(), app.ID), app.ID)
	}
}

func (s *Server) apiDatabases(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	list, err := s.DB.TeamDatabases(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiDatabase, 0, len(list))
	for _, m := range list {
		out = append(out, toAPIDatabase(m))
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiLoadDatabase(w http.ResponseWriter, r *http.Request, t db.APIToken) (db.Database, bool) {
	m, err := s.DB.Database(r.Context(), t.TeamID, r.PathValue("id"))
	if err != nil {
		s.apiMissing(w, r, err, "database")
		return m, false
	}
	return m, true
}

func (s *Server) apiDatabase(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	m, ok := s.apiLoadDatabase(w, r, t)
	if !ok {
		return
	}
	out := toAPIDatabase(m)
	if t.May(db.AbilitySensitive) && m.Password != "" {
		var err error
		if out.Password, err = s.Box.OpenString(m.Password); err != nil {
			s.apiFail(w, r, errors.New("the database's password cannot be decrypted"))
			return
		}
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiDatabaseStart(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	m, ok := s.apiLoadDatabase(w, r, t)
	if !ok {
		return
	}
	err := s.Deploy.EnqueueDatabase(r.Context(), m, false)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		apiOK(w, http.StatusAccepted, map[string]string{"id": m.ID, "status": "waiting"})
	case err != nil:
		s.apiFail(w, r, err)
	default:
		apiOK(w, http.StatusAccepted, map[string]string{"id": m.ID, "status": db.DeployQueued})
	}
}

func (s *Server) apiDatabaseStop(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	if m, ok := s.apiLoadDatabase(w, r, t); ok {
		s.apiStopped(w, r, s.Deploy.StopDatabase(r.Context(), m.ID), m.ID)
	}
}

func (s *Server) apiServices(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	list, err := s.DB.TeamServices(r.Context(), t.TeamID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := make([]apiService, 0, len(list))
	for _, m := range list {
		out = append(out, toAPIService(m))
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiLoadService(w http.ResponseWriter, r *http.Request, t db.APIToken) (db.Service, bool) {
	m, err := s.DB.Service(r.Context(), t.TeamID, r.PathValue("id"))
	if err != nil {
		s.apiMissing(w, r, err, "service")
		return m, false
	}
	return m, true
}

func (s *Server) apiService(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	m, ok := s.apiLoadService(w, r, t)
	if !ok {
		return
	}
	out := toAPIService(m)
	endpoints, err := s.DB.ListEndpoints(r.Context(), m.ID)
	if err == nil {
		for _, e := range endpoints {
			if e.Host == "" {
				continue
			}
			scheme := "http://"
			if e.TLS {
				scheme = "https://"
			}
			out.Domains = append(out.Domains, scheme+e.Host)
		}
		out.Tags, err = s.DB.TagsOf(r.Context(), t.TeamID, db.KindService, m.ID)
	}
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	apiOK(w, http.StatusOK, out)
}

func (s *Server) apiServiceDeploy(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	m, ok := s.apiLoadService(w, r, t)
	if !ok {
		return
	}
	queued, err := s.queueService(r.Context(), m)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	apiOK(w, http.StatusAccepted, queued)
}

func (s *Server) apiServiceStop(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	if m, ok := s.apiLoadService(w, r, t); ok {
		s.apiStopped(w, r, s.Deploy.StopService(r.Context(), m.ID), m.ID)
	}
}

// splitList reads a comma-separated query value.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// apiDeployMany is the deploy endpoint called with an API token: the apps
// and services named by id, and everything that has one of the tags.
//
//	curl -X POST -H "Authorization: Bearer msd_…" "https://<dashboard>/api/v1/deploy?uuid=<id>,<id>&tag=nightly"
func (s *Server) apiDeployMany(w http.ResponseWriter, r *http.Request, t db.APIToken) {
	ctx := r.Context()
	ids, tags := splitList(r.URL.Query().Get("uuid")), splitList(r.URL.Query().Get("tag"))
	switch {
	case len(ids) == 0 && len(tags) == 0:
		apiError(w, http.StatusBadRequest, "Name what to deploy: uuid=<id>[,<id>…], tag=<tag>[,<tag>…], or both.")
		return
	case len(ids) > maxDeployIDs || len(tags) > maxDeployTags:
		apiError(w, http.StatusBadRequest, "One call takes up to "+itoa(maxDeployIDs)+" ids and "+itoa(maxDeployTags)+" tags.")
		return
	}
	for _, tag := range tags {
		if !db.ValidTag(tag) {
			apiError(w, http.StatusBadRequest, "One of the tags is not written as a tag.")
			return
		}
	}
	// Everything is found before anything is queued: a call that names
	// one unknown id deploys nothing.
	var apps []db.App
	var services []db.Service
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		app, err := s.DB.App(ctx, t.TeamID, id)
		if err == nil {
			if !app.IsPreview() {
				apps = append(apps, app)
				continue
			}
			err = db.ErrNotFound
		}
		if errors.Is(err, db.ErrNotFound) {
			var svc db.Service
			if svc, err = s.DB.Service(ctx, t.TeamID, id); err == nil {
				services = append(services, svc)
				continue
			}
		}
		if errors.Is(err, db.ErrNotFound) {
			apiError(w, http.StatusNotFound, "There is no app or service with the id "+strconv.Quote(clip(id, 40))+". Nothing was deployed.")
			return
		}
		s.apiFail(w, r, err)
		return
	}
	for _, tag := range tags {
		tagApps, tagServices, err := s.DB.Tagged(ctx, t.TeamID, tag)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		for _, app := range tagApps {
			if !seen[app.ID] {
				seen[app.ID] = true
				apps = append(apps, app)
			}
		}
		for _, svc := range tagServices {
			if !seen[svc.ID] {
				seen[svc.ID] = true
				services = append(services, svc)
			}
		}
	}
	out := make([]apiQueued, 0, len(apps)+len(services))
	for _, app := range apps {
		queued, err := s.queueApp(ctx, app, "api")
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		out = append(out, queued)
	}
	for _, svc := range services {
		queued, err := s.queueService(ctx, svc)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		out = append(out, queued)
	}
	apiOK(w, http.StatusAccepted, map[string]any{"deployments": out})
}

// clip cuts a value that is echoed into an answer.
func clip(v string, n int) string {
	if len(v) > n {
		return strings.ToValidUTF8(v[:n], "") + "…"
	}
	return v
}
