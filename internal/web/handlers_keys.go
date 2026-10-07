package web

import (
	"errors"
	"net/http"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// keysState is what a request adds to the Keys page: a form that was
// refused, or a secret that was just made and is shown this once.
type keysState struct {
	tokenForm, keyForm   ui.Form
	deployForm, hookForm ui.Form
	newToken             string
	newDeploy            *pages.NewDeployToken
	newKey               *db.SSHKey
}

// made reports whether the page shows something this request made.
func (st keysState) made() bool {
	return st.newToken != "" || st.newDeploy != nil || st.newKey != nil
}

// renderKeys draws one tab of the Keys & tokens page: the team's SSH keys,
// or the person's API tokens with the team's deploy tokens and webhook
// secrets. No secret is in it but one that this very request made.
func (s *Server) renderKeys(w http.ResponseWriter, r *http.Request, status int, tab string, st keysState) {
	ctx := r.Context()
	sess := sessionFrom(r)
	v := pages.KeysView{Tab: tab, Base: s.publicBase(r), NewToken: st.newToken, TokenForm: st.tokenForm,
		NewDeploy: st.newDeploy, DeployForm: st.deployForm, HookForm: st.hookForm, NewKey: st.newKey, KeyForm: st.keyForm}
	var err error
	if tab == pages.KeysTabTokens {
		if v.Tokens, err = s.DB.ListAPITokens(ctx, sess.UserID, sess.TeamID); err == nil {
			v.Owners, err = s.keyOwners(r)
		}
	} else {
		v.Keys, err = s.DB.ListSSHKeys(ctx, sess.TeamID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shell := s.shell(w, r, "Keys & tokens", "keys")
	if st.made() {
		// This page answers a POST. With the tab's own address in place of
		// the POST's, Refresh fetches the tab instead of sending the form
		// again (sentBefore is what holds if it is sent all the same).
		shell.Address = keysTabPath(tab)
	}
	s.render(w, r, status, pages.Keys(shell, v))
}

func keysTabPath(tab string) string {
	if tab == pages.KeysTabTokens {
		return pages.TokensPath
	}
	return pages.KeysPath
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
	reporting := s.pushSources(r)
	for _, a := range apps {
		out = append(out, appKeyOwner(a, places[a.EnvironmentID].ProjectID, where(a.EnvironmentID), reporting))
	}
	for _, m := range services {
		// Only a service from a repository is deployed from outside.
		if m.FromGit() {
			out = append(out, serviceKeyOwner(m, places[m.EnvironmentID].ProjectID, where(m.EnvironmentID), reporting))
		}
	}
	return out, nil
}

// pushSources is the ids of the team's sources that report pushes
// themselves: its GitHub Apps. What deploys through one of them needs no
// webhook of its own; what deploys through a GitLab source does. A list
// that cannot be read is an empty one: a webhook secret is then offered to
// something that does not need it, which harms nothing.
func (s *Server) pushSources(r *http.Request) map[string]bool {
	sources, err := s.DB.ListGitSources(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.Log.Error("list sources", "route", logRoute(r), "err", err)
	}
	out := make(map[string]bool, len(sources))
	for _, g := range sources {
		out[g.ID] = g.ReportsPushes()
	}
	return out
}

func appKeyOwner(a db.App, projectID, where string, reporting map[string]bool) pages.KeyOwner {
	return pages.KeyOwner{Kind: db.KindApp, ID: a.ID, Name: a.Name, Where: where,
		Path: pages.ResourcePath(projectID, a.EnvironmentID, db.KindApp, a.ID), HasToken: a.DeployTokenHash != "",
		Git: a.Source == db.SourceGit, ViaApp: reporting[a.GitSourceID], HasSecret: a.WebhookSecret != ""}
}

func serviceKeyOwner(m db.Service, projectID, where string, reporting map[string]bool) pages.KeyOwner {
	return pages.KeyOwner{Kind: db.KindService, ID: m.ID, Name: m.Name, Where: where,
		Path: pages.ResourcePath(projectID, m.EnvironmentID, db.KindService, m.ID), HasToken: m.DeployTokenHash != "",
		Git: true, ViaApp: reporting[m.GitSourceID], HasSecret: m.WebhookSecret != ""}
}

func (s *Server) keysPage(w http.ResponseWriter, r *http.Request) {
	s.renderKeys(w, r, http.StatusOK, pages.KeysTabKeys, keysState{})
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	s.renderKeys(w, r, http.StatusOK, pages.KeysTabTokens, keysState{})
}

// pickedOwner finds the resource a dialog of the Keys page named, among
// those the dialog listed: the team's own, no preview, and only one that
// fits (lacks the token or the secret that is being made). The list the
// page was drawn from is the only thing consulted, so the dialog cannot be
// sent an id it did not offer.
func (s *Server) pickedOwner(r *http.Request, id string, fits func(pages.KeyOwner) bool) (pages.KeyOwner, bool, error) {
	owners, err := s.keyOwners(r)
	if err != nil {
		return pages.KeyOwner{}, false, err
	}
	for _, o := range owners {
		if o.ID == id && fits(o) {
			return o, true, nil
		}
	}
	return pages.KeyOwner{}, false, nil
}

// keysDeployToken makes a deploy token for the app or service chosen in
// the New deploy token dialog.
func (s *Server) keysDeployToken(w http.ResponseWriter, r *http.Request) {
	// Before the choice is looked at: the form sent again names a resource
	// that has a token by now, and would be refused for that, in a dialog
	// that then offers some other resource.
	if s.sentBefore(w, r, pages.TokensPath) {
		return
	}
	var f ui.Form
	id := r.PostFormValue("deploy_resource")
	f.Set("deploy_resource", id)
	o, ok, err := s.pickedOwner(r, id, pages.KeyOwner.LacksToken)
	if err != nil {
		s.notSent(r)
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.notSent(r)
		f.Fail("deploy_resource", "Choose an app or a service that has no deploy token.")
		s.renderKeys(w, r, http.StatusUnprocessableEntity, pages.KeysTabTokens, keysState{deployForm: f})
		return
	}
	s.newDeployToken(w, r, o)
}

// keysWebhookSecret makes a webhook secret for the app or service chosen
// in the New webhook secret dialog.
func (s *Server) keysWebhookSecret(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	id := r.PostFormValue("hook_resource")
	f.Set("hook_resource", id)
	o, ok, err := s.pickedOwner(r, id, pages.KeyOwner.LacksSecret)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		f.Fail("hook_resource", "Choose an app or a service from a repository that has no webhook secret.")
		s.renderKeys(w, r, http.StatusUnprocessableEntity, pages.KeysTabTokens, keysState{hookForm: f})
		return
	}
	s.newHookSecret(w, r, o)
}

// newDeployToken gives an app or a service a deploy token, in place of the
// one it may have, and shows it: this once, since only its hash is kept.
// owner is a resource a loader returned for the team. The caller has asked
// sentBefore: sent again, this would replace the token that was just shown
// and copied with one nobody has seen.
func (s *Server) newDeployToken(w http.ResponseWriter, r *http.Request, owner pages.KeyOwner) {
	token := "mdt_" + secret.RandomToken(32)
	if err := s.setDeployToken(r, owner, secret.HashToken(token)); err != nil {
		s.notSent(r)
		s.fail(w, r, err)
		return
	}
	// Rendered directly rather than after a redirect, so the token is never
	// placed in a cookie or a URL.
	s.renderKeys(w, r, http.StatusOK, pages.KeysTabTokens, keysState{newDeploy: &pages.NewDeployToken{
		Owner: owner.Name, Token: token, URL: s.publicBase(r) + "/api/v1/deploy?uuid=" + owner.ID}})
}

// revokeDeployToken takes a resource's deploy token away.
func (s *Server) revokeDeployToken(w http.ResponseWriter, r *http.Request, owner pages.KeyOwner) {
	if err := s.setDeployToken(r, owner, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Deploy token revoked.")
	redirect(w, r, pages.TokensPath)
}

func (s *Server) setDeployToken(r *http.Request, owner pages.KeyOwner, hash string) error {
	teamID := sessionFrom(r).TeamID
	if owner.Kind == db.KindService {
		return s.DB.SetServiceDeployToken(r.Context(), teamID, owner.ID, hash)
	}
	return s.DB.SetAppDeployToken(r.Context(), teamID, owner.ID, hash)
}

// newHookSecret creates or replaces the secret of a resource's own push
// webhook. It is not shown here: the Keys page shows it when asked.
func (s *Server) newHookSecret(w http.ResponseWriter, r *http.Request, owner pages.KeyOwner) {
	sealed, err := s.Box.SealString(secret.RandomHex(24))
	if err == nil {
		teamID := sessionFrom(r).TeamID
		if owner.Kind == db.KindService {
			err = s.DB.SetServiceWebhookSecret(r.Context(), teamID, owner.ID, sealed)
		} else {
			err = s.DB.SetAppWebhookSecret(r.Context(), teamID, owner.ID, sealed)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Webhook secret saved. Enter it in the repository's webhook settings.")
	redirect(w, r, pages.TokensPath)
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
		s.showHookSecret(w, r, appKeyOwner(v.App, v.Project.ID, "", s.pushSources(r)), v.App.WebhookSecret)
	}
}

func (s *Server) serviceWebhookShow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.showHookSecret(w, r, serviceKeyOwner(v.Service, v.Project.ID, "", s.pushSources(r)), v.Service.WebhookSecret)
	}
}
