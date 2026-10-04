package web

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

const (
	sessionCookie = "musdash_session"
	csrfCookie    = "musdash_csrf"
	flashCookie   = "musdash_flash"

	sessionLifetime = 30 * 24 * time.Hour
	// The expiry slides forward at most once a day, so reading pages does
	// not turn into a database write per request.
	sessionRefresh = 24 * time.Hour

	maxFormBytes    = 1 << 20
	formReadTimeout = 30 * time.Second
)

type ctxKey int

const sessionKey ctxKey = iota

// sessionFrom returns the request's session, or nil when signed out.
func sessionFrom(r *http.Request) *db.Session {
	s, _ := r.Context().Value(sessionKey).(*db.Session)
	return s
}

// recoverer turns a handler panic into a logged 500.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.Log.Error("panic", "route", logRoute(r), "panic", rec, "stack", string(debug.Stack()))
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// secureHeaders sets the browser-side protections. The policy allows only
// same-origin scripts and styles: pages carry no inline script or style.
const contentPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// loadSession attaches the session named by the cookie, if it is live.
func (s *Server) loadSession(r *http.Request) *http.Request {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return r
	}
	sess, err := s.DB.SessionByHash(r.Context(), secret.HashToken(c.Value))
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.Log.Error("load session", "err", err)
		}
		return r
	}
	if time.Until(time.Unix(sess.ExpiresAt, 0)) < sessionLifetime-sessionRefresh {
		if err := s.DB.TouchSession(r.Context(), sess.TokenHash, time.Now().Add(sessionLifetime).Unix()); err != nil {
			s.Log.Error("touch session", "err", err)
		}
	}
	return r.WithContext(context.WithValue(r.Context(), sessionKey, &sess))
}

// authed wraps a handler that needs a signed-in person. A request without a
// live session is sent to sign in before anything else is checked, so an
// expired tab redirects instead of failing its CSRF check.
func (s *Server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.loadSession(r)
		sess := sessionFrom(r)
		if sess == nil {
			n, err := s.DB.CountUsers(r.Context())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if n == 0 {
				redirect(w, r, "/setup")
				return
			}
			redirect(w, r, "/login")
			return
		}
		if !s.checkCSRF(w, r, sess.CSRFToken) {
			return
		}
		h(w, r)
	})
}

// anon wraps a handler that works signed out. Its forms are protected by a
// token held in a cookie, since there is no session to hold it yet. A browser
// that is signed in can still reach these pages (a reset link opened while
// signed in, a stale sign-in tab), so the session's token is accepted too.
func (s *Server) anon(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.loadSession(r)
		var tokens []string
		if sess := sessionFrom(r); sess != nil {
			tokens = append(tokens, sess.CSRFToken)
		}
		if c, err := r.Cookie(csrfCookie); err == nil {
			tokens = append(tokens, c.Value)
		}
		if !s.checkCSRF(w, r, tokens...) {
			return
		}
		h(w, r)
	})
}

// anonCSRF returns the signed-out CSRF token, issuing the cookie if needed.
func anonCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	token := secret.RandomToken(24)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return token
}

// checkCSRF verifies the token on state-changing requests against the tokens
// this browser was given. It writes the response and returns false when the
// request must not proceed.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, want ...string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	// Bound both the size of a form and how long a client may take to send
	// it, so slow or oversized uploads cannot hold memory. The deadline is
	// per request: streaming GET responses are not affected.
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(formReadTimeout))

	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.PostFormValue("_csrf")
	}
	ok := false
	for _, token := range want {
		// Every candidate is compared, without stopping at the first match.
		if len(token) >= 32 && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			ok = true
		}
	}
	if !ok {
		s.render(w, r, http.StatusForbidden, pages.Message("This form has expired", "Go back, reload the page and try again."))
		return false
	}
	return true
}

// loopbackOnly rejects requests that did not come from this machine.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || r.Header.Get("X-Forwarded-For") != "" {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromLocalProxy reports whether the request reached us from this machine,
// which is how the musdash proxy forwards traffic. Only then are the
// X-Forwarded-* headers believed.
func fromLocalProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isHTTPS reports whether the browser is talking HTTPS. The session cookie
// is marked Secure only then: a fresh install is first opened at
// http://<ip>:8000, where a Secure cookie would never be sent back.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return fromLocalProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https"
}

// clientIP returns the address to attribute a request to.
func clientIP(r *http.Request) string {
	if fromLocalProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiterIP reduces an address to what a rate limit should count. IPv4 is
// used whole. IPv6 is cut to its /64: one machine usually owns a whole /64
// and could otherwise use a fresh address for every guess.
func limiterIP(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return "unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// logRoute names a request for the log without its variable parts: the
// matched route pattern, such as "GET /reset/{token}". Paths can carry
// secrets (a password-reset token is one), so they are never logged.
func logRoute(r *http.Request) string {
	if r.Pattern != "" && r.Pattern != "/" {
		return r.Pattern
	}
	return r.Method + " (unmatched)"
}

// setFlash stores a one-time message for the next page.
func setFlash(w http.ResponseWriter, r *http.Request, tone, message string) {
	// A cookie holds about 4 KB; an error that quotes a command's output can
	// be longer, and an oversized cookie is dropped whole by the browser.
	const maxFlash = 600
	if len(message) > maxFlash {
		message = strings.ToValidUTF8(message[:maxFlash], "") + "…"
	}
	http.SetCookie(w, &http.Cookie{
		Name:  flashCookie,
		Value: base64.RawURLEncoding.EncodeToString([]byte(tone + "|" + message)),
		Path:  "/", MaxAge: 60, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash reads and clears the one-time message.
func takeFlash(w http.ResponseWriter, r *http.Request) *ui.Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	tone, message, ok := strings.Cut(string(raw), "|")
	if !ok || message == "" {
		return nil
	}
	switch tone {
	case ui.ToneNeutral, ui.ToneOK, ui.ToneWarn, ui.ToneDanger:
	default:
		tone = ui.ToneNeutral
	}
	return &ui.Flash{Tone: tone, Message: message}
}

// statusWriter remembers the response status for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestLog logs each request. It is on only in development mode; in
// production the access log stays off to keep the process quiet.
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.Log.Debug("request", "route", logRoute(r), "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}
