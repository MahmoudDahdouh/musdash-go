package web

import (
	"errors"
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// keysState is what a request adds to the Keys page: a form that was
// refused, or a secret that was just made and is shown this once.
type keysState struct {
	tokenForm, keyForm ui.Form
	newToken           string
	newDeploy          *pages.NewDeployToken
	newKey             *db.SSHKey
}

// renderKeys draws the Keys & tokens page: the person's API tokens, and
// the deploy tokens, webhook secrets and SSH keys of the team. No secret is
// in it but one that this very request made.
func (s *Server) renderKeys(w http.ResponseWriter, r *http.Request, status int, st keysState) {
	ctx := r.Context()
	sess := sessionFrom(r)
	v := pages.KeysView{Base: s.publicBase(r), NewToken: st.newToken, TokenForm: st.tokenForm,
		NewDeploy: st.newDeploy, NewKey: st.newKey, KeyForm: st.keyForm}
	var err error
	if v.Tokens, err = s.DB.ListAPITokens(ctx, sess.UserID, sess.TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Owners, err = s.keyOwners(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Keys, err = s.DB.ListSSHKeys(ctx, sess.TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.Keys(s.shell(w, r, "Keys & tokens", "keys"), v))
}

// keyOwners lists what can have a deploy token or a webhook secret: the
// team's apps (a preview has neither) and its services from Git.
func (s *Server) keyOwners(r *http.Request) ([]pages.KeyOwner, error) {
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	places, err := s.DB.Places(ctx, teamID)
	if err != nil {
		return nil, err
	}
	where := func(envID string) string {
		p := places[envID]
		return p.Project + " / " + p.Env
	}
	apps, err := s.DB.TeamApps(ctx, teamID)
	if err != nil {
		return nil, err
	}
	services, err := s.DB.TeamServices(ctx, teamID)
	if err != nil {
		return nil, err
	}
	out := make([]pages.KeyOwner, 0, len(apps)+len(services))
	for _, a := range apps {
		out = append(out, appKeyOwner(a, where(a.EnvironmentID)))
	}
	for _, m := range services {
		// Only a service from a repository is deployed from outside.
		if m.FromGit() {
			out = append(out, serviceKeyOwner(m, where(m.EnvironmentID)))
		}
	}
	return out, nil
}

func appKeyOwner(a db.App, where string) pages.KeyOwner {
	return pages.KeyOwner{Kind: db.KindApp, ID: a.ID, Name: a.Name, Where: where, HasToken: a.DeployTokenHash != "",
		Git: a.Source == db.SourceGit, ViaApp: a.GitSourceID != "", HasSecret: a.WebhookSecret != ""}
}

func serviceKeyOwner(m db.Service, where string) pages.KeyOwner {
	return pages.KeyOwner{Kind: db.KindService, ID: m.ID, Name: m.Name, Where: where, HasToken: m.DeployTokenHash != "",
		Git: true, ViaApp: m.GitSourceID != "", HasSecret: m.WebhookSecret != ""}
}

func (s *Server) keysPage(w http.ResponseWriter, r *http.Request) {
	s.renderKeys(w, r, http.StatusOK, keysState{})
}

// showHookSecret answers the Keys page's Show and Hide: the cell of one
// webhook secret, with the secret only when it was asked for. owner is a
// resource a loader returned for the team.
func (s *Server) showHookSecret(w http.ResponseWriter, r *http.Request, owner pages.KeyOwner, sealed string) {
	if sealed == "" {
		s.notFound(w, r)
		return
	}
	plain := ""
	if r.URL.Query().Get("hide") == "" {
		var err error
		if plain, err = s.Box.OpenString(sealed); err != nil {
			s.fail(w, r, errors.New("the webhook secret cannot be decrypted"))
			return
		}
	}
	s.render(w, r, http.StatusOK, pages.HookSecret(owner, plain))
}

func (s *Server) appWebhookShow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.showHookSecret(w, r, appKeyOwner(v.App, ""), v.App.WebhookSecret)
	}
}

func (s *Server) serviceWebhookShow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.showHookSecret(w, r, serviceKeyOwner(v.Service, ""), v.Service.WebhookSecret)
	}
}
