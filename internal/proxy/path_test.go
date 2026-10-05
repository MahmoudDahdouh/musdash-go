package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func hashOf(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func basic(user, password string) http.Header {
	return http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))}}
}

func TestLookupPicksTheLongestPathOnSegmentBoundaries(t *testing.T) {
	tab, err := Parse(strings.NewReader(`{"routes":[
		{"host":"example.com","target":"127.0.0.1:1"},
		{"host":"example.com","path":"/api","target":"127.0.0.1:2"},
		{"host":"example.com","path":"/api/v2","target":"127.0.0.1:3"},
		{"host":"paths.example.com","path":"/only","target":"127.0.0.1:4"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ host, path, target string }{
		{"example.com", "/", "127.0.0.1:1"},
		{"example.com", "/apix", "127.0.0.1:1"},
		{"example.com", "/ap", "127.0.0.1:1"},
		{"example.com", "/API", "127.0.0.1:1"},
		{"example.com", "/x/api", "127.0.0.1:1"},
		{"example.com", "/api", "127.0.0.1:2"},
		{"example.com", "/api/", "127.0.0.1:2"},
		{"example.com", "/api/v1/users", "127.0.0.1:2"},
		{"example.com", "/api/v22", "127.0.0.1:2"},
		{"example.com", "/api/v2", "127.0.0.1:3"},
		{"example.com", "/api/v2/users", "127.0.0.1:3"},
		{"EXAMPLE.com:443", "/api/v2/", "127.0.0.1:3"},
		{"paths.example.com", "/only/x", "127.0.0.1:4"},
		{"paths.example.com", "/", ""},
		{"paths.example.com", "/onlyx", ""},
		{"other.example.com", "/api", ""},
	}
	for _, c := range cases {
		rt, ok := tab.Lookup(c.host, c.path)
		if rt.Target != c.target || ok != (c.target != "") {
			t.Errorf("%s%s: got %q (%v), want %q", c.host, c.path, rt.Target, ok, c.target)
		}
	}
}

func TestPlain(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "/",
		"/":                 "/",
		"/a":                "/a",
		"/a/":               "/a/",
		"//a":               "/a",
		"/a//b":             "/a/b",
		"/a/./b":            "/a/b",
		"/public/../admin":  "/admin",
		"/public/../admin/": "/admin/",
		"/../../admin":      "/admin",
		"/a/..":             "/",
		"*":                 "/*",
	} {
		if got := Plain(in); got != want {
			t.Errorf("Plain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidPath(t *testing.T) {
	for _, p := range []string{"", "/api", "/api/v2", "/a-b_c.d", "/~user"} {
		if !ValidPath(p) {
			t.Errorf("%q should be a valid prefix", p)
		}
	}
	for _, p := range []string{"/", "api", "/api/", "/a//b", "/a/../b", "/a/.", "/a b", "/a?b", "/a#b", "/a%2Fb", `/a\b`, "/a\x00", "/" + strings.Repeat("a", 200)} {
		if ValidPath(p) {
			t.Errorf("%q should be refused", p)
		}
	}
}

func TestRoutesByPathAndStripsThePrefix(t *testing.T) {
	site, api, files := backend(t, "site"), backend(t, "api"), backend(t, "files")
	p, _ := newProxy(t, false,
		Route{Host: "example.com", Target: site},
		Route{Host: "example.com", Path: "/api", Target: api},
		Route{Host: "example.com", Path: "/files", Target: files, StripPrefix: true})

	for path, want := range map[string]string{
		"/":              "site|",
		"/apix":          "site|",
		"/api":           "api|",
		"/api/users?a=1": "api|",
		"/files/a.txt":   "files|",
	} {
		if rec := request(p.HTTP(), "GET", "example.com", path, nil); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want %s", path, rec.Code, rec.Body, want)
		}
	}
	// Passed on as it came unless the route strips its prefix.
	if rec := request(p.HTTP(), "GET", "example.com", "/api/users?a=1", nil); !strings.HasSuffix(rec.Body.String(), "path=/api/users?a=1") {
		t.Errorf("api path changed: %s", rec.Body)
	}
	for path, want := range map[string]string{
		"/files/a.txt?x=1":  "path=/a.txt?x=1",
		"/files":            "path=/",
		"/files/":           "path=/",
		"/files/a%2Fb/c":    "path=/a%2Fb/c",
		"/%66iles/a%2Fb":    "path=/a/b",
		"/files/sp%20ace/x": "path=/sp%20ace/x",
	} {
		if rec := request(p.HTTP(), "GET", "example.com", path, nil); rec.Code != 200 || !strings.HasSuffix(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want …%s", path, rec.Code, rec.Body, want)
		}
	}
}

func TestForwardedPrefixIsTheProxysOwn(t *testing.T) {
	seen := make(chan string, 2)
	echo := func(name string) string {
		return backendFunc(t, func(w http.ResponseWriter, r *http.Request) { seen <- name + ":" + r.Header.Get("X-Forwarded-Prefix") })
	}
	p, _ := newProxy(t, false,
		Route{Host: "example.com", Target: echo("site")},
		Route{Host: "example.com", Path: "/files", Target: echo("files"), StripPrefix: true})
	spoof := http.Header{"X-Forwarded-Prefix": {"/evil"}}
	request(p.HTTP(), "GET", "example.com", "/x", spoof)
	if got := <-seen; got != "site:" {
		t.Errorf("a client's X-Forwarded-Prefix reached the app: %q", got)
	}
	request(p.HTTP(), "GET", "example.com", "/files/x", spoof)
	if got := <-seen; got != "files:/files" {
		t.Errorf("stripped route: %q", got)
	}
}

func TestAPathThatIsNotPlainIsRedirectedWhereThePathDecides(t *testing.T) {
	site, admin := backend(t, "site"), backend(t, "admin")
	p, _ := newProxy(t, false,
		Route{Host: "example.com", Target: site},
		Route{Host: "example.com", Path: "/admin", Target: admin, AuthUser: "u", AuthHash: hashOf(t, "pw")},
		Route{Host: "simple.example.com", Target: site})

	for path, want := range map[string]string{
		"/public/../admin":      "/admin",
		"//admin":               "/admin",
		"/./admin/x?q=1":        "/admin/x?q=1",
		"/public/%2e%2e/admin":  "/admin",
		"/public%2F..%2Fadmin/": "/admin/",
		`//\evil.example.net`:   "/%5Cevil.example.net",
		"///evil.example.net":   "/evil.example.net",
	} {
		rec := request(p.HTTP(), "GET", "example.com", path, nil)
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d to %q, want a redirect to %q (%s)", path, rec.Code, rec.Header().Get("Location"), want, rec.Body)
		}
	}
	// The guarded path stays guarded however it is spelled.
	for _, path := range []string{"/admin", "/admin/", "/admin/x", "/%61dmin", "/admin%2Fx"} {
		if rec := request(p.HTTP(), "GET", "example.com", path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", path, rec.Code, rec.Body)
		}
	}
	// A host with one plain route passes paths on as they come.
	if rec := request(p.HTTP(), "GET", "simple.example.com", "/a//b/../c", nil); rec.Code != 200 || !strings.HasSuffix(rec.Body.String(), "path=/a//b/../c") {
		t.Errorf("simple host: %d %s", rec.Code, rec.Body)
	}
}

func TestAHostWithOnlyPathsAnswersNothingElsewhere(t *testing.T) {
	p, _ := newProxy(t, false, Route{Host: "example.com", Path: "/app", Target: backend(t, "app")})
	if rec := request(p.HTTP(), "GET", "example.com", "/", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/: %d", rec.Code)
	}
	if rec := request(p.HTTP(), "GET", "example.com", "/app/x", nil); rec.Code != 200 {
		t.Errorf("/app/x: %d", rec.Code)
	}
}

func TestBasicAuth(t *testing.T) {
	var got http.Header
	target := backendFunc(t, func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone(); w.Write([]byte("inside")) })
	hash := hashOf(t, "s3cret pass")
	p, _ := newProxy(t, false,
		Route{Host: "locked.example.com", Target: target, AuthUser: "ada", AuthHash: hash},
		Route{Host: "other.example.com", Target: target, AuthUser: "ada", AuthHash: hashOf(t, "another")})

	denied := map[string]http.Header{
		"nothing":          nil,
		"wrong password":   basic("ada", "guess"),
		"wrong user":       basic("bob", "s3cret pass"),
		"empty password":   basic("ada", ""),
		"long password":    basic("ada", "s3cret pass"+strings.Repeat("x", 80)),
		"bearer":           {"Authorization": {"Bearer s3cret pass"}},
		"not base64":       {"Authorization": {"Basic !!!"}},
		"other's password": basic("ada", "another"),
	}
	for name, header := range denied {
		got = nil
		rec := request(p.HTTP(), "GET", "locked.example.com", "/", header)
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") || got != nil {
			t.Errorf("%s: %d, reached the app: %v", name, rec.Code, got != nil)
		}
		if strings.Contains(rec.Body.String()+rec.Header().Get("WWW-Authenticate"), hash) {
			t.Errorf("%s: the hash is in the answer", name)
		}
	}

	rec := request(p.HTTP(), "GET", "locked.example.com", "/page", basic("ada", "s3cret pass"))
	if rec.Code != 200 || rec.Body.String() != "inside" {
		t.Fatalf("right password: %d %s", rec.Code, rec.Body)
	}
	if got.Get("Authorization") != "" {
		t.Errorf("the app was given the proxy's password: %q", got.Get("Authorization"))
	}
	// Verified on one route says nothing about another.
	if rec := request(p.HTTP(), "GET", "other.example.com", "/", basic("ada", "s3cret pass")); rec.Code != http.StatusUnauthorized {
		t.Errorf("another route accepted this route's password: %d", rec.Code)
	}
}

func TestVerifiedCredentialsAreRememberedForAWhile(t *testing.T) {
	target := backend(t, "app")
	hash := hashOf(t, "pw")
	p, routes := newProxy(t, false, Route{Host: "locked.example.com", Target: target, AuthUser: "ada", AuthHash: hash})
	now := time.Unix(1_700_000_000, 0)
	p.auth.now = func() time.Time { return now }

	ok := func() bool {
		return request(p.HTTP(), "GET", "locked.example.com", "/", basic("ada", "pw")).Code == 200
	}
	if !ok() || len(p.auth.verified) != 1 {
		t.Fatalf("first request: remembered %d", len(p.auth.verified))
	}
	// While remembered, no comparison runs: one that is made to wait for
	// ever would never answer.
	p.auth.turn <- struct{}{}
	if !ok() {
		t.Fatal("a remembered password was compared again")
	}
	// A wrong password is not helped by the right one being remembered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://locked.example.com/", nil)
	req.Header = basic("ada", "guess")
	if got := p.auth.check(req, p.Table().hosts["locked.example.com"][0]); got != authDenied {
		t.Fatalf("wrong password with a full gate: %v", got)
	}
	<-p.auth.turn

	now = now.Add(authRemember + time.Second)
	p.auth.turn <- struct{}{}
	done := make(chan bool, 1)
	go func() { done <- ok() }()
	select {
	case <-done:
		t.Fatal("an expired entry was still accepted without a comparison")
	case <-time.After(100 * time.Millisecond):
	}
	<-p.auth.turn
	if !<-done {
		t.Fatal("the right password was refused after the entry expired")
	}

	// A new password makes the old one useless at once.
	writeRoutes(t, routes, Route{Host: "locked.example.com", Target: target, AuthUser: "ada", AuthHash: hashOf(t, "new")})
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok() {
		t.Fatal("the old password still works after it was changed")
	}
}

func TestRememberedCredentialsAreBounded(t *testing.T) {
	g := newGate()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	for i := 0; i < authRemembered*3; i++ {
		g.remember(sha256.Sum256([]byte{byte(i), byte(i >> 8)}))
	}
	if len(g.verified) != authRemembered {
		t.Fatalf("remembered %d, want at most %d", len(g.verified), authRemembered)
	}
	n := authRemembered*3 - 1
	last := sha256.Sum256([]byte{byte(n), byte(n >> 8)})
	if !g.remembered(last) {
		t.Fatal("the newest entry was not kept")
	}
	// Expired entries make room before live ones do.
	now = now.Add(authRemember + time.Second)
	fresh := sha256.Sum256([]byte("fresh"))
	g.remember(fresh)
	if len(g.verified) != 1 || !g.remembered(fresh) {
		t.Fatalf("after expiry: %d entries", len(g.verified))
	}
}

func TestComparisonsWaitTheirTurnAndGiveUp(t *testing.T) {
	g := newGate()
	g.turn <- struct{}{} // a comparison that never ends
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x.example.com/", nil)
	req.Header = basic("ada", "pw")
	start := time.Now()
	if got := g.check(req, Route{Host: "x.example.com", AuthUser: "ada", AuthHash: hashOf(t, "pw")}); got != authDenied {
		t.Fatalf("got %v", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a request whose client left kept waiting")
	}
}

func TestAPasswordIsNeverAskedForOverPlainHTTPWhenThereIsHTTPS(t *testing.T) {
	p, _ := newProxy(t, true,
		Route{Host: "locked.example.com", Target: backend(t, "app"), TLS: true, AuthUser: "ada", AuthHash: hashOf(t, "pw")})
	rec := request(p.HTTP(), "GET", "locked.example.com", "/x", basic("ada", "pw"))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://locked.example.com/x" {
		t.Fatalf("plain HTTP: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := request(p.HTTPS(), "GET", "locked.example.com", "/x", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HTTPS without a password: %d", rec.Code)
	}
	if rec := request(p.HTTPS(), "GET", "locked.example.com", "/x", basic("ada", "pw")); rec.Code != 200 {
		t.Fatalf("HTTPS with the password: %d", rec.Code)
	}
}

func TestOneCertificatePerHostWhateverItsPaths(t *testing.T) {
	p, _ := newProxy(t, true,
		Route{Host: "example.com", Path: "/a", Target: "127.0.0.1:1"},
		Route{Host: "example.com", Path: "/b", Target: "127.0.0.1:2", TLS: true},
		Route{Host: "plain.example.com", Path: "/a", Target: "127.0.0.1:1"})
	if err := p.hostPolicy(context.Background(), "example.com"); err != nil {
		t.Errorf("a host with a TLS path was refused a certificate: %v", err)
	}
	if p.hostPolicy(context.Background(), "plain.example.com") == nil {
		t.Error("a host without TLS would get a certificate")
	}
	// Every path of the host is then served over HTTPS, not only the one
	// that asked.
	rec := request(p.HTTP(), "GET", "example.com", "/a/x", nil)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://example.com/a/x" {
		t.Errorf("/a over HTTP: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestParseValidatesPathsAndPasswords(t *testing.T) {
	costly, err := bcrypt.GenerateFromPassword([]byte("x"), maxAuthCost+1)
	if err != nil {
		t.Fatal(err)
	}
	hash := hashOf(t, "x")
	bad := map[string]string{
		"same host and path twice": `{"routes":[{"host":"a.example.com","path":"/x","target":"127.0.0.1:1"},{"host":"A.example.com","path":"/x","target":"127.0.0.1:2"}]}`,
		"same host twice":          `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1"},{"host":"a.example.com","target":"127.0.0.1:2"}]}`,
		"path with a slash at end": `{"routes":[{"host":"a.example.com","path":"/x/","target":"127.0.0.1:1"}]}`,
		"path with dots":           `{"routes":[{"host":"a.example.com","path":"/x/../y","target":"127.0.0.1:1"}]}`,
		"redirect on a path":       `{"routes":[{"host":"a.example.com","path":"/x","redirect_to":"b.example.com"}]}`,
		"user without a hash":      `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","auth_user":"u"}]}`,
		"hash without a user":      `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","auth_hash":"` + hash + `"}]}`,
		"not a hash":               `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","auth_user":"u","auth_hash":"plain"}]}`,
		"a hash that costs much":   `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","auth_user":"u","auth_hash":"` + string(costly) + `"}]}`,
		"user with a colon":        `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1","auth_user":"u:v","auth_hash":"` + hash + `"}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	good := `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1"},{"host":"a.example.com","path":"/x","target":"127.0.0.1:2","strip_prefix":true,"auth_user":"u","auth_hash":"` + hash + `"}]}`
	if _, err := Parse(strings.NewReader(good)); err != nil {
		t.Fatal(err)
	}
}

// One app routed twice on a host: open at "/", behind a password at
// "/admin". Whatever spelling an app might still read as "/admin" asks for
// the password, though the proxy itself would send it to the open route.
func TestAGuardedPathIsGuardedHoweverAnAppMightReadIt(t *testing.T) {
	app := backend(t, "app")
	p, _ := newProxy(t, false,
		Route{Host: "example.com", Target: app},
		Route{Host: "example.com", Path: "/admin", Target: app, AuthUser: "ada", AuthHash: hashOf(t, "pw")},
		Route{Host: "example.com", Path: "/admin/public", Target: app},
		Route{Host: "plain.example.com", Target: app},
		Route{Host: "plain.example.com", Path: "/admin", Target: app})

	guarded := []string{"/Admin", "/ADMIN/users", "/admin;x", "/admin;jsessionid=1/users", "/x/..;/admin", "/x/..;a=b/admin/",
		"/%5Cadmin", "/x/..%5Cadmin", "/%252e%252e/admin", "/x/%252e%252e/admin", "/%2561dmin", "/aDmIn/", "/admin%3Bx"}
	for _, path := range guarded {
		rec := request(p.HTTP(), "GET", "example.com", path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a password: %d %s", path, rec.Code, rec.Body)
			continue
		}
		if rec := request(p.HTTP(), "GET", "example.com", path, basic("ada", "pw")); rec.Code != 200 {
			t.Errorf("%s with the password: %d", path, rec.Code)
		}
		if rec := request(p.HTTP(), "GET", "example.com", path, basic("ada", "guess")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with a wrong password: %d", path, rec.Code)
		}
	}
	// What is not the guarded path stays open, and so does what was opened
	// below it on purpose.
	for _, path := range []string{"/", "/administrator", "/admins", "/x/admin", "/about;admin", "/admin/public", "/admin/public/x", "/public/Admin"} {
		if rec := request(p.HTTP(), "GET", "example.com", path, nil); rec.Code != 200 {
			t.Errorf("%s asks for a password: %d", path, rec.Code)
		}
	}
	// A host with no password asks for none.
	if rec := request(p.HTTP(), "GET", "plain.example.com", "/Admin", nil); rec.Code != 200 {
		t.Errorf("a host without passwords: %d", rec.Code)
	}
}

func TestLoose(t *testing.T) {
	for in, want := range map[string]string{
		"/Admin":            "/admin",
		"/admin;x/Users":    "/admin/users",
		"/x/..;/admin":      "/admin",
		`/\admin`:           "/admin",
		"/%2e%2e/admin":     "/admin",
		"/%252e%252e/admin": "/admin",
		"/a%zz":             "/a%zz",
		"/":                 "/",
	} {
		if got := loose(in); got != want {
			t.Errorf("loose(%q) = %q, want %q", in, got, want)
		}
	}
}

// The file as a proxy from before paths and passwords reads it: it knows
// "routes" and, of a route, the host and target. Hosts kept under the
// newer key do not exist for it.
func TestRoutesOfGuardedHostsAreUnderTheirOwnKey(t *testing.T) {
	doc := `{"routes":[{"host":"open.example.com","target":"127.0.0.1:1"}],
		"routes_v2":[{"host":"locked.example.com","target":"127.0.0.1:2","auth_user":"ada","auth_hash":"` + hashOf(t, "pw") + `"},
			{"host":"split.example.com","target":"127.0.0.1:3"},{"host":"split.example.com","path":"/api","target":"127.0.0.1:4"}]}`
	tab, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if rt, ok := tab.Lookup("locked.example.com", "/"); !ok || rt.AuthUser != "ada" {
		t.Fatalf("the guarded host: %+v %v", rt, ok)
	}
	if rt, _ := tab.Lookup("split.example.com", "/api/x"); rt.Target != "127.0.0.1:4" {
		t.Fatalf("the host routed by path: %+v", rt)
	}
	if tab.Len() != 3 {
		t.Fatalf("%d hosts", tab.Len())
	}
	// One host in both lists is one host: the same path twice is refused.
	both := `{"routes":[{"host":"a.example.com","target":"127.0.0.1:1"}],"routes_v2":[{"host":"a.example.com","target":"127.0.0.1:2"}]}`
	if _, err := Parse(strings.NewReader(both)); err == nil {
		t.Fatal("a host routed in both lists was accepted")
	}
}

// A flood of guesses at one site takes a few places in the line for a
// comparison, not all of them: the rest are refused at once, and somebody
// signing in elsewhere is not kept waiting behind them.
func TestGuessesAtOneRouteDoNotFillTheLine(t *testing.T) {
	g := newGate()
	flooded := Route{Host: "a.example.com", AuthUser: "ada", AuthHash: hashOf(t, "pw")}
	g.turn <- struct{}{} // a comparison that takes its time
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan authResult, 64)
	guess := func(i int) {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://a.example.com/", nil)
		req.Header = basic("ada", "guess "+string(rune('a'+i)))
		results <- g.check(req, flooded)
	}
	for i := range authWaiting {
		go guess(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		n := g.waiting["a.example.com\x00"]
		g.mu.Unlock()
		if n == authWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests waiting, want %d", n, authWaiting)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The line for this route is full: the next guess is turned away
	// without waiting.
	start := time.Now()
	guess(20)
	if got := <-results; got != authBusy || time.Since(start) > time.Second {
		t.Fatalf("a guess beyond the line: %v after %s", got, time.Since(start))
	}
	// Another route still has its own places.
	other := Route{Host: "b.example.com", AuthUser: "bob", AuthHash: hashOf(t, "secret")}
	done := make(chan authResult, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://b.example.com/", nil)
		req.Header = basic("bob", "secret")
		done <- g.check(req, other)
	}()
	cancel() // the guessers leave
	for range authWaiting {
		if got := <-results; got != authDenied {
			t.Fatalf("a guesser who left: %v", got)
		}
	}
	<-g.turn
	if got := <-done; got != authOK {
		t.Fatalf("a sign-in at another site: %v", got)
	}
	g.mu.Lock()
	left := len(g.waiting)
	g.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d routes still counted as waited for", left)
	}
}

// Somebody working behind a password is not sent back to the line while
// they keep working.
func TestUsingAPasswordKeepsItRemembered(t *testing.T) {
	g := newGate()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	id := sha256.Sum256([]byte("x"))
	g.remember(id)
	for range 10 {
		now = now.Add(authRemember - time.Second)
		if !g.remembered(id) {
			t.Fatal("credentials in use were forgotten")
		}
	}
	now = now.Add(authRemember + time.Second)
	if g.remembered(id) {
		t.Fatal("credentials nobody used for a while are still remembered")
	}
}
