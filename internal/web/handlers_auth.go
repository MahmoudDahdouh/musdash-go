package web

import (
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// One wording for every failed sign-in, so the page does not reveal whether
// an email address has an account.
const loginFailed = "That email and password do not match an account."

// normalEmail trims, lower-cases and validates an email address.
func normalEmail(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) > 254 {
		// Never carry an oversized value back into a page or a limiter key.
		return v[:254], false
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || !strings.Contains(v[strings.LastIndex(v, "@")+1:], ".") {
		return v, false
	}
	return v, true
}

// checkPassword compares a password with a stored hash, running at most a
// couple of comparisons at a time. It reports false when the request is
// cancelled while waiting its turn.
func (s *Server) checkPassword(r *http.Request, hash, password string) bool {
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	case <-r.Context().Done():
		return false
	}
	return auth.CheckPassword(hash, password)
}

// publicIP guesses this machine's public address: the source address the
// system would use to reach the internet. No packet is sent. It returns ""
// when that address is private or loopback (a home network, or a cloud
// server behind NAT); the Servers page lets a person set it instead.
func publicIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsPrivate() || addr.IP.IsLoopback() || addr.IP.IsLinkLocalUnicast() || addr.IP.IsUnspecified() {
		return ""
	}
	return addr.IP.String()
}

// startSession creates a session and sets its cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID, teamID string) error {
	token := secret.RandomToken(32)
	sess := db.Session{
		TokenHash: secret.HashToken(token),
		UserID:    userID,
		TeamID:    teamID,
		CSRFToken: secret.RandomToken(24),
		ExpiresAt: time.Now().Add(sessionLifetime).Unix(),
	}
	if err := s.DB.CreateSession(r.Context(), sess); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
}

// setupOpen reports whether first-run setup is still available.
func (s *Server) setupOpen(r *http.Request) (bool, error) {
	n, err := s.DB.CountUsers(r.Context())
	return n == 0, err
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	open, err := s.setupOpen(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !open {
		redirect(w, r, "/login")
		return
	}
	s.render(w, r, http.StatusOK, pages.Setup(ui.Form{}, anonCSRF(w, r)))
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("name"))
	email, emailOK := normalEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	f.Set("name", name)
	f.Set("email", email)

	if name == "" || len(name) > 80 {
		f.Fail("name", "Enter your name, up to 80 characters.")
	}
	if !emailOK {
		f.Fail("email", "Enter an email address like you@example.com.")
	}
	if err := auth.ValidatePassword(password); err != nil {
		f.Fail("password", err.Error())
	}
	if !f.OK() {
		s.render(w, r, http.StatusUnprocessableEntity, pages.Setup(f, anonCSRF(w, r)))
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	user, teamID, err := s.DB.CreateFirstUser(r.Context(), email, name, hash)
	if errors.Is(err, db.ErrSetupClosed) {
		redirect(w, r, "/login")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.DB.EnsureLocalServer(r.Context(), teamID, publicIP()); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, user.ID, teamID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("owner account created", "email", email)
	redirect(w, r, "/")
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r) != nil {
		redirect(w, r, "/")
		return
	}
	open, err := s.setupOpen(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if open {
		redirect(w, r, "/setup")
		return
	}
	s.render(w, r, http.StatusOK, pages.Login(ui.Form{}, anonCSRF(w, r), takeFlash(w, r)))
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	email, _ := normalEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	f.Set("email", email)

	// Limit by address and by address+email: the first slows one machine
	// guessing many accounts, the second slows guessing one account. The
	// attempt is counted before the password is checked, so parallel
	// requests cannot outrun the limit.
	ip := limiterIP(clientIP(r))
	keys := []string{"ip:" + ip, "login:" + ip + "|" + secret.HashToken(email)[:16]}
	if ok, wait := s.logins.Take(keys...); !ok {
		f.Fail("form", "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		w.Header().Set("Retry-After", itoa(int(wait.Seconds())+1))
		s.render(w, r, http.StatusTooManyRequests, pages.Login(f, anonCSRF(w, r), nil))
		return
	}

	user, err := s.DB.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	// user.PasswordHash is "" for an unknown email; the check still does the
	// full work so both cases take the same time.
	if !s.checkPassword(r, user.PasswordHash, password) {
		s.Log.Warn("failed sign-in", "ip", ip)
		f.Fail("form", loginFailed)
		s.render(w, r, http.StatusUnauthorized, pages.Login(f, anonCSRF(w, r), nil))
		return
	}
	s.logins.Reset(keys...)

	// With a second step, the password is half of signing in. No session
	// exists until the code is right.
	if user.TwoStep() {
		if err := s.awaitCode(w, r, user.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		redirect(w, r, "/login/code")
		return
	}

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

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteSession(r.Context(), sessionFrom(r).TokenHash); err != nil {
		s.fail(w, r, err)
		return
	}
	clearSessionCookie(w, r)
	redirect(w, r, "/login")
}

func (s *Server) resetForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if _, err := s.DB.PasswordResetUser(r.Context(), secret.HashToken(token)); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.render(w, r, http.StatusNotFound, pages.ResetInvalid())
			return
		}
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.Reset(ui.Form{}, anonCSRF(w, r), token))
}

func (s *Server) resetSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	userID, err := s.DB.PasswordResetUser(r.Context(), secret.HashToken(token))
	if errors.Is(err, db.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, pages.ResetInvalid())
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var f ui.Form
	password := r.PostFormValue("password")
	if err := auth.ValidatePassword(password); err != nil {
		f.Fail("password", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, pages.Reset(f, anonCSRF(w, r), token))
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Ends every session and consumes the reset token.
	if err := s.DB.SetPassword(r.Context(), userID, hash, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	clearSessionCookie(w, r)
	setFlash(w, r, ui.ToneOK, "Password saved. Sign in with the new password.")
	redirect(w, r, "/login")
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, ui.Form{}, ui.Form{}, ui.Form{})
}

func (s *Server) accountProfile(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var f ui.Form
	name := strings.TrimSpace(r.PostFormValue("name"))
	email, emailOK := normalEmail(r.PostFormValue("email"))
	f.Set("name", name)
	f.Set("email", email)
	if name == "" || len(name) > 80 {
		f.Fail("name", "Enter your name, up to 80 characters.")
	}
	if !emailOK {
		f.Fail("email", "Enter an email address like you@example.com.")
	}
	if f.OK() {
		err := s.DB.UpdateUserProfile(r.Context(), sess.UserID, name, email)
		switch {
		case db.IsUnique(err):
			f.Fail("email", "Another account already uses this email.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, f, ui.Form{}, ui.Form{})
		return
	}
	setFlash(w, r, ui.ToneOK, "Profile saved.")
	redirect(w, r, "/account")
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var f ui.Form
	password := r.PostFormValue("password")
	// A stolen or unattended session must not be able to guess the current
	// password without limit.
	key := "password:" + sess.UserID
	if ok, wait := s.accounts.Take(key); !ok {
		f.Fail("current", "Too many attempts. Try again in "+itoa(int(wait.Minutes())+1)+" minutes.")
		s.renderAccount(w, r, http.StatusTooManyRequests, ui.Form{}, f, ui.Form{})
		return
	}
	if s.checkPassword(r, sess.User.PasswordHash, r.PostFormValue("current")) {
		s.accounts.Reset(key)
	} else {
		f.Fail("current", "That is not your current password.")
	}
	if err := auth.ValidatePassword(password); err != nil {
		f.Fail("password", err.Error())
	}
	if !f.OK() {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, ui.Form{}, f, ui.Form{})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetPassword(r.Context(), sess.UserID, hash, sess.TokenHash); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Password changed. Other devices were signed out, and your API tokens were revoked.")
	redirect(w, r, "/account")
}
