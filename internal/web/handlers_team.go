package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

const (
	invitationLifetime = 7 * 24 * time.Hour
	// maxInvitations bounds how many links can be waiting at once.
	maxInvitations = 50
	// memberResetLifetime is how long a reset link made for a member works.
	memberResetLifetime = time.Hour
)

// hashPassword hashes a new password, a couple at a time like the
// comparisons: each costs about a quarter of a second of CPU.
func (s *Server) hashPassword(r *http.Request, password string) (string, error) {
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	case <-r.Context().Done():
		return "", r.Context().Err()
	}
	return auth.HashPassword(password)
}

// renderTeam draws the Team page. link is a link that was just made, shown
// this once.
func (s *Server) renderTeam(w http.ResponseWriter, r *http.Request, status int, v pages.TeamView, invite, rename ui.Form) {
	ctx := r.Context()
	sess := sessionFrom(r)
	var err error
	if v.Team, err = s.DB.Team(ctx, sess.TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Members, err = s.DB.ListMembers(ctx, sess.TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Invitations, err = s.DB.ListInvitations(ctx, sess.TeamID); err != nil {
		s.fail(w, r, err)
		return
	}
	v.Me, v.Role = sess.UserID, sess.Role
	s.render(w, r, status, pages.Team(s.shell(w, r, "Team", "team"), v, invite, rename))
}

func (s *Server) teamPage(w http.ResponseWriter, r *http.Request) {
	s.renderTeam(w, r, http.StatusOK, pages.TeamView{}, ui.Form{}, ui.Form{})
}

func (s *Server) teamRename(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("name"))
	f.Set("name", name)
	if name == "" || len(name) > 60 {
		f.Fail("name", "Enter a name, up to 60 characters.")
		s.renderTeam(w, r, http.StatusUnprocessableEntity, pages.TeamView{}, ui.Form{}, f)
		return
	}
	if err := s.DB.RenameTeam(r.Context(), sessionFrom(r).TeamID, name); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Team name saved.")
	redirect(w, r, "/team")
}

func (s *Server) invitationCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	var f ui.Form
	email, emailOK := normalEmail(r.PostFormValue("email"))
	f.Set("email", email)
	if !emailOK {
		f.Fail("email", "Enter an email address like you@example.com.")
	}
	// An Admin invites Members. Only an Owner chooses a role, and the
	// choice ends at Admin: an Owner is made by an Owner changing a role.
	role := db.RoleMember
	if want := r.PostFormValue("role"); want != "" && want != db.RoleMember {
		f.Set("role", want)
		switch {
		case want != db.RoleAdmin:
			f.Fail("role", "Choose Member or Admin.")
		case !db.MayInvite(sess.Role, want):
			f.Fail("email", "Only an Owner can invite an Admin.")
		default:
			role = db.RoleAdmin
		}
	}
	if f.OK() {
		waiting, err := s.DB.ListInvitations(ctx, sess.TeamID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(waiting) >= maxInvitations {
			f.Fail("email", "There are "+itoa(maxInvitations)+" invitations waiting. Cancel some first.")
		}
	}
	if !f.OK() {
		s.renderTeam(w, r, http.StatusUnprocessableEntity, pages.TeamView{}, f, ui.Form{})
		return
	}
	token := secret.RandomToken(32)
	_, err := s.DB.CreateInvitation(ctx, db.Invitation{
		TeamID: sess.TeamID, Email: email, Role: role, TokenHash: secret.HashToken(token),
		InvitedBy: sess.UserID, ExpiresAt: time.Now().Add(invitationLifetime).Unix(),
	})
	switch {
	case errors.Is(err, db.ErrHasAccount):
		f.Fail("email", "This person is already a member.")
	case db.IsUnique(err):
		f.Fail("email", "This address is already invited. Cancel that invitation to make a new link.")
	case err != nil:
		s.fail(w, r, err)
		return
	}
	if !f.OK() {
		s.renderTeam(w, r, http.StatusUnprocessableEntity, pages.TeamView{}, f, ui.Form{})
		return
	}
	s.Log.Info("invitation made", "role", role)
	// The link is on this page and nowhere else: not in a redirect, a
	// cookie or the log.
	s.renderTeam(w, r, http.StatusOK, pages.TeamView{
		Link: s.publicBase(r) + "/invite/" + token, LinkFor: email, LinkKind: "invitation",
	}, ui.Form{}, ui.Form{})
}

func (s *Server) invitationDelete(w http.ResponseWriter, r *http.Request) {
	err := s.DB.DeleteInvitation(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Invitation cancelled. Its link no longer works.")
	redirect(w, r, "/team#invitations")
}

// loadMember fetches the member in the path. It answers 404 itself.
func (s *Server) loadMember(w http.ResponseWriter, r *http.Request) (db.Member, bool) {
	m, err := s.DB.Member(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return m, false
	}
	if err != nil {
		s.fail(w, r, err)
		return m, false
	}
	return m, true
}

// loadManaged fetches the member in the path for somebody who may remove
// them or reset how they sign in. The route lets an Admin in; which
// members an Admin may touch is decided here.
func (s *Server) loadManaged(w http.ResponseWriter, r *http.Request) (db.Member, bool) {
	m, ok := s.loadMember(w, r)
	if !ok {
		return m, false
	}
	sess := sessionFrom(r)
	if !db.CanManage(sess.Role, sess.UserID, m) {
		needs := owner.needs()
		if m.UserID == sess.UserID {
			needs = "another Owner"
		}
		s.render(w, r, http.StatusForbidden, pages.Forbidden(s.shell(w, r, "Not allowed", ""), needs))
		return m, false
	}
	return m, true
}

// managedNoMore answers a write that the database refused although the
// page allowed it: the member's role, or the asker's, changed in between.
// It reports whether it wrote the response.
func (s *Server) managedNoMore(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, db.ErrNotAllowed):
		s.render(w, r, http.StatusForbidden, pages.Forbidden(s.shell(w, r, "Not allowed", ""), owner.needs()))
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
	default:
		s.fail(w, r, err)
	}
	return true
}

func (s *Server) memberRole(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMember(w, r)
	if !ok {
		return
	}
	role := r.PostFormValue("role")
	if db.RoleRank(role) == 0 {
		setFlash(w, r, ui.ToneDanger, "Choose Member, Admin or Owner.")
		redirect(w, r, "/team")
		return
	}
	err := s.DB.SetRole(r.Context(), sessionFrom(r).TeamID, m.UserID, role)
	switch {
	case errors.Is(err, db.ErrLastOwner):
		setFlash(w, r, ui.ToneWarn, m.Name+" is the only Owner. Make somebody else an Owner first.")
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		s.Log.Info("role changed", "member", m.UserID, "role", role, "by", sessionFrom(r).UserID)
		note := ""
		if db.RoleRank(role) > db.RoleRank(m.Role) {
			// So that nothing made under the lower role carries over.
			note = " They were signed out, and their API tokens and reset links no longer work."
		}
		setFlash(w, r, ui.ToneOK, m.Name+" is now "+pages.RoleLabel(role)+"."+note)
	}
	redirect(w, r, "/team")
}

func (s *Server) memberReset(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManaged(w, r)
	if !ok {
		return
	}
	token := secret.RandomToken(32)
	sess := sessionFrom(r)
	err := s.DB.CreatePasswordResetBy(r.Context(), sess.TeamID, sess.UserID, m.UserID, secret.HashToken(token), time.Now().Add(memberResetLifetime).Unix())
	if s.managedNoMore(w, r, err) {
		return
	}
	s.Log.Info("reset link made", "member", m.UserID, "by", sessionFrom(r).UserID)
	s.renderTeam(w, r, http.StatusOK, pages.TeamView{
		Link: s.publicBase(r) + "/reset/" + token, LinkFor: m.Name, LinkKind: "reset",
	}, ui.Form{}, ui.Form{})
}

func (s *Server) memberRemove(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManaged(w, r)
	if !ok {
		return
	}
	// The browser enforces the typed confirmation; check it again here so a
	// stray or scripted POST cannot delete an account.
	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), m.Email) {
		setFlash(w, r, ui.ToneDanger, m.Name+" was not removed: the address you typed did not match.")
		redirect(w, r, "/team")
		return
	}
	sess := sessionFrom(r)
	err := s.DB.RemoveMemberBy(r.Context(), sess.TeamID, sess.UserID, m.UserID)
	switch {
	case errors.Is(err, db.ErrNotAllowed):
		s.managedNoMore(w, r, err)
		return
	case errors.Is(err, db.ErrLastOwner):
		setFlash(w, r, ui.ToneWarn, m.Name+" is the only Owner and cannot be removed.")
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		s.Log.Info("member removed", "member", m.UserID, "by", sessionFrom(r).UserID)
		setFlash(w, r, ui.ToneOK, m.Name+" was removed and signed out everywhere.")
	}
	redirect(w, r, "/team")
}

// loadInvitation fetches the invitation an address names, for somebody who
// is signed out. Opening links is limited by address like signing in is:
// a token cannot be guessed, but nothing should invite trying.
func (s *Server) loadInvitation(w http.ResponseWriter, r *http.Request) (db.Invitation, string, bool) {
	if ok, wait := s.logins.Take("invite:" + limiterIP(clientIP(r))); !ok {
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		s.render(w, r, http.StatusTooManyRequests, pages.Message("Too many attempts", "Try again in "+itoa(int(wait.Minutes())+1)+" minutes."))
		return db.Invitation{}, "", false
	}
	if sess := sessionFrom(r); sess != nil {
		// With one team, whoever is signed in is a member already.
		s.render(w, r, http.StatusConflict, pages.Message("You are already signed in",
			"This invitation creates a new account. You are signed in as "+sess.User.Email+"; sign out first if the invitation is for you."))
		return db.Invitation{}, "", false
	}
	inv, err := s.DB.InvitationByHash(r.Context(), secret.HashToken(r.PathValue("token")))
	if errors.Is(err, db.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, pages.InviteInvalid())
		return inv, "", false
	}
	var team db.Team
	if err == nil {
		team, err = s.DB.Team(r.Context(), inv.TeamID)
	}
	if err != nil {
		s.fail(w, r, err)
		return inv, "", false
	}
	// A link that was found is not counted against the address: a person
	// who mistypes their password a few times is not guessing links.
	s.logins.Reset("invite:" + limiterIP(clientIP(r)))
	return inv, team.Name, true
}

func (s *Server) inviteForm(w http.ResponseWriter, r *http.Request) {
	inv, team, ok := s.loadInvitation(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, pages.Invite(ui.Form{}, anonCSRF(w, r), r.PathValue("token"), inv, team))
}

func (s *Server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	inv, team, ok := s.loadInvitation(w, r)
	if !ok {
		return
	}
	token := r.PathValue("token")
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("name"))
	password := r.PostFormValue("password")
	f.Set("name", name)
	if name == "" || len(name) > 80 {
		f.Fail("name", "Enter your name, up to 80 characters.")
	}
	if err := auth.ValidatePassword(password); err != nil {
		f.Fail("password", err.Error())
	}
	if !f.OK() {
		s.render(w, r, http.StatusUnprocessableEntity, pages.Invite(f, anonCSRF(w, r), token, inv, team))
		return
	}
	hash, err := s.hashPassword(r, password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	user, teamID, err := s.DB.AcceptInvitation(r.Context(), secret.HashToken(token), name, hash)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// Used or cancelled between the page and the form.
		s.render(w, r, http.StatusNotFound, pages.InviteInvalid())
		return
	case errors.Is(err, db.ErrHasAccount):
		f.Fail("form", "An account already uses "+inv.Email+". Sign in with it instead.")
		s.render(w, r, http.StatusConflict, pages.Invite(f, anonCSRF(w, r), token, inv, team))
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, user.ID, teamID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("invitation accepted", "member", user.ID, "role", inv.Role)
	redirect(w, r, "/")
}
