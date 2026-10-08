package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

var (
	// GitHub App names: letters, numbers, spaces and hyphens, up to 34.
	githubAppNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 -]{1,32}[A-Za-z0-9]$`)
	githubOrgRE     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
)

// publicBase works out the address other machines reach this dashboard at:
// the dashboard domain when one is set, otherwise the address in the
// browser's bar.
func (s *Server) publicBase(r *http.Request) string {
	if domain, err := s.DB.Setting(r.Context(), db.SettingInstanceDomain); err == nil && domain != "" {
		return "https://" + domain
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// validBase checks an address a person typed for this dashboard.
func validBase(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// sourceForms is the state of the two dialogs of the Sources page: the
// one that was sent and refused comes back open.
type sourceForms struct {
	app    ui.Form // New GitHub App
	gitlab ui.Form // Connect GitLab
}

func (s *Server) renderSources(w http.ResponseWriter, r *http.Request, status int, f sourceForms) {
	teamID := sessionFrom(r).TeamID
	sources, err := s.DB.ListGitSources(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, ok := f.app.Values["base"]; !ok {
		f.app.Set("base", s.publicBase(r))
	}
	if _, ok := f.app.Values["name"]; !ok {
		f.app.Set("name", "musdash-"+secret.RandomID()[:6])
	}
	if _, ok := f.gitlab.Values["gitlab_base"]; !ok {
		f.gitlab.Set("gitlab_base", "https://gitlab.com")
	}
	s.render(w, r, status, pages.Sources(s.shell(w, r, "Sources", "sources"), sources, f.app, f.gitlab))
}

func (s *Server) sourcesPage(w http.ResponseWriter, r *http.Request) {
	s.renderSources(w, r, http.StatusOK, sourceForms{})
}

// githubManifest is the document GitHub creates an App from.
type githubManifest struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	HookAttributes map[string]any    `json:"hook_attributes"`
	RedirectURL    string            `json:"redirect_url"`
	SetupURL       string            `json:"setup_url"`
	CallbackURLs   []string          `json:"callback_urls"`
	Public         bool              `json:"public"`
	Permissions    map[string]string `json:"default_permissions"`
	Events         []string          `json:"default_events"`
}

// githubStart records a pending GitHub App and shows the page that hands
// the manifest to GitHub. GitHub creates the App and sends the browser back
// with a one-time code.
func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("name"))
	org := strings.TrimSpace(r.PostFormValue("organization"))
	rawBase := r.PostFormValue("base")
	f.Set("name", name)
	f.Set("organization", org)
	f.Set("base", rawBase)

	if !githubAppNameRE.MatchString(name) {
		f.Fail("name", "Use 3 to 34 letters, numbers, spaces or hyphens.")
	}
	if org != "" && !githubOrgRE.MatchString(org) {
		f.Fail("organization", "Enter the organisation's GitHub name, such as acme, or leave it empty for your own account.")
	}
	base, ok := validBase(rawBase)
	if !ok {
		f.Fail("base", "Enter this dashboard's address, such as https://musdash.example.com.")
	}
	if !f.OK() {
		s.renderSources(w, r, http.StatusUnprocessableEntity, sourceForms{app: f})
		return
	}

	state := secret.RandomToken(24)
	src, err := s.DB.StartGitSource(r.Context(), sessionFrom(r).TeamID, name, state)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	manifest, err := json.Marshal(githubManifest{
		Name: name,
		URL:  base,
		// Push events arrive here, signed with a secret GitHub generates.
		HookAttributes: map[string]any{"url": base + "/webhooks/github/" + src.ID, "active": true},
		RedirectURL:    base + "/sources/github/callback",
		SetupURL:       base + "/sources/github/installed",
		CallbackURLs:   []string{base + "/sources/github/callback"},
		Public:         false,
		// Read the code; comment on pull requests for preview deployments.
		Permissions: map[string]string{"contents": "read", "metadata": "read", "pull_requests": "write"},
		Events:      []string{"push", "pull_request"},
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	action := "https://github.com/settings/apps/new"
	if org != "" {
		action = "https://github.com/organizations/" + org + "/settings/apps/new"
	}
	// This one page posts a form to GitHub, so its policy allows that
	// destination in addition to this site.
	w.Header().Set("Content-Security-Policy", strings.Replace(contentPolicy, "form-action 'self'", "form-action 'self' https://github.com", 1))
	s.render(w, r, http.StatusOK, pages.GitHubHandoff(s.shell(w, r, "Connect GitHub", "sources"), name, action+"?state="+url.QueryEscape(state), string(manifest)))
}

// githubCallback finishes the manifest flow: GitHub has created the App and
// redirected back with a code that is exchanged for the App's credentials.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The state ties this redirect to a flow this team started in this
	// install; without a match the code is not exchanged at all.
	pending, err := s.DB.PendingGitSource(ctx, sessionFrom(r).TeamID, r.URL.Query().Get("state"))
	if errors.Is(err, db.ErrNotFound) {
		setFlash(w, r, ui.ToneDanger, "That GitHub link has expired or was already used. Start again from Sources.")
		redirect(w, r, "/sources")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 200 {
		setFlash(w, r, ui.ToneDanger, "GitHub did not return a code. Start again from Sources.")
		redirect(w, r, "/sources")
		return
	}
	res, err := s.GitHub.ConvertManifest(ctx, code)
	if err != nil {
		s.Log.Error("github manifest conversion", "err", err)
		setFlash(w, r, ui.ToneDanger, "GitHub would not finish creating the App: "+err.Error())
		redirect(w, r, "/sources")
		return
	}
	pending.AppID, pending.Slug, pending.HTMLURL, pending.ClientID = res.ID, res.Slug, res.HTMLURL, res.ClientID
	// GitHub's word for what the person typed there. It is shown like a
	// name, so it is held to a name's rule.
	if res.Name != "" && len(res.Name) <= 100 && plainText(res.Name) {
		pending.Name = res.Name
	}
	for dst, plain := range map[*string]string{&pending.ClientSecret: res.ClientSecret, &pending.PrivateKey: res.PEM, &pending.WebhookSecret: res.WebhookSecret} {
		if *dst, err = s.Box.SealString(plain); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := s.DB.FinishGitSource(ctx, pending); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "GitHub App created. Now install it on the repositories you want to deploy.")
	redirect(w, r, "/sources")
}

// githubInstalled is where GitHub sends the browser after the App was
// installed on an account.
func (s *Server) githubInstalled(w http.ResponseWriter, r *http.Request) {
	setFlash(w, r, ui.ToneOK, "Installed. Its repositories can now be deployed from New app.")
	redirect(w, r, "/sources")
}

// gitlabCreate connects a GitLab instance: an address and an access token,
// which GitLab is asked about before anything is stored. The token is the
// person's own and is never sent back: not in a refused form, not on the
// page.
func (s *Server) gitlabCreate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("gitlab_name"))
	rawBase := strings.TrimSpace(r.PostFormValue("gitlab_base"))
	token := strings.TrimSpace(r.PostFormValue("gitlab_token"))
	f.Set("gitlab_name", name)
	f.Set("gitlab_base", rawBase)

	if name == "" || len(name) > 60 || !plainText(name) {
		f.Fail("gitlab_name", labelProblem(name, "Enter a name, up to 60 characters."))
	}
	base, ok := source.GitLabBase(rawBase)
	if !ok {
		f.Fail("gitlab_base", "Enter the address of the GitLab instance, such as https://gitlab.com or https://gitlab.example.com. It must be https, with nothing after the host.")
	}
	if !source.ValidGitLabToken(token) {
		f.Fail("gitlab_token", "Enter the access token as GitLab showed it.")
	}
	var user string
	if f.OK() {
		// Asked before anything is stored: a wrong token, or an address
		// that is no GitLab, is said while the person is still here.
		var err error
		if user, err = s.GitLab.User(r.Context(), base, token); err != nil {
			f.Fail("gitlab_token", sentence(err))
		}
	}
	if !f.OK() {
		s.renderSources(w, r, http.StatusUnprocessableEntity, sourceForms{gitlab: f})
		return
	}
	sealed, err := s.Box.SealString(token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.DB.CreateGitLabSource(r.Context(), sessionFrom(r).TeamID, name, base, user, sealed); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "GitLab connected. Its repositories can now be chosen when you add an app.")
	redirect(w, r, "/sources")
}

// sourceDelete forgets a source of either kind, unless something still
// deploys through it.
func (s *Server) sourceDelete(w http.ResponseWriter, r *http.Request) {
	teamID := sessionFrom(r).TeamID
	src, err := s.DB.GitSource(r.Context(), teamID, r.PathValue("id"))
	if err == nil {
		err = s.DB.DeleteGitSource(r.Context(), teamID, src.ID)
	}
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, db.ErrInUse):
		setFlash(w, r, ui.ToneDanger, "An app or a service still deploys through "+src.Name+". Change or delete it first.")
	case err != nil:
		s.fail(w, r, err)
		return
	case src.Kind == db.GitSourceGitLab:
		setFlash(w, r, ui.ToneOK, "Removed from musdash. The access token itself still works until you revoke it on GitLab.")
	default:
		setFlash(w, r, ui.ToneOK, "Removed from musdash. To delete the App itself, use its settings page on GitHub.")
	}
	redirect(w, r, "/sources")
}

// loadRepoSource fetches the source a repository list is asked of, which
// must be of the kind the address names. Anything else is not found.
func (s *Server) loadRepoSource(w http.ResponseWriter, r *http.Request, kind string) (db.GitSource, bool) {
	src, err := s.DB.GitSource(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && src.Kind != kind) {
		http.NotFound(w, r)
		return src, false
	}
	if err != nil {
		s.fail(w, r, err)
		return src, false
	}
	return src, true
}

// githubRepos lists the repositories a GitHub App can reach, as a fragment
// the New app form loads on request.
func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request) {
	src, ok := s.loadRepoSource(w, r, db.GitSourceGitHubApp)
	if !ok {
		return
	}
	list := pages.RepoPick{Base: "https://github.com", Source: src.ID, None: "This App is not installed on any repository yet. Use Choose repositories on the Sources page."}
	key, err := s.Box.Open(src.PrivateKey)
	if err != nil {
		list.Problem = "The App's key cannot be decrypted. Was the master key changed?"
	} else if list.Repos, err = s.GitHub.Repositories(r.Context(), src.AppID, key); err != nil {
		list.Problem = "GitHub did not return the repositories: " + err.Error()
	}
	s.render(w, r, http.StatusOK, pages.RepoList(list))
}

// gitlabRepos lists the projects a GitLab source's token can read, as the
// same fragment.
func (s *Server) gitlabRepos(w http.ResponseWriter, r *http.Request) {
	src, ok := s.loadRepoSource(w, r, db.GitSourceGitLab)
	if !ok {
		return
	}
	list := pages.RepoPick{Base: src.BaseURL, Source: src.ID, None: "This token is not a member of any project. Type the path of one it can read, such as group/shop."}
	token, err := s.Box.OpenString(src.Token)
	if err != nil {
		list.Problem = "The token cannot be decrypted. Was the master key changed?"
	} else if list.Repos, err = s.GitLab.Projects(r.Context(), src.BaseURL, token); err != nil {
		list.Problem = sentence(err)
	}
	s.render(w, r, http.StatusOK, pages.RepoList(list))
}

// hostTimeout bounds one question to a Git host that a form asks while a
// person waits.
const hostTimeout = 10 * time.Second

// hostOf is the one host a source reads repositories on: github.com for a
// GitHub App, its own instance for a GitLab source.
func hostOf(g db.GitSource) string {
	if g.Kind == db.GitSourceGitLab {
		return source.GitLabHost(g.BaseURL)
	}
	return "github.com"
}

// sourceBranches asks a source's host for a repository's branches.
func (s *Server) sourceBranches(ctx context.Context, src db.GitSource, repo source.Repo) ([]string, error) {
	if src.Kind == db.GitSourceGitLab {
		token, err := s.Box.OpenString(src.Token)
		if err != nil {
			return nil, errSealed
		}
		return s.GitLab.Branches(ctx, src.BaseURL, token, repo.FullName())
	}
	key, err := s.Box.Open(src.PrivateKey)
	if err != nil {
		return nil, errSealed
	}
	return s.GitHub.Branches(ctx, src.AppID, key, repo.Owner, repo.Name)
}

// sourceFiles asks a source's host for the names of the files in one
// folder of a repository at a branch.
func (s *Server) sourceFiles(ctx context.Context, src db.GitSource, repo source.Repo, branch, dir string) ([]string, error) {
	if src.Kind == db.GitSourceGitLab {
		token, err := s.Box.OpenString(src.Token)
		if err != nil {
			return nil, errSealed
		}
		return s.GitLab.Files(ctx, src.BaseURL, token, repo.FullName(), branch, dir)
	}
	key, err := s.Box.Open(src.PrivateKey)
	if err != nil {
		return nil, errSealed
	}
	return s.GitHub.Files(ctx, src.AppID, key, repo.Owner, repo.Name, branch, dir)
}

// errSealed is a source whose credentials cannot be opened.
var errSealed = errors.New("the source's credentials cannot be decrypted: was the master key changed?")

// hostProblem words why a Git host could not be asked about a repository.
func hostProblem(src db.GitSource, err error) string {
	switch {
	case errors.Is(err, source.ErrNotInstalled):
		return "The App is not installed on this repository. Use Choose repositories on the Sources page."
	case errors.Is(err, source.ErrNotThere):
		return "The repository has no such branch or folder, or " + pages.SourceKind(src) + " " + src.Name + " cannot read it."
	}
	return sentence(err)
}

// sourceRepo reads the repository a form names for a source. It must be on
// the source's own host: its credentials go nowhere else.
func sourceRepo(src db.GitSource, raw string) (source.Repo, bool) {
	repo, err := source.ParseRepo(raw)
	return repo, err == nil && repo.Host == hostOf(src)
}

// repoBranches lists the branches of the repository a form has chosen, as
// the fragment the branch picker loads. kind is the source's.
func (s *Server) repoBranches(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		src, ok := s.loadRepoSource(w, r, kind)
		if !ok {
			return
		}
		var list pages.BranchPick
		raw := strings.TrimSpace(r.URL.Query().Get("repo"))
		repo, ok := sourceRepo(src, raw)
		switch {
		case raw == "":
			list.Note = "Choose a repository first."
		case !ok:
			list.Problem = pages.SourceKind(src) + " " + src.Name + " can only read repositories on " + hostOf(src) + "."
		default:
			ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
			defer cancel()
			var err error
			if list.Branches, err = s.sourceBranches(ctx, src, repo); err != nil {
				list.Problem = hostProblem(src, err)
			}
		}
		s.render(w, r, http.StatusOK, pages.BranchList(list))
	}
}

// gitDetect works out how a repository is built from the names of the
// files in the folder a form names, and answers the line under Build with.
// Without a source there is nobody to ask, and the line is empty; so it is
// for values the form itself will refuse.
func (s *Server) gitDetect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var found pages.Detected
	answer := func() { s.render(w, r, http.StatusOK, pages.BuildDetected(found)) }

	kind, id, _ := strings.Cut(q.Get("access"), ":")
	branch := strings.TrimSpace(q.Get("branch"))
	dir := strings.Trim(strings.TrimSpace(q.Get("base_dir")), "/")
	if kind != "source" || !source.ValidBranch(branch) || !source.ValidRelPath(dir) {
		answer()
		return
	}
	src, err := s.DB.GitSource(r.Context(), sessionFrom(r).TeamID, id)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, ok := sourceRepo(src, q.Get("repo"))
	if !ok {
		answer()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	files, err := s.sourceFiles(ctx, src, repo, branch, dir)
	if err != nil {
		found.Problem = hostProblem(src, err)
	} else {
		found.Looked = true
		found.Pack, found.Found = deploy.GuessPack(files)
	}
	answer()
}

func (s *Server) sshKeyCreate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("key_name"))
	f.Set("key_name", name)
	if name == "" || len(name) > 60 || !plainText(name) {
		f.Fail("key_name", labelProblem(name, "Enter a name, up to 60 characters."))
		s.renderKeys(w, r, http.StatusUnprocessableEntity, pages.KeysTabKeys, keysState{keyForm: f})
		return
	}
	// Sent again, this would make a second key pair of the same name.
	if s.sentBefore(w, r, pages.KeysPath) {
		return
	}
	made := false
	defer func() {
		if !made {
			s.notSent(r)
		}
	}()
	public, private, err := source.GenerateDeployKey("musdash")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sealed, err := s.Box.Seal(private)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	key, err := s.DB.CreateSSHKey(r.Context(), sessionFrom(r).TeamID, name, public, sealed)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	made = true
	// Shown straight away, with the public half ready to copy.
	s.renderKeys(w, r, http.StatusOK, pages.KeysTabKeys, keysState{newKey: &key})
}

func (s *Server) sshKeyDelete(w http.ResponseWriter, r *http.Request) {
	err := s.DB.DeleteSSHKey(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, db.ErrInUse):
		setFlash(w, r, ui.ToneDanger, "An app still clones with this key. Change or delete that app first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, r, ui.ToneOK, "Key deleted.")
	}
	redirect(w, r, pages.KeysPath)
}
