package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/MahmoudDahdouh/musdash-go/internal/auth"
	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/ops"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

func TestMain(m *testing.M) {
	auth.Cost = bcrypt.MinCost
	os.Exit(m.Run())
}

const (
	testEmail    = "owner@example.com"
	testPassword = "correct horse battery"
)

// app is a running server with one browser-like client.
type app struct {
	t      *testing.T
	server *Server
	db     *db.DB
	cfg    *config.Config
	fake   *runnertest.Fake
	url    string
	client *http.Client
}

func newApp(t *testing.T, dev bool) *app {
	return newAppWithLog(t, dev, io.Discard)
}

func newAppWithLog(t *testing.T, dev bool, logTo io.Writer) *app {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(secret.RandomBytes(secret.KeySize))
	log := slog.New(slog.NewTextHandler(logTo, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{DataDir: dir, Dev: dev}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// The server the tests deploy to is scripted: every docker command
	// succeeds and containers report as running.
	fake := &runnertest.Fake{Handle: func(line string, _ runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker inspect"):
			return `{"Status":"running","Running":true,"ExitCode":0}`, nil
		case strings.HasPrefix(line, "docker version"):
			return "29.8.0\n", nil
		case strings.HasPrefix(line, "docker logs"):
			return "listening on :80\n<script>alert(1)</script>\n", nil
		}
		return "", nil
	}}
	pool := servers.NewWith(fake)
	queue := jobs.New(d.DB, log, 2)
	deployer := deploy.New(d, box, queue, pool, cfg, log, "127.0.0.1:8000")
	deployer.Probe = okProbe{}
	deployer.Register()
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		queue.Stop(ctx)
	})
	operations := ops.New(d, box, queue, pool, cfg, log)
	operations.Sender = notify.Sender{Dialer: notify.Dialer{AllowLoopback: true}}
	operations.S3Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.7")}, nil
	}
	operations.Register()
	s := &Server{Cfg: cfg, DB: d, Box: box, Queue: queue, Deploy: deployer, Ops: operations, Pool: pool, Log: log}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	a := &app{t: t, server: s, db: d, cfg: cfg, fake: fake, url: srv.URL}
	a.client = a.newClient()
	return a
}

// newClient returns a client with its own cookie jar that does not follow
// redirects, so tests can assert on them.
func (a *app) newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *app) do(c *http.Client, req *http.Request) (*http.Response, string) {
	a.t.Helper()
	res, err := c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func (a *app) get(path string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.url+path, nil)
	return a.do(a.client, req)
}

var csrfRE = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// csrf returns the token embedded in the form on the given page.
func (a *app) csrf(path string) string {
	a.t.Helper()
	_, body := a.get(path)
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		a.t.Fatalf("no CSRF field on %s", path)
	}
	return m[1]
}

// post submits a form with the CSRF token taken from tokenPage.
func (a *app) post(tokenPage, path string, form url.Values) (*http.Response, string) {
	a.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", a.csrf(tokenPage))
	return a.postRaw(a.client, path, form, nil)
}

func (a *app) postRaw(c *http.Client, path string, form url.Values, header http.Header) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.url+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header[k] = v
	}
	return a.do(c, req)
}

// setup creates the owner account and leaves the client signed in.
func (a *app) setup() {
	a.t.Helper()
	res, body := a.post("/setup", "/setup", url.Values{"name": {"Owner"}, "email": {testEmail}, "password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		a.t.Fatalf("setup: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), body)
	}
}

// seal encrypts a value the way the server stores secrets.
func (a *app) seal(plain string) string {
	a.t.Helper()
	sealed, err := a.server.Box.SealString(plain)
	if err != nil {
		a.t.Fatal(err)
	}
	return sealed
}

// okProbe passes every health check.
type okProbe struct{}

func (okProbe) HTTP(context.Context, int, string) error { return nil }
func (okProbe) TCP(context.Context, int) error          { return nil }

func wantRedirect(t *testing.T, res *http.Response, to string) {
	t.Helper()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != to {
		t.Fatalf("got %d → %q, want 303 → %q", res.StatusCode, res.Header.Get("Location"), to)
	}
}

func wantStatus(t *testing.T, res *http.Response, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("status %d, want %d", res.StatusCode, status)
	}
}

func TestFreshInstallGoesToSetupThenCloses(t *testing.T) {
	a := newApp(t, false)
	res, _ := a.get("/")
	wantRedirect(t, res, "/setup")
	res, _ = a.get("/login")
	wantRedirect(t, res, "/setup")

	a.setup()
	res, body := a.get("/")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No projects yet") {
		t.Fatal("dashboard did not render the empty state")
	}

	// A second browser cannot run setup again.
	other := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/setup", nil)
	res, _ = a.do(other, req)
	wantRedirect(t, res, "/login")

	req, _ = http.NewRequest(http.MethodGet, a.url+"/login", nil)
	_, page := a.do(other, req)
	token := csrfRE.FindStringSubmatch(page)[1]
	res, _ = a.postRaw(other, "/setup", url.Values{"_csrf": {token}, "name": {"Evil"}, "email": {"evil@example.com"}, "password": {"another long password"}}, nil)
	wantRedirect(t, res, "/login")
	if n, _ := a.db.CountUsers(context.Background()); n != 1 {
		t.Fatalf("%d users after a second setup attempt", n)
	}
}

func TestSetupValidation(t *testing.T) {
	a := newApp(t, false)
	res, body := a.post("/setup", "/setup", url.Values{"name": {""}, "email": {"not-an-email"}, "password": {"short"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	for _, want := range []string{"Enter your name", "Enter an email address", "Use at least 10 characters", `value="not-an-email"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(body, `value="short"`) {
		t.Error("the password was echoed back into the page")
	}
}

func TestLoginDoesNotRevealAccounts(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	c := a.newClient()
	login := func(email, password string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
		_, page := a.do(c, req)
		token := csrfRE.FindStringSubmatch(page)[1]
		return a.postRaw(c, "/login", url.Values{"_csrf": {token}, "email": {email}, "password": {password}}, nil)
	}

	resWrong, bodyWrong := login(testEmail, "wrong password here")
	resUnknown, bodyUnknown := login("nobody@example.com", "wrong password here")
	wantStatus(t, resWrong, http.StatusUnauthorized)
	wantStatus(t, resUnknown, http.StatusUnauthorized)
	if !strings.Contains(bodyWrong, loginFailed) || !strings.Contains(bodyUnknown, loginFailed) {
		t.Fatal("both failures must show the same message")
	}

	res, _ := login("  OWNER@example.com ", testPassword)
	wantRedirect(t, res, "/")
	var session *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie {
			session = ck
		}
	}
	if session == nil {
		t.Fatal("no session cookie")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v", session.HttpOnly, session.SameSite)
	}
	// Plain HTTP on a fresh install: a Secure cookie would never come back.
	if session.Secure {
		t.Error("cookie marked Secure on a plain HTTP request")
	}
}

func TestCookieIsSecureBehindHTTPSProxy(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
	_, page := a.do(c, req)
	token := csrfRE.FindStringSubmatch(page)[1]
	// httptest clients connect from loopback, like the musdash proxy does.
	res, _ := a.postRaw(c, "/login", url.Values{"_csrf": {token}, "email": {testEmail}, "password": {testPassword}},
		http.Header{"X-Forwarded-Proto": {"https"}})
	wantRedirect(t, res, "/")
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie && !ck.Secure {
			t.Fatal("session cookie must be Secure when the browser used HTTPS")
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
	_, page := a.do(c, req)
	token := csrfRE.FindStringSubmatch(page)[1]
	attempt := func(password string) *http.Response {
		res, _ := a.postRaw(c, "/login", url.Values{"_csrf": {token}, "email": {testEmail}, "password": {password}}, nil)
		return res
	}
	for range 5 {
		wantStatus(t, attempt("wrong password here"), http.StatusUnauthorized)
	}
	// Even the right password is refused while blocked.
	res := attempt(testPassword)
	wantStatus(t, res, http.StatusTooManyRequests)
	if res.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
}

func TestLoginRateLimitHoldsUnderParallelRequests(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/login", nil)
	_, page := a.do(c, req)
	token := csrfRE.FindStringSubmatch(page)[1]

	var checked atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := a.postRaw(c, "/login", url.Values{"_csrf": {token}, "email": {testEmail}, "password": {"wrong password here"}}, nil)
			if res.StatusCode == http.StatusUnauthorized {
				checked.Add(1)
			}
		}()
	}
	wg.Wait()
	if checked.Load() != 5 {
		t.Fatalf("%d of 40 parallel guesses reached the password check, want 5", checked.Load())
	}
}

func TestSignedInBrowserCanUseSignedOutForms(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	u, _ := a.db.UserByEmail(ctx, testEmail)
	token := secret.RandomToken(32)
	a.db.CreatePasswordReset(ctx, secret.HashToken(token), u.ID, time.Now().Add(time.Hour).Unix())

	// The reset link is opened in the browser that is still signed in.
	res, _ := a.post("/reset/"+token, "/reset/"+token, url.Values{"password": {"a brand new password"}})
	wantRedirect(t, res, "/login")
}

func TestLimiterIPAndLogRoute(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":          "203.0.113.9",
		"2001:db8:1:2:aaaa::1": "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::2": "2001:db8:1:2::/64",
		"::ffff:203.0.113.9":   "203.0.113.9",
		"not an address":       "unknown",
	} {
		if got := limiterIP(in); got != want {
			t.Errorf("limiterIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResetTokenIsNotLogged(t *testing.T) {
	var logs strings.Builder
	a := newAppWithLog(t, true, &logs)
	a.setup()
	res, _ := a.get("/reset/super-secret-reset-token")
	wantStatus(t, res, http.StatusNotFound)
	if strings.Contains(logs.String(), "super-secret-reset-token") {
		t.Fatalf("the reset token was written to the log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "GET /reset/{token}") {
		t.Fatalf("the request was not logged by its route:\n%s", logs.String())
	}
}

func TestCSRF(t *testing.T) {
	a := newApp(t, false)
	a.setup()

	res, _ := a.postRaw(a.client, "/projects", url.Values{"name": {"No token"}}, nil)
	wantStatus(t, res, http.StatusForbidden)
	res, _ = a.postRaw(a.client, "/projects", url.Values{"name": {"Bad token"}, "_csrf": {"wrong"}}, nil)
	wantStatus(t, res, http.StatusForbidden)

	// htmx sends the token as a header.
	token := a.csrf("/projects/new")
	res, _ = a.postRaw(a.client, "/projects", url.Values{"name": {"Header token"}}, http.Header{"X-Csrf-Token": {token}})
	wantStatus(t, res, http.StatusSeeOther)

	projects, _ := a.db.ListProjects(context.Background(), firstTeam(t, a))
	if len(projects) != 1 || projects[0].Name != "Header token" {
		t.Fatalf("projects: %+v", projects)
	}
}

func firstTeam(t *testing.T, a *app) string {
	t.Helper()
	u, err := a.db.UserByEmail(context.Background(), testEmail)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.db.FirstTeamOf(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSignedOutRequestsRedirect(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	token := a.csrf("/projects/new")

	// Another tab signs out.
	res, _ := a.postRaw(a.client, "/logout", url.Values{"_csrf": {token}}, nil)
	wantRedirect(t, res, "/login")

	// This tab's next form post goes to sign in rather than failing.
	res, _ = a.postRaw(a.client, "/projects", url.Values{"_csrf": {token}, "name": {"Stale"}}, nil)
	wantRedirect(t, res, "/login")

	// An htmx request navigates the whole page.
	res, _ = a.postRaw(a.client, "/projects", url.Values{"name": {"Stale"}}, http.Header{"Hx-Request": {"true"}, "X-Csrf-Token": {token}})
	if res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("htmx: %d HX-Redirect=%q", res.StatusCode, res.Header.Get("HX-Redirect"))
	}

	res, _ = a.get("/")
	wantRedirect(t, res, "/login")
	res, _ = a.get("/account")
	wantRedirect(t, res, "/login")
}

func TestProjectsAndEnvironments(t *testing.T) {
	a := newApp(t, false)
	a.setup()

	res, body := a.post("/projects/new", "/projects", url.Values{"name": {"  "}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Enter a name") {
		t.Fatal("no error for an empty name")
	}

	res, _ = a.post("/projects/new", "/projects", url.Values{"name": {"Shop <script>"}, "description": {"Storefront"}})
	wantStatus(t, res, http.StatusSeeOther)
	path := res.Header.Get("Location")

	res, body = a.get(path)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Shop &lt;script&gt;") || strings.Contains(body, "Shop <script>") {
		t.Fatal("project name is not escaped")
	}
	if !strings.Contains(body, "Nothing is deployed in production") || !strings.Contains(body, "Project created.") {
		t.Fatal("project page is missing the empty state or the confirmation")
	}

	settings := path + "/settings"
	res, _ = a.post(settings, path, url.Values{"name": {"Shop"}, "description": {""}})
	wantRedirect(t, res, settings)

	// Environments.
	res, body = a.post(settings, path+"/environments", url.Values{"name": {"Not Valid!"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, envNameRule) {
		t.Fatal("no error for a bad environment name")
	}
	res, _ = a.post(settings, path+"/environments", url.Values{"name": {"Staging"}})
	wantRedirect(t, res, settings)
	res, body = a.post(settings, path+"/environments", url.Values{"name": {"staging"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "already has an environment called staging") {
		t.Fatal("no error for a duplicate environment")
	}

	ctx := context.Background()
	projectID := strings.TrimPrefix(path, "/projects/")
	envs, _ := a.db.ListEnvironments(ctx, projectID)
	if len(envs) != 2 {
		t.Fatalf("%d environments, want 2", len(envs))
	}
	// Creation order is kept even for rows made within the same second.
	if envs[0].Name != "production" || envs[1].Name != "staging" {
		t.Fatalf("environment order: %s, %s", envs[0].Name, envs[1].Name)
	}
	staging := envs[1]

	res, _ = a.get(path + "?env=" + staging.ID)
	wantStatus(t, res, http.StatusOK)
	res, _ = a.get(path + "?env=nope")
	wantStatus(t, res, http.StatusNotFound)

	// Deleting needs the typed name.
	res, _ = a.post(settings, "/environments/"+staging.ID+"/delete", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, settings)
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 2 {
		t.Fatal("environment deleted without confirmation")
	}
	res, _ = a.post(settings, "/environments/"+staging.ID+"/delete", url.Values{"confirm": {"staging"}})
	wantRedirect(t, res, settings)
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 1 {
		t.Fatal("environment not deleted")
	}
	// The last environment stays.
	res, _ = a.post(settings, "/environments/"+envs[0].ID+"/delete", url.Values{"confirm": {"production"}})
	wantRedirect(t, res, settings)
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 1 {
		t.Fatal("the last environment was deleted")
	}

	res, _ = a.post(settings, path+"/delete", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, settings)
	res, _ = a.post(settings, path+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, "/")
	res, _ = a.get(path)
	wantStatus(t, res, http.StatusNotFound)
}

func TestOtherTeamsProjectIsNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	p, err := a.db.CreateProject(ctx, "otherteam", "Secret project", "")
	if err != nil {
		t.Fatal(err)
	}
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	token := a.csrf("/projects/new")

	for _, path := range []string{"/projects/" + p.ID, "/projects/" + p.ID + "/settings"} {
		res, body := a.get(path)
		wantStatus(t, res, http.StatusNotFound)
		if strings.Contains(body, "Secret project") {
			t.Fatalf("%s leaked another team's project", path)
		}
	}
	for path, form := range map[string]url.Values{
		"/projects/" + p.ID:                       {"name": {"Hijacked"}},
		"/projects/" + p.ID + "/delete":           {"confirm": {"Secret project"}},
		"/projects/" + p.ID + "/environments":     {"name": {"staging"}},
		"/environments/" + envs[0].ID + "/delete": {"confirm": {"production"}},
	} {
		form.Set("_csrf", token)
		res, _ := a.postRaw(a.client, path, form, nil)
		wantStatus(t, res, http.StatusNotFound)
	}
	got, err := a.db.Project(ctx, "otherteam", p.ID)
	if err != nil || got.Name != "Secret project" {
		t.Fatalf("the other team's project was changed: %+v %v", got, err)
	}
}

func TestPasswordResetLink(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	u, _ := a.db.UserByEmail(ctx, testEmail)
	token := secret.RandomToken(32)
	if err := a.db.CreatePasswordReset(ctx, secret.HashToken(token), u.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/reset/"+token, nil)
	res, page := a.do(c, req)
	wantStatus(t, res, http.StatusOK)
	csrf := csrfRE.FindStringSubmatch(page)[1]

	res, _ = a.postRaw(c, "/reset/"+token, url.Values{"_csrf": {csrf}, "password": {"short"}}, nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res, _ = a.postRaw(c, "/reset/"+token, url.Values{"_csrf": {csrf}, "password": {"a brand new password"}}, nil)
	wantRedirect(t, res, "/login")

	// The link works once, and the old session is gone.
	req, _ = http.NewRequest(http.MethodGet, a.url+"/reset/"+token, nil)
	res, _ = a.do(c, req)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.get("/")
	wantRedirect(t, res, "/login")

	updated, _ := a.db.UserByEmail(ctx, testEmail)
	if !auth.CheckPassword(updated.PasswordHash, "a brand new password") {
		t.Fatal("password was not changed")
	}

	req, _ = http.NewRequest(http.MethodGet, a.url+"/reset/unknown-token", nil)
	res, _ = a.do(c, req)
	wantStatus(t, res, http.StatusNotFound)
}

func TestAccount(t *testing.T) {
	a := newApp(t, false)
	a.setup()

	res, body := a.post("/account", "/account/password", url.Values{"current": {"not my password"}, "password": {"a brand new password"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "That is not your current password.") {
		t.Fatal("no error for a wrong current password")
	}

	res, _ = a.post("/account", "/account/password", url.Values{"current": {testPassword}, "password": {"a brand new password"}})
	wantRedirect(t, res, "/account")
	// This browser stays signed in.
	res, _ = a.get("/account")
	wantStatus(t, res, http.StatusOK)

	res, _ = a.post("/account", "/account/profile", url.Values{"name": {"New Name"}, "email": {"new@example.com"}})
	wantRedirect(t, res, "/account")
	_, body = a.get("/account")
	if !strings.Contains(body, `value="New Name"`) || !strings.Contains(body, `value="new@example.com"`) {
		t.Fatal("profile was not saved")
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	a := newApp(t, false)
	res, body := a.get("/setup")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
		"Cache-Control":          "no-store",
	} {
		if got := res.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	// The policy forbids inline script and style, so pages must have none.
	if strings.Contains(body, "<script>") || strings.Contains(body, " style=") || strings.Contains(body, "onclick=") {
		t.Error("page contains inline script or style, which the CSP blocks")
	}

	m := regexp.MustCompile(`href="(/static/app\.css\?v=[0-9a-f]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no stylesheet link")
	}
	req, _ := http.NewRequest(http.MethodGet, a.url+m[1], nil)
	req.Header.Set("Accept-Encoding", "gzip")
	tr := &http.Transport{DisableCompression: true}
	raw, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	raw.Body.Close()
	if raw.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(raw.Header.Get("Content-Type"), "text/css") {
		t.Errorf("css: encoding %q type %q", raw.Header.Get("Content-Encoding"), raw.Header.Get("Content-Type"))
	}
	if !strings.Contains(raw.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("css Cache-Control = %q", raw.Header.Get("Cache-Control"))
	}

	res, _ = a.get("/static/nope.js")
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.get("/static/../server.go")
	if res.StatusCode == http.StatusOK {
		t.Error("path traversal served a file")
	}
	res, _ = a.get("/healthz")
	wantStatus(t, res, http.StatusOK)
}

func TestGalleryOnlyInDev(t *testing.T) {
	prod := newApp(t, false)
	prod.setup()
	res, _ := prod.get("/_ui")
	wantStatus(t, res, http.StatusNotFound)

	dev := newApp(t, true)
	dev.setup()
	res, body := dev.get("/_ui")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Brand scale") {
		t.Fatal("gallery did not render")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	a := newApp(t, false)
	res, _ := a.get("/no/such/page")
	wantStatus(t, res, http.StatusNotFound)
	a.setup()
	res, body := a.get("/no/such/page")
	wantStatus(t, res, http.StatusNotFound)
	if !strings.Contains(body, "Page not found") {
		t.Fatal("no 404 page inside the app frame")
	}
}

func TestMemReadout(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	res, body := a.get("/sys/mem")
	wantStatus(t, res, http.StatusOK)
	if !regexp.MustCompile(`>\d+ MB<`).MatchString(body) {
		t.Fatalf("readout: %s", body)
	}
}
