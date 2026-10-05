package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// gitChoices lists the ways the team can reach a repository.
func (s *Server) gitChoices(r *http.Request) (pages.GitChoices, error) {
	teamID := sessionFrom(r).TeamID
	c := pages.GitChoices{Access: []pages.AccessOption{{Value: "public", Label: "Nothing: the repository is public"}}}
	sources, err := s.DB.ListGitSources(r.Context(), teamID)
	if err != nil {
		return c, err
	}
	keys, err := s.DB.ListSSHKeys(r.Context(), teamID)
	if err != nil {
		return c, err
	}
	c.Sources = sources
	for _, g := range sources {
		c.Access = append(c.Access, pages.AccessOption{Value: "source:" + g.ID, Label: "GitHub App: " + g.Name})
	}
	for _, k := range keys {
		c.Access = append(c.Access, pages.AccessOption{Value: "key:" + k.ID, Label: "Deploy key: " + k.Name})
	}
	return c, nil
}

// parseRepoForm reads and validates the fields that say which repository
// and branch to read, and how: the part of the Git forms that apps and
// services share.
func parseRepoForm(r *http.Request, f *ui.Form, c pages.GitChoices, app *db.App) {
	field := func(key string) string {
		v := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, v)
		return v
	}
	f.Set("_submitted", "1")

	access := field("access")
	offered := false
	for _, a := range c.Access {
		offered = offered || a.Value == access
	}
	app.GitSourceID, app.SSHKeyID = "", ""
	switch kind, id, _ := strings.Cut(access, ":"); {
	case !offered:
		f.Fail("access", "Choose how the repository is read.")
	case kind == "source":
		app.GitSourceID = id
	case kind == "key":
		app.SSHKeyID = id
	}

	app.RepoURL = field("repo")
	repo, err := source.ParseRepo(app.RepoURL)
	switch {
	case err != nil:
		f.Fail("repo", sentence(err))
	case app.SSHKeyID != "" && !repo.SSH:
		f.Fail("repo", "A deploy key needs the SSH form of the address, such as git@"+repo.Host+":"+repo.FullName()+".git.")
	case app.SSHKeyID == "" && app.GitSourceID == "" && repo.SSH:
		f.Fail("repo", "An SSH address needs a deploy key. Choose one above, or use the https:// address of a public repository.")
	case app.GitSourceID != "" && repo.Host != "github.com":
		f.Fail("repo", "A GitHub App can only read repositories on github.com.")
	default:
		app.RepoName = strings.ToLower(repo.FullName())
	}

	app.Branch = field("branch")
	if !source.ValidBranch(app.Branch) {
		f.Fail("branch", "Enter a branch name such as main.")
	}
	app.AutoDeploy = r.PostFormValue("auto_deploy") == "1"
	f.Set("auto_deploy", map[bool]string{true: "1", false: "0"}[app.AutoDeploy])
}

// insideRepo is the message for a path that must lie in the repository.
const insideRepo = "Enter a path inside the repository, such as apps/web, without a leading slash."

// parseGitForm reads and validates the repository and build fields into app.
func parseGitForm(r *http.Request, f *ui.Form, c pages.GitChoices, app *db.App) {
	field := func(key string) string {
		v := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, v)
		return v
	}
	parseRepoForm(r, f, c, app)
	app.BuildPack = field("build_pack")
	if app.BuildPack != deploy.PackDockerfile && app.BuildPack != deploy.PackStatic {
		f.Fail("build_pack", "Choose how the app is built.")
	}
	if app.BaseDir = strings.Trim(field("base_dir"), "/"); !source.ValidRelPath(app.BaseDir) {
		f.Fail("base_dir", insideRepo)
	}
	if app.DockerfilePath = field("dockerfile_path"); !source.ValidRelPath(app.DockerfilePath) {
		f.Fail("dockerfile_path", insideRepo)
	}
	if app.PublishDir = strings.Trim(field("publish_dir"), "/"); !source.ValidRelPath(app.PublishDir) {
		f.Fail("publish_dir", insideRepo)
	}
	app.SPAFallback = r.PostFormValue("spa_fallback") == "1"
	f.Set("spa_fallback", map[bool]string{true: "1", false: "0"}[app.SPAFallback])
}

// triggers gathers what the Settings page shows about starting deployments
// from outside.
func (s *Server) triggers(r *http.Request, app db.App, newToken string) (pages.Triggers, error) {
	base := s.publicBase(r)
	tr := pages.Triggers{
		WebhookURL:   base + "/webhooks/git/" + app.ID,
		DeployURL:    base + "/api/v1/deploy?uuid=" + app.ID,
		HasToken:     app.DeployTokenHash != "",
		NewToken:     newToken,
		ViaGitHubApp: app.GitSourceID != "",
	}
	if app.WebhookSecret != "" {
		plain, err := s.Box.OpenString(app.WebhookSecret)
		if err != nil {
			return tr, errors.New("the webhook secret cannot be decrypted")
		}
		tr.WebhookSecret = plain
	}
	return tr, nil
}

// renderAppSettings draws the Settings page with the given form states.
func (s *Server) renderAppSettings(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, general, domain, src ui.Form, newToken string) {
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tr, err := s.triggers(r, v.App, newToken)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Servers, err = s.DB.ListServers(r.Context(), sessionFrom(r).TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.AppSettings(s.appShell(w, r, v), v, general, domain, src, choices, tr))
}

func (s *Server) appSourceSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	if v.App.Source != db.SourceGit {
		s.notFound(w, r)
		return
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var f ui.Form
	app := v.App
	parseGitForm(r, &f, choices, &app)
	if app.BuildPack == deploy.PackStatic {
		// The generated static image always listens on 80.
		app.Port = 80
	}
	if f.OK() {
		// A source or key of another team is refused by the query itself.
		if err := s.DB.UpdateAppSource(r.Context(), sessionFrom(r).TeamID, app); errors.Is(err, db.ErrNotFound) {
			f.Fail("access", "Choose how the repository is read.")
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderAppSettings(w, r, http.StatusUnprocessableEntity, v, ui.Form{}, ui.Form{}, f, "")
		return
	}
	setFlash(w, r, ui.ToneOK, "Source saved. Redeploy to build from it.")
	redirect(w, r, "/apps/"+app.ID+"/settings")
}

// appWebhookSecret creates or replaces the secret of the app's own push
// webhook.
func (s *Server) appWebhookSecret(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	sealed, err := s.Box.SealString(secret.RandomHex(24))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetAppWebhookSecret(r.Context(), sessionFrom(r).TeamID, v.App.ID, sealed); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Webhook secret saved. Enter it in the repository's webhook settings.")
	redirect(w, r, "/apps/"+v.App.ID+"/settings#triggers")
}

// appBuildServer chooses the server a Git app's image is built on.
func (s *Server) appBuildServer(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	if v.App.Source != db.SourceGit {
		s.notFound(w, r)
		return
	}
	err := s.DB.SetAppBuildServer(r.Context(), sessionFrom(r).TeamID, v.App.ID, r.PostFormValue("build_server"))
	if errors.Is(err, db.ErrNotFound) {
		// Not a server of this team.
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Saved. The next deployment builds there.")
	redirect(w, r, "/apps/"+v.App.ID+"/settings")
}

// appDeployToken creates, replaces or revokes the app's deploy token. A new
// token is shown once: only its hash is kept.
func (s *Server) appDeployToken(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	teamID := sessionFrom(r).TeamID
	if r.PostFormValue("revoke") == "1" {
		if err := s.DB.SetAppDeployToken(r.Context(), teamID, v.App.ID, ""); err != nil {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, "Deploy token revoked.")
		redirect(w, r, "/apps/"+v.App.ID+"/settings#triggers")
		return
	}
	token := "mdt_" + secret.RandomToken(32)
	hash := secret.HashToken(token)
	if err := s.DB.SetAppDeployToken(r.Context(), teamID, v.App.ID, hash); err != nil {
		s.fail(w, r, err)
		return
	}
	v.App.DeployTokenHash = hash
	// Rendered directly rather than after a redirect, so the token is never
	// placed in a cookie or a URL.
	s.renderAppSettings(w, r, http.StatusOK, v, ui.Form{}, ui.Form{}, ui.Form{}, token)
}
