package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// tokenLifetimes are the choices the form offers for when a token ends.
var tokenLifetimes = map[string]time.Duration{
	"30":    30 * 24 * time.Hour,
	"90":    90 * 24 * time.Hour,
	"365":   365 * 24 * time.Hour,
	"never": 0,
}

// tokenCreate makes an API token for the signed-in person. The token is
// 32 random bytes, so its SHA-256 is what is stored: there is nothing in
// it for a slower hash to protect.
func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("token_name"))
	ability := r.PostFormValue("token_ability")
	expires := r.PostFormValue("token_expires")
	f.Set("token_name", name)
	f.Set("token_ability", ability)
	f.Set("token_expires", expires)
	if name == "" || len(name) > 60 || !plainText(name) {
		f.Fail("token_name", labelProblem(name, "Enter a name, up to 60 characters."))
	}
	if ability != db.AbilityRead && ability != db.AbilityDeploy {
		f.Fail("token_ability", "Choose what the token may do.")
	}
	lifetime, ok := tokenLifetimes[expires]
	if !ok {
		f.Fail("token_expires", "Choose when the token ends.")
	}
	// A token is a way in that lasts and asks for no second step. Like
	// the second step itself, a session left open is not enough to make one.
	if f.OK() {
		s.passwordAgain(r, &f, "token_password")
	}
	if !f.OK() {
		s.renderAccountWith(w, r, http.StatusUnprocessableEntity, ui.Form{}, ui.Form{}, ui.Form{}, f, "")
		return
	}
	raw := apiTokenPrefix + secret.RandomToken(32)
	t := db.APIToken{UserID: sess.UserID, TeamID: sess.TeamID, Name: name, TokenHash: secret.HashToken(raw), Ability: ability}
	if lifetime > 0 {
		t.ExpiresAt = time.Now().Add(lifetime).Unix()
	}
	_, err := s.DB.CreateAPIToken(r.Context(), t)
	if errors.Is(err, db.ErrTooMany) {
		f.Fail("token_name", "You have "+itoa(db.MaxAPITokens)+" tokens. Revoke one first.")
		s.renderAccountWith(w, r, http.StatusUnprocessableEntity, ui.Form{}, ui.Form{}, ui.Form{}, f, "")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("api token made", "user", sess.UserID, "ability", ability)
	// The token is in this response and nowhere else.
	s.renderAccountWith(w, r, http.StatusOK, ui.Form{}, ui.Form{}, ui.Form{}, ui.Form{}, raw)
}

func (s *Server) tokenDelete(w http.ResponseWriter, r *http.Request) {
	err := s.DB.DeleteAPIToken(r.Context(), sessionFrom(r).UserID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Token revoked. Calls that use it are refused from now on.")
	redirect(w, r, "/account#tokens")
}
