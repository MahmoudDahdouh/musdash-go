package web

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
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
	pool.DB, pool.Box = d, box
	queue := jobs.New(d.DB, log, 2)
	deployer := deploy.New(d, box, queue, pool, cfg, log, "127.0.0.1:8000")
	deployer.Probe = okProbe{}
	// No test writes to GitHub.
	deployer.Comments = nil
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

// appPath, databasePath and servicePath are the address of a resource's
// page, which says where the resource is: read from the database, so a test
// names a resource by its id as it always did. An id that is nowhere gets
// an address that is nowhere, which is how a test asks for what is not
// there.
func (a *app) appPath(id string) string      { return a.resourcePath(db.KindApp, "apps", id) }
func (a *app) databasePath(id string) string { return a.resourcePath(db.KindDatabase, "databases", id) }
func (a *app) servicePath(id string) string  { return a.resourcePath(db.KindService, "services", id) }

func (a *app) resourcePath(kind, table, id string) string {
	a.t.Helper()
	project, env := "nowhere", "nowhere"
	err := a.db.QueryRow(`SELECT e.project_id, e.id FROM `+table+` r JOIN environments e ON e.id = r.environment_id WHERE r.id = ?`, id).Scan(&project, &env)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.t.Fatal(err)
	}
	return pages.ResourcePath(project, env, kind, id)
}

// createdID is the id of what a form made, read from where its answer
// leads: the address of the new resource, and whatever follows it.
func createdID(res *http.Response, kind string) string {
	_, after, _ := strings.Cut(res.Header.Get("Location"), "/"+kind+"/")
	id, _, _ := strings.Cut(after, "/")
	return id
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
	if !strings.Contains(body, "Getting started") {
		t.Fatal("Home did not show a new install where to start")
	}
	res, body = a.get("/projects")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No projects yet") {
		t.Fatal("the Projects page did not render the empty state")
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
	token := a.csrf("/projects")
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
	token := a.csrf("/projects")

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

	res, body := a.post("/projects", "/projects", url.Values{"name": {"  "}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Enter a name") {
		t.Fatal("no error for an empty name")
	}

	res, _ = a.post("/projects", "/projects", url.Values{"name": {"Shop <script>"}, "description": {"Storefront"}})
	wantStatus(t, res, http.StatusSeeOther)
	path := res.Header.Get("Location")

	// A project has a page of its own, the first of its two tabs: its
	// environments as cards, each a link into it, and Add environment.
	res, body = a.get(path)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Shop &lt;script&gt;") || strings.Contains(body, "Shop <script>") {
		t.Fatal("project name is not escaped")
	}
	if !strings.Contains(body, "Project created.") {
		t.Fatal("the project's page is missing the confirmation")
	}
	card := regexp.MustCompile(`class="tile-link" href="(` + path + `/env/[a-z2-7]+)">production<`).FindStringSubmatch(body)
	if card == nil {
		t.Fatalf("the project's page has no card for production:\n%s", body)
	}
	production := card[1]
	settings := path + "/settings"
	tabs := func(page, body, current string) {
		t.Helper()
		nav := between(t, page, body, `aria-label="Sections"`, `</nav>`)
		if strings.Count(nav, "<a ") != 2 || !strings.Contains(nav, `href="`+path+`"`) || !strings.Contains(nav, `href="`+settings+`"`) ||
			!strings.Contains(nav, "Environments") || !regexp.MustCompile(`href="`+current+`"[^>]*aria-current="page"`).MatchString(nav) {
			t.Fatalf("%s: the project's tabs:\n%s", page, nav)
		}
	}
	tabs(path, body, path)
	for _, want := range []string{`data-open="new-environment"`, `id="new-environment"`, `action="` + path + `/environments"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the project's page lacks %s", want)
		}
	}
	if strings.Contains(body, "data-autoopen") {
		t.Error("a dialog is open on a page nobody sent a form from")
	}
	// Add environment is the header's one button, and primary. Settings is
	// a tab, not a button, and nothing of it is on this page.
	header := between(t, path, body, `<header class="mb-6`, `</header>`)
	if strings.Count(header, "<button") != 1 || !regexp.MustCompile(`btn-primary"[^>]*data-open="new-environment"`).MatchString(header) {
		t.Fatalf("the header's buttons:\n%s", header)
	}
	if strings.Contains(body, "project-settings") || strings.Contains(body, `action="`+path+`/delete"`) || strings.Contains(body, `action="`+path+`"`) {
		t.Error("the project's page still holds its Settings")
	}
	// The environment's page is what runs in it, without the project's tabs.
	res, body = a.get(production)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Nothing is in production yet") || !strings.Contains(body, `href="`+production+`/settings"`) {
		t.Fatal("the environment's page is missing the empty state or the way to its Settings")
	}
	if strings.Contains(body, "new-environment") || strings.Contains(body, `aria-label="Sections"`) {
		t.Error("the environment's page has the project's dialog or its tabs")
	}

	// Settings is the second tab: the project's details, its variables and
	// its deletion, and nothing of an environment.
	res, body = a.get(settings)
	wantStatus(t, res, http.StatusOK)
	tabs(settings, body, settings)
	for _, want := range []string{`id="edit-project"`, `action="` + path + `"`, `href="` + path + `/variables"`, `action="` + path + `/delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the project's Settings lack %s", want)
		}
	}
	if strings.Contains(body, "new-environment") || strings.Contains(body, "/env/") || strings.Contains(body, "data-autoopen") {
		t.Error("the project's Settings hold something of an environment, or an open dialog")
	}
	// Refused, the Edit dialog comes back open there with what was typed.
	res, body = a.post(settings, path, url.Values{"name": {""}, "description": {"Kept"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Enter a name") || !regexp.MustCompile(`id="edit-project"[^>]*data-autoopen`).MatchString(body) || !strings.Contains(body, `value="Kept"`) {
		t.Fatal("a project without a name: no error, no open dialog, or what was typed is lost")
	}
	res, _ = a.post(settings, path, url.Values{"name": {"Shop"}, "description": {""}})
	wantRedirect(t, res, settings)

	// Refused, Add environment comes back open on the project's page.
	res, body = a.post(path, path+"/environments", url.Values{"name": {"Not Valid!"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if strings.Count(body, envNameRule) != 1 || !regexp.MustCompile(`id="new-environment"[^>]*data-autoopen`).MatchString(body) || !strings.Contains(body, production) {
		t.Fatal("a bad environment name: not one error, no open dialog, or not the project's page")
	}
	// A new environment is where the browser goes next.
	res, _ = a.post(path, path+"/environments", url.Values{"name": {"Staging"}})
	if to := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(to, path+"/env/") || to == production {
		t.Fatalf("got %d → %q, want 303 into the new environment", res.StatusCode, to)
	}
	res, body = a.post(path, path+"/environments", url.Values{"name": {"staging"}})
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
	staging := path + "/env/" + envs[1].ID

	// The project's page has a card for each, in the order they were made.
	res, body = a.get(path)
	wantStatus(t, res, http.StatusOK)
	tiles := between(t, path, body, `aria-label="Environments"`, `</ul>`)
	if first, second := strings.Index(tiles, `href="`+production+`"`), strings.Index(tiles, `href="`+staging+`"`); first < 0 || second < first {
		t.Fatalf("the environments' cards:\n%s", tiles)
	}
	res, _ = a.get(path + "/env/nope")
	wantStatus(t, res, http.StatusNotFound)

	// An environment's Settings: its name, its variables, its deletion.
	res, body = a.get(staging + "/settings")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{`action="` + staging + `/settings"`, `href="` + staging + `/variables"`, `action="` + staging + `/delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the environment's Settings lacks %s", want)
		}
	}
	// Renaming holds a name to the rule a new one is held to.
	for name, want := range map[string]string{"Not Valid!": envNameRule, "production": "already has an environment called production"} {
		res, body = a.post(staging+"/settings", staging+"/settings", url.Values{"name": {name}})
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if !strings.Contains(body, want) || !strings.Contains(body, "data-autoopen") {
			t.Errorf("renaming to %q: no %q in an open dialog", name, want)
		}
	}
	res, _ = a.post(staging+"/settings", staging+"/settings", url.Values{"name": {" Preview "}})
	wantRedirect(t, res, staging+"/settings")
	if env, _ := a.db.Environment(ctx, firstTeam(t, a), envs[1].ID); env.Name != "preview" {
		t.Fatalf("the environment is called %q after renaming", env.Name)
	}
	// An environment of another project is not renamed through this one.
	other, otherEnv := a.project("Blog")
	res, _ = a.post(staging+"/settings", "/projects/"+other+"/env/"+envs[1].ID+"/settings", url.Values{"name": {"mine"}})
	wantStatus(t, res, http.StatusNotFound)
	_ = otherEnv

	// Deleting needs the typed name.
	res, _ = a.post(staging+"/settings", staging+"/delete", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, staging+"/settings")
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 2 {
		t.Fatal("environment deleted without confirmation")
	}
	// Deleted, the person is on the project, which has an environment left.
	res, _ = a.post(staging+"/settings", staging+"/delete", url.Values{"confirm": {"preview"}})
	wantRedirect(t, res, path)
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 1 {
		t.Fatal("environment not deleted")
	}
	// The last environment stays, and its Settings offers no way to try.
	if _, body = a.get(production + "/settings"); strings.Contains(body, `action="`+production+`/delete"`) || !strings.Contains(body, "only environment") {
		t.Error("the only environment's Settings offers to delete it")
	}
	res, _ = a.post(production+"/settings", production+"/delete", url.Values{"confirm": {"production"}})
	wantRedirect(t, res, production+"/settings")
	if envs, _ := a.db.ListEnvironments(ctx, projectID); len(envs) != 1 {
		t.Fatal("the last environment was deleted")
	}

	res, _ = a.post(settings, path+"/delete", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, settings)
	res, _ = a.post(settings, path+"/delete", url.Values{"confirm": {"Shop"}})
	wantRedirect(t, res, "/projects")
	res, _ = a.get(path)
	wantStatus(t, res, http.StatusNotFound)
}

// An environment's page is named in the path under its project; the
// switchers in the bar list what is beside it, and only that.
func TestEnvironmentInThePathAndSwitchers(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, production := a.project("Shop")
	staging, err := a.db.CreateEnvironment(ctx, firstTeam(t, a), projectID, "staging")
	if err != nil {
		t.Fatal(err)
	}
	appID := a.newApp(projectID, production, "web", false, nil)
	a.newApp(projectID, staging, "web-next", false, nil)
	otherID, otherEnv := a.project("Blog")

	// Without an environment in the path it is the project's page, which
	// lists the environments and nothing that is in one.
	first, page := a.get("/projects/" + projectID)
	wantStatus(t, first, http.StatusOK)
	if !strings.Contains(page, "/env/"+production.ID+`"`) || !strings.Contains(page, "/env/"+staging.ID+`"`) || strings.Contains(page, ">web<") {
		t.Fatal("the project's page does not list its environments, or lists a resource")
	}
	// Each card counts what is in its environment: an app in each, and a
	// database in production only.
	a.newDatabase(projectID, production, "postgres", "maindb", nil)
	_, page = a.get("/projects/" + projectID)
	tiles := strings.SplitN(between(t, "the project's page", page, `aria-label="Environments"`, `</ul>`), "/env/"+staging.ID, 2)
	if len(tiles) != 2 || strings.Count(tiles[0], `title="app"`) != 1 || strings.Count(tiles[0], `title="database"`) != 1 ||
		strings.Count(tiles[1], `title="app"`) != 1 || strings.Count(tiles[1], `title="databases"`) != 1 {
		t.Fatalf("the cards do not count what is in each environment:\n%s", tiles)
	}
	_, page = a.get("/projects/" + projectID + "/env/" + production.ID)
	if !strings.Contains(page, ">web<") || strings.Contains(page, "web-next") {
		t.Fatal("the page does not show production's resources, or shows another environment's")
	}
	if !strings.Contains(page, "/projects/"+projectID+"/env/"+production.ID+"/new") {
		t.Fatal("no Add resource link for this environment")
	}
	if _, page := a.get("/projects/" + projectID + "/env/" + staging.ID); !strings.Contains(page, "web-next") || strings.Contains(page, ">web<") {
		t.Fatal("staging's page does not show its own resources only")
	}
	// An environment is reached through its own project only.
	res, _ := a.get("/projects/" + otherID + "/env/" + production.ID)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.get("/projects/" + otherID + "/env/" + production.ID + "/new")
	wantStatus(t, res, http.StatusNotFound)
	// The old address of the New project page is gone: it is a dialog.
	res, _ = a.get("/projects/new")
	wantStatus(t, res, http.StatusNotFound)

	// The environment switcher lists this project's environments.
	res, menu := a.get("/projects/" + projectID + "/switch/environments?at=" + staging.ID)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(menu, "/env/"+production.ID) || !strings.Contains(menu, "/env/"+staging.ID) || strings.Contains(menu, otherEnv.ID) {
		t.Fatalf("environment switcher:\n%s", menu)
	}
	if !regexp.MustCompile(`href="[^"]*/env/` + staging.ID + `"[^>]*aria-selected="true"`).MatchString(menu) {
		t.Fatal("the switcher does not mark the current environment")
	}
	// Under them is the way up, to the project's page.
	if !strings.Contains(menu, `href="/projects/`+projectID+`"`) || !strings.Contains(menu, "All environments") {
		t.Fatalf("the environment switcher does not lead to the project's page:\n%s", menu)
	}
	// The resource switcher lists what is in the environment.
	res, menu = a.get("/projects/" + projectID + "/env/" + production.ID + "/switch/resources")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(menu, ">web<") || strings.Contains(menu, "web-next") || !strings.Contains(menu, "/env/"+production.ID+"/new") {
		t.Fatalf("resource switcher:\n%s", menu)
	}

	// The project step of a trail is a switcher too, on every page under
	// a project.
	for _, page := range []string{"/projects/" + projectID + "/env/" + staging.ID, "/projects/" + projectID, "/projects/" + projectID + "/settings", "/projects/" + projectID + "/variables", a.appPath(appID)} {
		if _, body := a.get(page); !strings.Contains(body, `hx-get="/switch/projects?at=`+projectID+`"`) {
			t.Errorf("%s: the project step is not a switcher", page)
		}
	}

	// Another team's project and environment answer a note, not their names.
	if _, err := a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		t.Fatal(err)
	}
	theirs, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	// The project switcher lists the team's projects and no other team's,
	// marks the one the person is in, and leads to the list.
	res, menu = a.get("/switch/projects?at=" + projectID)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(menu, `href="/projects/`+otherID+`"`) || strings.Contains(menu, "Secret") || strings.Contains(menu, theirs.ID) || !strings.Contains(menu, `href="/projects"`) {
		t.Fatalf("project switcher:\n%s", menu)
	}
	if !regexp.MustCompile(`href="/projects/`+projectID+`"[^>]*aria-selected="true"`).MatchString(menu) || regexp.MustCompile(`href="/projects/`+otherID+`"[^>]*aria-selected="true"`).MatchString(menu) {
		t.Fatal("the switcher does not mark the current project, or marks another")
	}
	theirEnvs, _ := a.db.ListEnvironments(ctx, theirs.ID)
	for _, path := range []string{"/projects/" + theirs.ID + "/switch/environments", "/projects/" + theirs.ID + "/env/" + theirEnvs[0].ID + "/switch/resources"} {
		res, menu := a.get(path)
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(menu, theirEnvs[0].ID) || strings.Contains(menu, "production") || !strings.Contains(menu, "is gone") {
			t.Errorf("%s answers for another team:\n%s", path, menu)
		}
	}
}

// The Projects page counts what is in each project and makes one from a
// dialog; a refused name comes back with the dialog open.
func TestProjectTilesAndDialog(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	a.newApp(projectID, env, "web", false, nil)
	a.newApp(projectID, env, "api", false, nil)

	projects, err := a.db.ListProjects(context.Background(), firstTeam(t, a))
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %v, %v", projects, err)
	}
	if p := projects[0]; p.Apps != 2 || p.Databases != 0 || p.Services != 0 || p.EnvCount != 1 {
		t.Fatalf("counts: %+v", p)
	}
	_, page := a.get("/projects")
	if !strings.Contains(page, `class="tile"`) || !strings.Contains(page, `id="new-project"`) || strings.Contains(page, "data-autoopen") {
		t.Fatal("the Projects page has no tiles or no closed dialog")
	}
	res, page := a.post("/projects", "/projects", url.Values{"name": {""}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(page, "data-autoopen") || !strings.Contains(page, "Enter a name") {
		t.Fatal("a refused project does not come back with the dialog open")
	}
}

// A resource's address says where the resource is, and is its address only
// when that is where it is: the same app under another environment or
// another project is not found, a page, a fragment and an action alike.
func TestResourceAddressSaysWhereItIs(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, production := a.project("Shop")
	staging, err := a.db.CreateEnvironment(ctx, firstTeam(t, a), projectID, "staging")
	if err != nil {
		t.Fatal(err)
	}
	otherID, otherEnv := a.project("Blog")
	appID := a.newApp(projectID, production, "web", false, nil)
	m := a.newDatabase(projectID, production, "postgres", "maindb", nil)

	long := "/projects/" + projectID + "/env/" + production.ID + "/app/" + appID
	if got := a.appPath(appID); got != long {
		t.Fatalf("the app's address is %s, want %s", got, long)
	}
	res, page := a.get(long)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{`hx-get="` + long + `/status"`, `action="` + long + `/deploy"`, `href="` + long + `/settings"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the app's page lacks %s", want)
		}
	}
	if strings.Contains(page, `"/apps/`) {
		t.Error("the app's page still holds a short address")
	}
	if _, page = a.get("/projects/" + projectID + "/env/" + production.ID); !strings.Contains(page, `href="`+long+`"`) || !strings.Contains(page, `href="`+a.databasePath(m.ID)+`"`) {
		t.Error("the environment's tiles do not lead to the long addresses")
	}

	token := a.csrf("/projects")
	for _, wrong := range []string{
		"/projects/" + projectID + "/env/" + staging.ID + "/app/" + appID,
		"/projects/" + otherID + "/env/" + production.ID + "/app/" + appID,
		"/projects/" + otherID + "/env/" + otherEnv.ID + "/app/" + appID,
		"/projects/" + projectID + "/env/" + production.ID + "/database/" + appID,
		"/projects/" + projectID + "/env/" + production.ID + "/app/" + m.ID,
	} {
		for _, rest := range []string{"", "/status", "/settings", "/deployments"} {
			if res, _ := a.get(wrong + rest); res.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s%s: %d, want 404", wrong, rest, res.StatusCode)
			}
		}
		if res, _ := a.postRaw(a.client, wrong+"/delete", url.Values{"_csrf": {token}, "confirm": {"web"}}, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s/delete: %d, want 404", wrong, res.StatusCode)
		}
	}
	if _, err := a.db.App(ctx, firstTeam(t, a), appID); err != nil {
		t.Fatalf("the app was deleted through an address that is not its own: %v", err)
	}

	// The short address leads to the long one, with what follows it. It is
	// for reading: nothing is posted to it.
	res, _ = a.get("/apps/" + appID)
	wantRedirect(t, res, long)
	res, _ = a.get("/apps/" + appID + "/deployments/abc/status?was=queued")
	wantRedirect(t, res, long+"/deployments/abc/status?was=queued")
	res, _ = a.get("/databases/" + m.ID + "/backups")
	wantRedirect(t, res, a.databasePath(m.ID)+"/backups")
	for _, gone := range []string{"/apps/nosuchapp", "/databases/" + appID, "/services/" + appID + "/compose"} {
		if res, _ := a.get(gone); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", gone, res.StatusCode)
		}
	}
	if res, _ := a.postRaw(a.client, "/apps/"+appID+"/delete", url.Values{"_csrf": {token}, "confirm": {"web"}}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("POST to the short address: %d, want 404", res.StatusCode)
	}

	// The addresses an environment had lead to the ones it has.
	env := "/projects/" + projectID + "/env/" + production.ID
	for old, to := range map[string]string{
		"/projects/" + projectID + "/e/" + production.ID:          env,
		"/projects/" + projectID + "/e/" + production.ID + "/new": env,
		"/environments/" + production.ID + "/variables":           env + "/variables",
	} {
		res, _ := a.get(old)
		wantRedirect(t, res, to)
	}
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
	token := a.csrf("/projects")

	for _, path := range []string{"/projects/" + p.ID, "/projects/" + p.ID + "/settings"} {
		res, body := a.get(path)
		wantStatus(t, res, http.StatusNotFound)
		if strings.Contains(body, "Secret project") {
			t.Fatalf("%s leaked another team's project", path)
		}
	}
	for path, form := range map[string]url.Values{
		"/projects/" + p.ID:                                    {"name": {"Hijacked"}},
		"/projects/" + p.ID + "/delete":                        {"confirm": {"Secret project"}},
		"/projects/" + p.ID + "/environments":                  {"name": {"staging"}},
		"/projects/" + p.ID + "/env/" + envs[0].ID + "/delete": {"confirm": {"production"}},
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

// The policy forbids inline script and style on every page, not only the
// first one: the pages a signed-in person works in, with their dialogs,
// switchers and tiles, and the component gallery, must have none either.
var elementID = regexp.MustCompile(`\sid="([^"]*)"`)

func TestSignedInPagesHaveNoInlineScriptOrStyle(t *testing.T) {
	a := newApp(t, true)
	a.setup()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", false, nil)
	git := a.newGitApp(projectID, env, "api", nil)
	a.stackServer("front", "3000", nil)
	svc := a.newService(projectID, env, "site", nil)
	mdb := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	base := "/projects/" + projectID
	for _, page := range []string{
		a.databasePath(mdb.ID), a.databasePath(mdb.ID) + "/backups", a.databasePath(mdb.ID) + "/settings",
		a.appPath(git.ID) + "/settings", a.appPath(git.ID) + "/tasks", a.appPath(git.ID) + "/environment",
		a.servicePath(svc.ID), a.servicePath(svc.ID) + "/compose", a.servicePath(svc.ID) + "/settings",
		"/", "/projects", base + "/env/" + env.ID, base + "/env/" + env.ID + "/new", base + "/env/" + env.ID + "/settings", base + "/env/" + env.ID + "/variables", base, base + "/settings", base + "/variables",
		a.appPath(appID), a.appPath(appID) + "/environment", a.appPath(appID) + "/environment/edit", a.appPath(appID) + "/domains", a.appPath(appID) + "/storage", a.appPath(appID) + "/settings",
		"/tags", "/keys", "/keys/tokens", "/servers", "/sources", "/team", "/team/variables", "/account",
		"/settings", "/settings/storages", "/notifications", "/_ui",
	} {
		res, body := a.get(page)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", page, res.StatusCode)
			continue
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: CSP = %q", page, csp)
		}
		for _, inline := range []string{"<script>", " style=", "onclick=", "javascript:"} {
			if strings.Contains(body, inline) {
				t.Errorf("%s contains %q, which the CSP blocks", page, inline)
			}
		}
		// An id that two elements have breaks whatever names it: a label's
		// for, a button's data-open, a link's #fragment. Pages with several
		// dialogs are where that happens.
		ids := map[string]int{}
		for _, m := range elementID.FindAllStringSubmatch(body, -1) {
			ids[m[1]]++
		}
		for id, n := range ids {
			if n > 1 {
				t.Errorf("%s has %d elements with id %q", page, n, id)
			}
		}
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

// between is the part of a page from one mark to the next. A page that
// lacks either fails the test here, with its address.
func between(t *testing.T, page, body, from, to string) string {
	t.Helper()
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("%s: no %s", page, from)
	}
	j := strings.Index(body[i:], to)
	if j < 0 {
		t.Fatalf("%s: no %s after %s", page, to, from)
	}
	return body[i : i+j]
}

// TestHeader pins the bar at the top: the team first, as a switcher, and
// the person last, with a menu that holds the account and Sign out. The
// sidebar holds neither any more.
func TestHeader(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	teamStep := regexp.MustCompile(`(?s)<nav class="crumbs"[^>]*>\s*<div class="select" data-select>\s*<button[^>]*id="crumb-0"[^>]*popovertarget="crumb-0-menu".*?Default team.*?hx-get="/switch/teams"`)
	for _, page := range []string{"/", "/projects", "/projects/" + projectID + "/env/" + env.ID, "/account", "/nosuchpage"} {
		_, body := a.get(page)
		if !teamStep.MatchString(body) {
			t.Errorf("%s: the trail does not start with the team switcher", page)
		}
		bar := between(t, page, body, `<div class="topbar">`, `<main class="page"`)
		for _, want := range []string{`id="usermenu"`, `href="/account"`, `action="/logout"`, `name="_csrf"`, "data-confirm=", "Owner", testEmail, `href="/settings"`, "data-menu-actions"} {
			if !strings.Contains(bar, want) {
				t.Errorf("%s: the bar lacks %s", page, want)
			}
		}
		side := between(t, page, body, `<aside class="sidebar"`, `</aside>`)
		for _, gone := range []string{`action="/logout"`, `href="/account"`, "is using", "/sys/mem"} {
			if strings.Contains(side, gone) {
				t.Errorf("%s: the sidebar still holds %s", page, gone)
			}
		}
		// On a small screen the sidebar is a drawer over the page: its
		// backdrop is the element after it, which is how the stylesheet
		// finds it, and it has a way out of its own, since the bar's button
		// is behind the backdrop then.
		if !regexp.MustCompile(`</aside>\s*<div class="sidebar-backdrop"></div>`).MatchString(body) {
			t.Errorf("%s: no backdrop right after the sidebar", page)
		}
		if !regexp.MustCompile(`<button type="button" class="[^"]*sidebar-close[^"]*" data-nav-toggle aria-label="Close menu"`).MatchString(side) {
			t.Errorf("%s: the sidebar has no Close button", page)
		}
	}
	// A page that has a trail of its own keeps it, after the team.
	_, body := a.get("/projects/" + projectID + "/env/" + env.ID)
	if !regexp.MustCompile(`(?s)Default team.*?crumb-sep.*?href="/projects".*?Shop`).MatchString(body) {
		t.Error("the project's trail does not follow the team")
	}
	// The menu marks the page the person is on and no other: an item is
	// not the current one for being the first.
	_, body = a.get("/account")
	if !regexp.MustCompile(`href="/account"[^>]*aria-current="page"`).MatchString(body) {
		t.Error("the account page is not marked in the person's menu")
	}
	_, body = a.get("/")
	if menu := between(t, "/", body, `id="usermenu-list"`, `<main class="page"`); strings.Contains(menu, "aria-current") || strings.Contains(menu, `aria-selected="true"`) {
		t.Error("the person's menu marks an item on a page that is none of its own")
	}
	// Sign out is the menu's danger item, and so is the button that
	// confirms it.
	if !regexp.MustCompile(`<button type="submit" class="menu-item menu-item-danger"[^>]*data-confirm-tone="danger"[^>]*>`).MatchString(body) {
		t.Error("Sign out is not a danger item with a danger question")
	}
	if regexp.MustCompile(`href="/account"[^>]*menu-item-danger|menu-item-danger[^>]*href="/account"`).MatchString(body) {
		t.Error("the account item is drawn as a danger item")
	}

	// Settings is an Admin's, in the menu as in the sidebar, and so is
	// Notifications in the sidebar.
	if side := between(t, "/", body, `<aside class="sidebar"`, `</aside>`); !strings.Contains(side, `href="/notifications"`) {
		t.Error("an Owner's sidebar has no Notifications")
	}
	member := a.newPerson("Member", db.RoleMember)
	if _, body := member.get("/"); strings.Contains(body, `href="/settings"`) || strings.Contains(body, `href="/notifications"`) || !strings.Contains(body, `id="usermenu"`) {
		t.Error("a Member's page: Settings or Notifications shown, or no menu")
	}
	if res, _ := member.get("/notifications"); res.StatusCode != http.StatusForbidden {
		t.Errorf("a Member reading /notifications: %d", res.StatusCode)
	}

	// What the team switcher lists: the one team, where the person is, and
	// the way to its page.
	res, body := a.get("/switch/teams")
	wantStatus(t, res, http.StatusOK)
	if !regexp.MustCompile(`(?s)href="/"[^>]*aria-selected="true".*?Default team.*?href="/team"`).MatchString(body) {
		t.Fatalf("team options: %s", body)
	}
	// A renamed team is named so on the next page.
	res, _ = a.post("/team", "/team", url.Values{"name": {"Acme"}})
	wantRedirect(t, res, "/team")
	if _, body = a.get("/"); !strings.Contains(body, "Acme") || strings.Contains(body, "Default team") {
		t.Error("the bar does not name the renamed team")
	}

	// The memory line went, and its route with it.
	if res, _ = a.get("/sys/mem"); res.StatusCode != http.StatusNotFound {
		t.Errorf("/sys/mem: status %d", res.StatusCode)
	}
}
