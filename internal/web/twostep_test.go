package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

var (
	totpKeyRE  = regexp.MustCompile(`data-copy="([A-Z2-7]{32})"`)
	recoveryRE = regexp.MustCompile(`<li class="fact">([a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4})</li>`)
)

// signIn posts the sign-in form in a browser of its own.
func (a *app) signIn(email, password string) (*http.Client, *http.Response) {
	a.t.Helper()
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
	_, page := a.do(c, req)
	csrf := csrfRE.FindStringSubmatch(page)[1]
	res, _ := a.postRaw(c, "/login", url.Values{"_csrf": {csrf}, "email": {email}, "password": {password}}, nil)
	return c, res
}

// sendCode posts the second step in a browser that passed the first.
func (a *app) sendCode(c *http.Client, code string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login/code", nil)
	res, page := a.do(c, req)
	if res.StatusCode != http.StatusOK {
		return res, page
	}
	return a.postRaw(c, "/login/code", url.Values{"_csrf": {csrfRE.FindStringSubmatch(page)[1]}, "code": {code}}, nil)
}

// turnOnTwoStep sets the second step up for the owner and returns the key
// and the recovery codes the pages showed.
func (a *app) turnOnTwoStep() (key string, codes []string) {
	a.t.Helper()
	res, page := a.post("/account", "/account/two-step/start", url.Values{"start_password": {testPassword}})
	wantStatus(a.t, res, http.StatusOK)
	m := totpKeyRE.FindStringSubmatch(page)
	if m == nil {
		a.t.Fatalf("no key on the set-up page:\n%s", page)
	}
	key = m[1]
	res, page = a.post("/account", "/account/two-step/confirm", url.Values{"code": {auth.TOTPNow(key, time.Now())}})
	wantStatus(a.t, res, http.StatusOK)
	for _, m := range recoveryRE.FindAllStringSubmatch(page, -1) {
		codes = append(codes, m[1])
	}
	if len(codes) != auth.RecoveryCodes {
		a.t.Fatalf("%d recovery codes on the page:\n%s", len(codes), page)
	}
	return key, codes
}

// codeIn returns a code of a step after now, for a test that has already
// used the current one: a code works once.
func codeIn(key string, steps int) string {
	return auth.TOTPNow(key, time.Now().Add(time.Duration(steps)*30*time.Second))
}

func signedIn(a *app, c *http.Client) bool {
	req, _ := http.NewRequest(http.MethodGet, a.url+"/", nil)
	res, _ := a.do(c, req)
	return res.StatusCode == http.StatusOK
}

func TestTurningTwoStepOn(t *testing.T) {
	var logs strings.Builder
	a := newAppWithLog(t, true, &logs)
	a.setup()
	ctx := context.Background()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	elsewhere, res := a.signIn(testEmail, testPassword)
	wantRedirect(t, res, "/")

	// It asks for the password: a session left open is not enough.
	res, page := a.post("/account", "/account/two-step/start", url.Values{"start_password": {"not the password"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if totpKeyRE.MatchString(page) {
		t.Fatal("a key was shown without the password")
	}
	// Confirming before starting does nothing.
	res, _ = a.post("/account", "/account/two-step/confirm", url.Values{"code": {"123456"}})
	wantRedirect(t, res, "/account#two-step")

	res, page = a.post("/account", "/account/two-step/start", url.Values{"start_password": {testPassword}})
	wantStatus(t, res, http.StatusOK)
	key := totpKeyRE.FindStringSubmatch(page)[1]
	if !strings.Contains(page, "otpauth://totp/musdash:") {
		t.Fatal("no otpauth link on the set-up page")
	}
	// The key is stored sealed, and nothing is on yet.
	u, _ := a.db.UserByID(ctx, owner.ID)
	if u.TwoStep() || u.TOTPPending == "" || strings.Contains(u.TOTPPending, key) {
		t.Fatalf("pending state: %+v", u)
	}
	if !signedIn(a, elsewhere) {
		t.Fatal("the other session ended before the second step was on")
	}

	// A wrong code does not turn it on, and the key is not shown again.
	res, page = a.post("/account", "/account/two-step/confirm", url.Values{"code": {"000000"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if strings.Contains(page, key) || strings.Contains(page, key[:4]+" "+key[4:8]) {
		t.Fatal("the key was shown again after a wrong code")
	}
	_, page = a.get("/account")
	if strings.Contains(page, key) || strings.Contains(page, key[:4]+" "+key[4:8]) {
		t.Fatal("the Account page shows the pending key")
	}

	res, page = a.post("/account", "/account/two-step/confirm", url.Values{"code": {auth.TOTPNow(key, time.Now())}})
	wantStatus(t, res, http.StatusOK)
	codes := recoveryRE.FindAllStringSubmatch(page, -1)
	if len(codes) != auth.RecoveryCodes {
		t.Fatalf("%d recovery codes", len(codes))
	}
	u, _ = a.db.UserByID(ctx, owner.ID)
	if !u.TwoStep() || u.TOTPPending != "" || strings.Contains(u.TOTPSecret, key) {
		t.Fatalf("after turning on: %+v", u)
	}
	// Recovery codes are stored as hashes.
	var stored string
	a.db.QueryRow(`SELECT group_concat(code_hash) FROM recovery_codes`).Scan(&stored)
	for _, c := range codes {
		if strings.Contains(stored, c[1]) || strings.Contains(stored, strings.ReplaceAll(c[1], "-", "")) {
			t.Fatal("a recovery code is stored as it is")
		}
	}
	// This session stays, every other ends.
	if !signedIn(a, a.client) || signedIn(a, elsewhere) {
		t.Fatal("turning on should end the other sessions and keep this one")
	}
	// Neither the key nor a code reached the log.
	for _, shown := range []string{key, codes[0][1]} {
		if strings.Contains(logs.String(), shown) {
			t.Fatalf("%q was written to the log", shown)
		}
	}
	// The Account page says so, and shows no secret.
	_, page = a.get("/account")
	if !strings.Contains(page, "10 recovery codes left") || strings.Contains(page, key) || strings.Contains(page, codes[0][1]) {
		t.Fatalf("account page after turning on:\n%s", page)
	}
	// Starting again while it is on does nothing.
	res, _ = a.post("/account", "/account/two-step/start", url.Values{"start_password": {testPassword}})
	wantRedirect(t, res, "/account#two-step")
}

func TestSigningInWithTwoSteps(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	key, codes := a.turnOnTwoStep()

	// The password alone signs nobody in.
	c, res := a.signIn(testEmail, testPassword)
	wantRedirect(t, res, "/login/code")
	if signedIn(a, c) {
		t.Fatal("signed in without the second step")
	}
	for _, cookie := range c.Jar.Cookies(mustURL(a.url + "/login/code")) {
		if cookie.Name == sessionCookie {
			t.Fatal("a session cookie was set before the code")
		}
	}

	// A wrong code, then the right one. The code that turned the second
	// step on belongs to the current step and was used, so the next is sent.
	res, _ = a.sendCode(c, "000000")
	wantStatus(t, res, http.StatusUnauthorized)
	if signedIn(a, c) {
		t.Fatal("signed in with a wrong code")
	}
	var usedStep int64
	a.db.QueryRow(`SELECT totp_step FROM users WHERE email = ?`, testEmail).Scan(&usedStep)
	res, _ = a.sendCode(c, auth.TOTPNow(key, time.Unix(usedStep*30, 0)))
	wantStatus(t, res, http.StatusUnauthorized) // used when it was turned on
	code := codeIn(key, 1)
	res, _ = a.sendCode(c, code)
	wantRedirect(t, res, "/")
	if !signedIn(a, c) {
		t.Fatal("not signed in after the right code")
	}

	// The same code does not sign a second browser in.
	c2, res := a.signIn(testEmail, testPassword)
	wantRedirect(t, res, "/login/code")
	res, _ = a.sendCode(c2, code)
	wantStatus(t, res, http.StatusUnauthorized)

	// A recovery code works once, typed as people type.
	res, _ = a.sendCode(c2, " "+strings.ToUpper(codes[0])+" ")
	wantRedirect(t, res, "/")
	c3, _ := a.signIn(testEmail, testPassword)
	res, _ = a.sendCode(c3, codes[0])
	wantStatus(t, res, http.StatusUnauthorized)
	if signedIn(a, c3) {
		t.Fatal("a recovery code worked twice")
	}

	// Without having passed the password, the code page is not there.
	fresh := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login/code", nil)
	res, _ = a.do(fresh, req)
	wantRedirect(t, res, "/login")
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// codeCookie returns the cookie that sits between the password and the code.
func codeCookie(a *app, c *http.Client) *http.Cookie {
	for _, cookie := range c.Jar.Cookies(mustURL(a.url + "/login/code")) {
		if cookie.Name == twoStepCookie {
			return cookie
		}
	}
	a.t.Fatal("no second-step cookie")
	return nil
}

func TestSecondStepCookie(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	key, _ := a.turnOnTwoStep()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	// A second member, without a second step.
	mem := a.newPerson("Member", db.RoleMember)

	c, res := a.signIn(testEmail, testPassword)
	good := codeCookie(a, c)
	if strings.Contains(good.Value, owner.ID) {
		t.Fatalf("the cookie should be sealed: %+v", good)
	}
	for _, set := range res.Cookies() {
		if set.Name == twoStepCookie && (!set.HttpOnly || set.SameSite != http.SameSiteLaxMode || set.MaxAge > 300 || set.Path != "/login") {
			t.Fatalf("the cookie as it was set: %+v", set)
		}
	}

	send := func(value string) *http.Response {
		browser := a.newClient()
		req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
		_, page := a.do(browser, req)
		csrf := csrfRE.FindStringSubmatch(page)[1]
		browser.Jar.SetCookies(mustURL(a.url+"/login/code"), []*http.Cookie{{Name: twoStepCookie, Value: value, Path: "/login"}})
		res, _ := a.postRaw(browser, "/login/code", url.Values{"_csrf": {csrf}, "code": {codeIn(key, 1)}}, nil)
		if signedIn(a, browser) != (res.StatusCode == http.StatusSeeOther && res.Header.Get("Location") == "/") {
			t.Fatal("the answer and the session disagree")
		}
		return res
	}
	seal := func(plain string) string { return a.seal(plain) }
	expires := func(d time.Duration) string { return itoa(int(time.Now().Add(d).Unix())) }

	// Tampered with, sealed with another key, run out, of another shape,
	// for an account without a second step, for nobody: none signs in.
	for name, value := range map[string]string{
		"tampered":       good.Value[:len(good.Value)-2] + "AA",
		"not sealed":     twoStepMark + "|" + owner.ID + "|" + expires(time.Minute),
		"expired":        seal(twoStepMark + "|" + owner.ID + "|" + expires(-time.Second)),
		"another mark":   seal("x|" + owner.ID + "|" + expires(time.Minute)),
		"no expiry":      seal(twoStepMark + "|" + owner.ID),
		"just an id":     seal(owner.ID),
		"no second step": seal(twoStepMark + "|" + mem.user.ID + "|" + expires(time.Minute)),
		"nobody":         seal(twoStepMark + "|nosuchuser|" + expires(time.Minute)),
		"empty":          "",
	} {
		if res := send(value); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
			t.Errorf("%s: got %d → %q, want to be sent back to sign in", name, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// The real one works.
	wantRedirect(t, send(good.Value), "/")
}

func TestGuessingCodesIsLimited(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	key, _ := a.turnOnTwoStep()

	c, _ := a.signIn(testEmail, testPassword)
	for i := 0; i < 5; i++ {
		res, _ := a.sendCode(c, "00000"+itoa(i))
		wantStatus(t, res, http.StatusUnauthorized)
	}
	// The sixth try is refused even when it is right, and from another
	// browser too: the count is the account's.
	res, _ := a.sendCode(c, codeIn(key, 1))
	wantStatus(t, res, http.StatusTooManyRequests)
	other, _ := a.signIn(testEmail, testPassword)
	res, _ = a.sendCode(other, codeIn(key, 1))
	wantStatus(t, res, http.StatusTooManyRequests)
	if signedIn(a, c) || signedIn(a, other) {
		t.Fatal("signed in past the limit")
	}
}

func TestTurningTwoStepOff(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	key, codes := a.turnOnTwoStep()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	on := func() bool {
		u, _ := a.db.UserByID(ctx, owner.ID)
		return u.TwoStep()
	}

	// New recovery codes need a code from the app, not a recovery code.
	res, _ := a.post("/account", "/account/two-step/codes", url.Values{"code": {codes[1]}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res, page := a.post("/account", "/account/two-step/codes", url.Values{"code": {codeIn(key, 1)}})
	wantStatus(t, res, http.StatusOK)
	fresh := recoveryRE.FindAllStringSubmatch(page, -1)
	if len(fresh) != auth.RecoveryCodes || fresh[0][1] == codes[0] {
		t.Fatalf("new codes: %v", fresh)
	}
	c, _ := a.signIn(testEmail, testPassword)
	res, _ = a.sendCode(c, codes[1])
	wantStatus(t, res, http.StatusUnauthorized) // an old code

	// Turning off needs the password and a code.
	res, _ = a.post("/account", "/account/two-step/off", url.Values{"off_password": {"wrong password"}, "code": {fresh[0][1]}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res, _ = a.post("/account", "/account/two-step/off", url.Values{"off_password": {testPassword}, "code": {"000000"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !on() {
		t.Fatal("turned off without both")
	}
	// A wrong password must not use the code up.
	res, _ = a.post("/account", "/account/two-step/off", url.Values{"off_password": {testPassword}, "code": {fresh[0][1]}})
	wantRedirect(t, res, "/account#two-step")
	if on() {
		t.Fatal("still on")
	}
	if n, _ := a.db.CountRecoveryCodes(ctx, owner.ID); n != 0 {
		t.Fatalf("%d recovery codes left after turning off", n)
	}
	// Signing in is one step again.
	_, res = a.signIn(testEmail, testPassword)
	wantRedirect(t, res, "/")
}

func TestTurningAMembersTwoStepOff(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	ad := a.newPerson("Admin", db.RoleAdmin)
	mem := a.newPerson("Member", db.RoleMember)
	a.turnOnTwoStep()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	// Give the Member a second step directly: only its presence matters.
	a.db.Exec(`UPDATE users SET totp_secret = ? WHERE id = ?`, a.seal(auth.NewTOTPKey()), mem.user.ID)
	on := func(id string) bool {
		u, _ := a.db.UserByID(ctx, id)
		return u.TwoStep()
	}

	// An Admin may for a Member, not for the Owner; a Member for nobody.
	res, _ := ad.post("/team/members/"+owner.ID+"/two-step-off", nil)
	wantStatus(t, res, http.StatusForbidden)
	res, _ = mem.post("/team/members/"+owner.ID+"/two-step-off", nil)
	wantStatus(t, res, http.StatusForbidden)
	if !on(owner.ID) {
		t.Fatal("the Owner's second step was turned off by somebody below them")
	}
	_, page := ad.get("/team")
	if !strings.Contains(page, "/team/members/"+mem.user.ID+"/two-step-off") || strings.Contains(page, "/team/members/"+owner.ID+"/two-step-off") {
		t.Fatal("the Team page should offer it to an Admin for the Member only")
	}
	res, _ = ad.post("/team/members/"+mem.user.ID+"/two-step-off", nil)
	wantRedirect(t, res, "/team")
	if on(mem.user.ID) {
		t.Fatal("the Member's second step is still on")
	}
}

// A password reset does not go around the second step.
func TestResetLinkKeepsTheSecondStep(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	key, _ := a.turnOnTwoStep()
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	token := secret.RandomToken(32)
	if err := a.db.CreatePasswordReset(ctx, secret.HashToken(token), owner.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/reset/"+token, nil)
	_, page := a.do(c, req)
	res, _ := a.postRaw(c, "/reset/"+token, url.Values{"_csrf": {csrfRE.FindStringSubmatch(page)[1]}, "password": {"a brand new password"}}, nil)
	wantRedirect(t, res, "/login")
	if signedIn(a, c) {
		t.Fatal("the reset link signed somebody in")
	}
	c, res = a.signIn(testEmail, "a brand new password")
	wantRedirect(t, res, "/login/code")
	if signedIn(a, c) {
		t.Fatal("signed in with the new password alone")
	}
	res, _ = a.sendCode(c, codeIn(key, 1))
	wantRedirect(t, res, "/")
}

// The count of wrong codes is the account's and cannot be emptied from
// outside: the limiter that counts by address is another one.
func TestCodeLimitSurvivesAFloodOfOtherAddresses(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	key, _ := a.turnOnTwoStep()

	c, _ := a.signIn(testEmail, testPassword)
	for i := 0; i < 5; i++ {
		res, _ := a.sendCode(c, "00000"+itoa(i))
		wantStatus(t, res, http.StatusUnauthorized)
	}
	// More addresses than the by-address table holds, each asking for an
	// invitation that does not exist. (The test server is on loopback, so
	// X-Forwarded-For is believed, as it is behind the musdash proxy.)
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < 4200; i++ {
		req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/x", nil)
		req.Header.Set("X-Forwarded-For", "10."+itoa(i>>16&255)+"."+itoa(i>>8&255)+"."+itoa(i&255))
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	res, _ := a.sendCode(c, codeIn(key, 1))
	wantStatus(t, res, http.StatusTooManyRequests)
	if signedIn(a, c) {
		t.Fatal("the account's limit on codes was reset from outside")
	}
}
