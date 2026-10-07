package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
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
		c.Access = append(c.Access, pages.AccessOption{Value: "source:" + g.ID, Label: pages.SourceKind(g) + ": " + g.Name})
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
	case app.GitSourceID != "" && repo.Host != sourceHost(c, app.GitSourceID):
		// A source's credentials are its own host's, and go nowhere else.
		f.Fail("repo", pages.SourceKind(chosenSource(c, app.GitSourceID))+" "+chosenSource(c, app.GitSourceID).Name+" can only read repositories on "+sourceHost(c, app.GitSourceID)+".")
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

// chosenSource is the source of the team with this id, among the ones the
// form offered.
func chosenSource(c pages.GitChoices, id string) db.GitSource {
	for _, g := range c.Sources {
		if g.ID == id {
			return g
		}
	}
	return db.GitSource{}
}

// sourceHost is the one host a source reads repositories on: github.com
// for a GitHub App, its own instance for a GitLab source.
func sourceHost(c pages.GitChoices, id string) string {
	if g := chosenSource(c, id); g.Kind == db.GitSourceGitLab {
		return source.GitLabHost(g.BaseURL)
	}
	return "github.com"
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
	if !deploy.ValidPack(app.BuildPack) {
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

// renderAppSettings draws the Settings page with the given form states.
func (s *Server) renderAppSettings(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, general, src ui.Form) {
	s.renderAppSettingsForms(w, r, status, v, general, src, ui.Form{})
}

// renderAppSettingsWith draws the Settings page with the previews form in
// the given state.
func (s *Server) renderAppSettingsWith(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, previews ui.Form) {
	s.renderAppSettingsForms(w, r, status, v, ui.Form{}, ui.Form{}, previews)
}

func (s *Server) renderAppSettingsForms(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, general, src, previews ui.Form) {
	if v.App.IsPreview() {
		// Nothing of its own to set: where it comes from, and how to
		// remove it.
		s.render(w, r, status, pages.PreviewSettings(s.appShell(w, r, v), v))
		return
	}
	if err := s.loadPreviews(r.Context(), &v); err != nil {
		s.fail(w, r, err)
		return
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Servers, err = s.DB.ListServers(r.Context(), sessionFrom(r).TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Tags, v.TeamTags, err = s.tagChoices(r, db.KindApp, v.App.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.AppSettings(s.appShell(w, r, v), v, general, src, previews, choices))
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
		s.renderAppSettings(w, r, http.StatusUnprocessableEntity, v, ui.Form{}, f)
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
	s.newHookSecret(w, r, appKeyOwner(v.App, "", s.pushSources(r)))
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
	if r.PostFormValue("revoke") == "1" {
		s.revokeDeployToken(w, r, appKeyOwner(v.App, "", s.pushSources(r)))
		return
	}
	if s.sentBefore(w, r, pages.TokensPath) {
		return
	}
	s.newDeployToken(w, r, appKeyOwner(v.App, "", s.pushSources(r)))
}
