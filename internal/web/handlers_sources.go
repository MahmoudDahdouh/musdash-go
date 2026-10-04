package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
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

func (s *Server) renderSources(w http.ResponseWriter, r *http.Request, status int, appForm, keyForm ui.Form, newKey *db.SSHKey) {
	teamID := sessionFrom(r).TeamID
	sources, err := s.DB.ListGitSources(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	keys, err := s.DB.ListSSHKeys(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, ok := appForm.Values["base"]; !ok {
		appForm.Set("base", s.publicBase(r))
	}
	if _, ok := appForm.Values["name"]; !ok {
		appForm.Set("name", "musdash-"+secret.RandomID()[:6])
	}
	s.render(w, r, status, pages.Sources(s.shell(w, r, "Sources", "sources"), sources, keys, appForm, keyForm, newKey))
}

func (s *Server) sourcesPage(w http.ResponseWriter, r *http.Request) {
	s.renderSources(w, r, http.StatusOK, ui.Form{}, ui.Form{}, nil)
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
		s.renderSources(w, r, http.StatusUnprocessableEntity, f, ui.Form{}, nil)
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
	if res.Name != "" {
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

func (s *Server) githubDelete(w http.ResponseWriter, r *http.Request) {
	err := s.DB.DeleteGitSource(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, db.ErrInUse):
		setFlash(w, r, ui.ToneDanger, "An app still deploys through this GitHub App. Change or delete that app first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, r, ui.ToneOK, "Removed from musdash. To delete the App itself, use its settings page on GitHub.")
	}
	redirect(w, r, "/sources")
}

// githubRepos lists the repositories a GitHub App can reach, as a fragment
// the New app form loads on request.
func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request) {
	src, err := s.DB.GitSource(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	key, err := s.Box.Open(src.PrivateKey)
	if err != nil {
		s.render(w, r, http.StatusOK, pages.RepoList(nil, "The App's key cannot be decrypted. Was the master key changed?"))
		return
	}
	repos, err := s.GitHub.Repositories(r.Context(), src.AppID, key)
	if err != nil {
		s.render(w, r, http.StatusOK, pages.RepoList(nil, "GitHub did not return the repositories: "+err.Error()))
		return
	}
	s.render(w, r, http.StatusOK, pages.RepoList(repos, ""))
}

func (s *Server) sshKeyCreate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("key_name"))
	f.Set("key_name", name)
	if name == "" || len(name) > 60 {
		f.Fail("key_name", "Enter a name, up to 60 characters.")
		s.renderSources(w, r, http.StatusUnprocessableEntity, ui.Form{}, f, nil)
		return
	}
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
	// Shown straight away, with the public half ready to copy.
	s.renderSources(w, r, http.StatusOK, ui.Form{}, ui.Form{}, &key)
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
		setFlash(w, r, ui.ToneOK, "Deploy key deleted.")
	}
	redirect(w, r, "/sources")
}
