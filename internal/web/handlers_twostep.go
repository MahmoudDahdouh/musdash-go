package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

const (
	// twoStepCookie carries, between the password and the code, which
	// account the password was right for. It is sealed with the master
	// key and is not a session: nothing is signed in until the code is.
	twoStepCookie = "musdash_2fa"
	twoStepWindow = 5 * time.Minute
	// twoStepMark starts the cookie's sealed text, so that no other value
	// this install seals could be passed off as one.
	twoStepMark = "2fa1"

	twoStepIssuer = "musdash"

	wrongCode = "That code is not right. Codes change every thirty seconds, and each works once."
)

// errTOTPKey is what a second step whose key cannot be opened fails with.
var errTOTPKey = errors.New("the second step's key cannot be decrypted: was the master key changed?")

// codeAllowed counts one attempt at a person's second step. The count is
// by account and not by address: a code has a million values and three
// are right at any moment, so somebody with the password and many
// addresses could otherwise try them all.
func (s *Server) codeAllowed(userID string) (bool, time.Duration) {
	return s.logins.Take("code:" + userID)
}

// secondStep reports whether what was typed is a code the person's app
// shows now, or one of their recovery codes, and uses it up.
func (s *Server) secondStep(ctx context.Context, user db.User, typed string) (bool, error) {
	if auth.IsTOTPCode(typed) {
		key, err := s.Box.OpenString(user.TOTPSecret)
		if err != nil || key == "" {
			return false, errTOTPKey
		}
		step, ok := auth.TOTPCheck(key, typed, time.Now(), user.TOTPStep)
		if !ok {
			return false, nil
		}
		// Checked again where it is stored: of two requests with one
		// code, one finds the step taken.
		err = s.DB.UseTOTPStep(ctx, user.ID, step)
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	code := auth.NormalRecoveryCode(typed)
	if len(code) != 12 {
		return false, nil
	}
	err := s.DB.UseRecoveryCode(ctx, user.ID, secret.HashToken(code))
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// passwordAgain checks the signed-in person's password once more, for a
// change that a session left open must not be enough for. It records the
// failure on the form field.
func (s *Server) passwordAgain(r *http.Request, f *ui.Form, field string) bool {
	sess := sessionFrom(r)
	key := "password:" + sess.UserID
	if ok, wait := s.logins.Take(key); !ok {
		f.Fail(field, "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		return false
	}
	if !s.checkPassword(r, sess.User.PasswordHash, r.PostFormValue(field)) {
		f.Fail(field, "That is not your current password.")
		return false
	}
	s.logins.Reset(key)
	return true
}

// renderAccount draws the Account page.
func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, profile, password, twoStep ui.Form) {
	sess := sessionFrom(r)
	v := pages.AccountView{TwoStep: sess.User.TwoStep()}
	if v.TwoStep {
		var err error
		if v.RecoveryCodes, err = s.DB.CountRecoveryCodes(r.Context(), sess.UserID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.render(w, r, status, pages.Account(s.shell(w, r, "Account", "account"), profile, password, twoStep, v))
}

// newRecoveryCodes makes a set of recovery codes and their hashes.
func newRecoveryCodes() (codes, hashes []string) {
	for range auth.RecoveryCodes {
		c := auth.NewRecoveryCode()
		codes = append(codes, c)
		hashes = append(hashes, secret.HashToken(auth.NormalRecoveryCode(c)))
	}
	return codes, hashes
}

// twoStepStart makes a key and shows it. Nothing is on yet: the key waits
// until a code proves the person's app has it.
func (s *Server) twoStepStart(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.User.TwoStep() {
		setFlash(w, r, ui.ToneNeutral, "Two-step sign-in is already on.")
		redirect(w, r, "/account#two-step")
		return
	}
	var f ui.Form
	if !s.passwordAgain(r, &f, "start_password") {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, ui.Form{}, ui.Form{}, f)
		return
	}
	key := auth.NewTOTPKey()
	sealed, err := s.Box.SealString(key)
	if err == nil {
		err = s.DB.SetTOTPPending(r.Context(), sess.UserID, sealed)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The key is in this response and no other: a later request, which
	// has only the session to show, never sees it again.
	s.render(w, r, http.StatusOK, pages.TwoStepSetup(s.shell(w, r, "Two-step sign-in", "account"),
		key, auth.TOTPLink(twoStepIssuer, sess.User.Email, key), ui.Form{}))
}

// twoStepConfirm turns the second step on when the code is right for the
// key that was shown.
func (s *Server) twoStepConfirm(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.User.TwoStep() || sess.User.TOTPPending == "" {
		setFlash(w, r, ui.ToneNeutral, "Start setting up two-step sign-in from here.")
		redirect(w, r, "/account#two-step")
		return
	}
	var f ui.Form
	again := func(status int, message string) {
		f.Fail("code", message)
		s.render(w, r, status, pages.TwoStepConfirm(s.shell(w, r, "Two-step sign-in", "account"), f))
	}
	// Somebody at a session left open does not know the key, and must not
	// be able to guess a code until one turns the second step on with it.
	if ok, wait := s.codeAllowed(sess.UserID); !ok {
		again(http.StatusTooManyRequests, "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		return
	}
	key, err := s.Box.OpenString(sess.User.TOTPPending)
	if err != nil {
		s.fail(w, r, errTOTPKey)
		return
	}
	step, ok := auth.TOTPCheck(key, r.PostFormValue("code"), time.Now(), 0)
	if !ok {
		again(http.StatusUnprocessableEntity, wrongCode)
		return
	}
	codes, hashes := newRecoveryCodes()
	err = s.DB.EnableTOTP(r.Context(), sess.UserID, sess.User.TOTPPending, step, sess.TokenHash, hashes)
	if errors.Is(err, db.ErrNotFound) {
		// The key was replaced by another start while this form was open.
		again(http.StatusConflict, "This set-up was started again somewhere else. Start over.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.logins.Reset("code:" + sess.UserID)
	s.Log.Info("second step turned on", "user", sess.UserID)
	s.render(w, r, http.StatusOK, pages.RecoveryCodes(s.shell(w, r, "Recovery codes", "account"), codes, true))
}

// twoStepCodes replaces the recovery codes. It asks for a code from the
// app: recovery codes are a way in, and a session alone must not mint them.
func (s *Server) twoStepCodes(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if !sess.User.TwoStep() {
		redirect(w, r, "/account#two-step")
		return
	}
	var f ui.Form
	if ok, wait := s.codeAllowed(sess.UserID); !ok {
		f.Fail("codes_code", "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		s.renderAccount(w, r, http.StatusTooManyRequests, ui.Form{}, ui.Form{}, f)
		return
	}
	typed := r.PostFormValue("code")
	ok := false
	if auth.IsTOTPCode(typed) {
		var err error
		if ok, err = s.secondStep(r.Context(), sess.User, typed); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !ok {
		f.Fail("codes_code", wrongCode)
		s.renderAccount(w, r, http.StatusUnprocessableEntity, ui.Form{}, ui.Form{}, f)
		return
	}
	s.logins.Reset("code:" + sess.UserID)
	codes, hashes := newRecoveryCodes()
	if err := s.DB.ReplaceRecoveryCodes(r.Context(), sess.UserID, hashes); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.RecoveryCodes(s.shell(w, r, "Recovery codes", "account"), codes, false))
}

// twoStepOff removes the second step: the password and a code.
func (s *Server) twoStepOff(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if !sess.User.TwoStep() {
		redirect(w, r, "/account#two-step")
		return
	}
	var f ui.Form
	if ok, wait := s.codeAllowed(sess.UserID); !ok {
		f.Fail("off_code", "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		s.renderAccount(w, r, http.StatusTooManyRequests, ui.Form{}, ui.Form{}, f)
		return
	}
	if s.passwordAgain(r, &f, "off_password") {
		ok, err := s.secondStep(r.Context(), sess.User, r.PostFormValue("code"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !ok {
			f.Fail("off_code", wrongCode)
		}
	}
	if !f.OK() {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, ui.Form{}, ui.Form{}, f)
		return
	}
	s.logins.Reset("code:" + sess.UserID)
	if err := s.DB.DisableTOTP(r.Context(), sess.UserID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("second step turned off", "user", sess.UserID)
	setFlash(w, r, ui.ToneOK, "Two-step sign-in is off.")
	redirect(w, r, "/account#two-step")
}

// memberTwoStepOff turns off another member's second step, for one who
// lost their phone and their recovery codes.
func (s *Server) memberTwoStepOff(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManaged(w, r)
	if !ok {
		return
	}
	if err := s.DB.DisableTOTP(r.Context(), m.UserID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("second step turned off", "user", m.UserID, "by", sessionFrom(r).UserID)
	setFlash(w, r, ui.ToneOK, m.Name+" now signs in with a password only, until they set the second step up again.")
	redirect(w, r, "/team")
}

// awaitCode remembers, in a sealed cookie, that the password was right
// for an account whose sign-in has a second step.
func (s *Server) awaitCode(w http.ResponseWriter, r *http.Request, userID string) error {
	expires := time.Now().Add(twoStepWindow)
	sealed, err := s.Box.SealString(twoStepMark + "|" + userID + "|" + strconv.FormatInt(expires.Unix(), 10))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: twoStepCookie, Value: sealed, Path: "/login",
		MaxAge:   int(twoStepWindow.Seconds()),
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func clearTwoStepCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: twoStepCookie, Path: "/login", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
}

// awaitedUser returns the account the request's cookie says the password
// was right for, while that is still fresh.
func (s *Server) awaitedUser(r *http.Request) (db.User, bool) {
	c, err := r.Cookie(twoStepCookie)
	if err != nil || c.Value == "" {
		return db.User{}, false
	}
	plain, err := s.Box.OpenString(c.Value)
	if err != nil {
		return db.User{}, false
	}
	parts := strings.Split(plain, "|")
	if len(parts) != 3 || parts[0] != twoStepMark {
		return db.User{}, false
	}
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return db.User{}, false
	}
	user, err := s.DB.UserByID(r.Context(), parts[1])
	if err != nil || !user.TwoStep() {
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			s.Log.Error("load account for second step", "err", err)
		}
		return db.User{}, false
	}
	return user, true
}

func (s *Server) loginCodeForm(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r) != nil {
		redirect(w, r, "/")
		return
	}
	if _, ok := s.awaitedUser(r); !ok {
		redirect(w, r, "/login")
		return
	}
	s.render(w, r, http.StatusOK, pages.LoginCode(ui.Form{}, anonCSRF(w, r)))
}

func (s *Server) loginCodeSubmit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.awaitedUser(r)
	if !ok {
		clearTwoStepCookie(w, r)
		setFlash(w, r, ui.ToneWarn, "That took too long. Sign in again.")
		redirect(w, r, "/login")
		return
	}
	var f ui.Form
	if ok, wait := s.codeAllowed(user.ID); !ok {
		f.Fail("form", "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		s.render(w, r, http.StatusTooManyRequests, pages.LoginCode(f, anonCSRF(w, r)))
		return
	}
	ok, err := s.secondStep(r.Context(), user, r.PostFormValue("code"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.Log.Warn("failed second step", "ip", limiterIP(clientIP(r)))
		f.Fail("form", wrongCode)
		s.render(w, r, http.StatusUnauthorized, pages.LoginCode(f, anonCSRF(w, r)))
		return
	}
	s.logins.Reset("code:" + user.ID)
	clearTwoStepCookie(w, r)
	teamID, err := s.DB.FirstTeamOf(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, user.ID, teamID); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/")
}
