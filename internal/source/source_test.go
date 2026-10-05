package source

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseRepo(t *testing.T) {
	good := map[string]Repo{
		"https://github.com/acme/shop":               {Host: "github.com", Owner: "acme", Name: "shop"},
		"https://github.com/acme/shop.git":           {Host: "github.com", Owner: "acme", Name: "shop"},
		"https://GitHub.com/Acme/Shop/":              {Host: "github.com", Owner: "Acme", Name: "Shop"},
		"git@github.com:acme/shop.git":               {Host: "github.com", Owner: "acme", Name: "shop", SSH: true},
		"ssh://git@git.example.com:2222/team/app":    {Host: "git.example.com", Owner: "team", Name: "app", SSH: true},
		"https://gitlab.com/group/sub/project.git":   {Host: "gitlab.com", Owner: "group/sub", Name: "project"},
		"https://git.example.com:8443/o/r_e.p-o.git": {Host: "git.example.com", Owner: "o", Name: "r_e.p-o"},
	}
	for in, want := range good {
		got, err := ParseRepo(in)
		if err != nil {
			t.Errorf("%q refused: %v", in, err)
			continue
		}
		if got.Host != want.Host || got.Owner != want.Owner || got.Name != want.Name || got.SSH != want.SSH {
			t.Errorf("%q → %+v, want %+v", in, got, want)
		}
	}
	bad := []string{
		"", "acme/shop", "github.com/acme/shop",
		"ext::sh -c 'touch /tmp/pwned'", "ext::sh", "file:///etc/passwd", "/etc/passwd", "../../repo",
		"--upload-pack=touch /tmp/pwned", "-oProxyCommand=evil", "git://github.com/acme/shop", "http://github.com/acme/shop",
		"https://user:pass@github.com/acme/shop", "https://token@github.com/acme/shop",
		"https://github.com/acme/shop?x=1", "https://github.com/acme/shop#frag", "https://github.com/acme",
		"https://github.com/acme/../shop", "https://github.com//acme/shop", "https://github.com/acme/shop;id",
		"https://github.com/acme/$(id)", "git@github.com:acme/shop.git --upload-pack=x", "ssh://git:pw@host/a/b",
		"https://-evil.com/a/b", "git@-evil:a/b", strings.Repeat("a", 600),
	}
	for _, in := range bad {
		if got, err := ParseRepo(in); err == nil {
			t.Errorf("%q accepted as %+v", in, got)
		}
	}
}

func TestValidBranchAndPath(t *testing.T) {
	for _, b := range []string{"main", "release/1.2", "feature/add_login-v2", "v1.0", "a"} {
		if !ValidBranch(b) {
			t.Errorf("branch %q refused", b)
		}
	}
	for _, b := range []string{"", "--force", "-b", "a b", "a..b", "a//b", "main/", "main.", "x.lock", "a;b", "$(id)", "a\nb", "refs~1", "a:b", "a^b", "a?b", strings.Repeat("a", 201)} {
		if ValidBranch(b) {
			t.Errorf("branch %q accepted", b)
		}
	}
	for _, p := range []string{"", ".", "Dockerfile", "docker/Dockerfile.prod", "apps/web", "public_html"} {
		if !ValidRelPath(p) {
			t.Errorf("path %q refused", p)
		}
	}
	for _, p := range []string{"/etc/passwd", "../secret", "a/../../b", "-f", "a//b", "a b", "a;b", "a/..", "$(x)"} {
		if ValidRelPath(p) {
			t.Errorf("path %q accepted", p)
		}
	}
}

func TestGenerateDeployKey(t *testing.T) {
	pub, priv, err := GenerateDeployKey("musdash-shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " musdash-shop") || strings.Contains(pub, "\n") {
		t.Fatalf("public key %q", pub)
	}
	parsedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("private key does not parse: %v", err)
	}
	if string(signer.PublicKey().Marshal()) != string(parsedPub.Marshal()) {
		t.Fatal("the two halves do not belong together")
	}
	other, _, _ := GenerateDeployKey("x")
	if other == pub {
		t.Fatal("two generated keys are identical")
	}
}

func TestReadPushChecksTheSignature(t *testing.T) {
	secret, body := []byte("s3cret"), `{"ref":"refs/heads/main","after":"a1b2c3d4e5f6","repository":{"full_name":"acme/shop"}}`
	good := Sign(secret, []byte(body))
	p, signed := ReadPush(strings.NewReader(body), secret, good)
	if !signed || p != (Push{Repo: "acme/shop", Branch: "main", Commit: "a1b2c3d4e5f6"}) {
		t.Fatalf("a correct signature was refused: %+v %v", p, signed)
	}
	flipped := good[:len(good)-1] + "0"
	if flipped == good {
		flipped = good[:len(good)-1] + "1"
	}
	for _, header := range []string{
		"", "sha1=" + good[7:], flipped, "sha256=nothex", "sha256=", "sha256=abcd",
		Sign([]byte("other"), []byte(body)), Sign(secret, []byte("tampered")),
	} {
		if p, signed := ReadPush(strings.NewReader(body), secret, header); signed || p != (Push{}) {
			t.Errorf("header %q was accepted: %+v", header, p)
		}
	}
	// Bytes after the JSON value are part of what was signed.
	if _, signed := ReadPush(strings.NewReader(body+" trailing"), secret, good); signed {
		t.Fatal("a body with extra bytes passed with the signature of the shorter one")
	}
	// No secret configured means nothing verifies, even a "correct" MAC.
	for _, empty := range [][]byte{nil, {}} {
		if _, signed := ReadPush(strings.NewReader(body), empty, Sign(empty, []byte(body))); signed {
			t.Fatal("an empty secret must verify nothing")
		}
	}
}

func TestReadPushEvents(t *testing.T) {
	secret := []byte("s3cret")
	read := func(body string) Push {
		t.Helper()
		p, signed := ReadPush(strings.NewReader(body), secret, Sign(secret, []byte(body)))
		if !signed {
			t.Fatalf("a signed body was refused: %s", body)
		}
		return p
	}
	// The wanted values are found wherever they sit, and same-named keys
	// deeper in the document are not mistaken for them.
	push := `{"commits":[{"ref":"refs/heads/evil","repository":{"full_name":"evil/evil"},"added":["a",{"b":[1,2,{"c":null}]}]}],
		"ref":"refs/heads/release/1.2","before":"0000","after":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","deleted":false,
		"repository":{"id":7,"owner":{"full_name":"nested/owner","login":"acme"},"full_name":"acme/shop","topics":["x"]},
		"pusher":{"name":"sam"}}`
	if got := read(push); got != (Push{Repo: "acme/shop", Branch: "release/1.2", Commit: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}) {
		t.Fatalf("%+v", got)
	}
	ignored := map[string]string{
		"tag push":       `{"ref":"refs/tags/v1","after":"abc","repository":{"full_name":"acme/shop"}}`,
		"branch deleted": `{"ref":"refs/heads/old","after":"abc","deleted":true,"repository":{"full_name":"acme/shop"}}`,
		"zero after":     `{"ref":"refs/heads/old","after":"0000000000000000000000000000000000000000","repository":{"full_name":"acme/shop"}}`,
		"ping event":     `{"zen":"Keep it logically awesome.","hook_id":1}`,
		"wrong types":    `{"ref":["refs/heads/main"],"after":{"x":1},"repository":"acme/shop"}`,
		"not an object":  `["refs/heads/main"]`,
		"not json":       `ref=refs/heads/main`,
		"cut short":      `{"ref":"refs/heads/main","after":"abc","repository":{"full_name":`,
		"empty":          ``,
	}
	for name, body := range ignored {
		if p := read(body); p.Branch != "" {
			t.Errorf("%s: treated as a push: %+v", name, p)
		}
	}
	// A large value that is not wanted is skipped, not kept.
	big := `{"commits":"` + strings.Repeat("x", 1<<20) + `","ref":"refs/heads/main","after":"abc"}`
	if got := read(big); got.Branch != "main" {
		t.Fatalf("%+v", got)
	}
}

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// verifyJWT checks an RS256 token against the key and returns its claims.
func verifyJWT(t *testing.T, token string, key *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header %s", header)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestAppJWT(t *testing.T) {
	key, pemBytes := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	token, err := AppJWT(12345, pemBytes, now)
	if err != nil {
		t.Fatal(err)
	}
	claims := verifyJWT(t, token, &key.PublicKey)
	if claims["iss"] != "12345" || claims["iat"].(float64) != float64(now.Unix()-60) || claims["exp"].(float64) != float64(now.Unix()+540) {
		t.Fatalf("claims %v", claims)
	}
	// GitHub also issues PKCS#8 keys.
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := AppJWT(1, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), now); err != nil {
		t.Fatalf("PKCS#8 key refused: %v", err)
	}
	for name, bad := range map[string][]byte{"empty": nil, "not pem": []byte("hello"), "garbage pem": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("x")})} {
		if _, err := AppJWT(1, bad, now); err == nil {
			t.Errorf("%s key accepted", name)
		}
	}
}

// fakeGitHub is a stand-in for api.github.com that checks how it is called.
type fakeGitHub struct {
	t   *testing.T
	key *rsa.PublicKey

	mu       sync.Mutex
	requests []string
	tokenReq map[string]any
	// comments holds the text of each comment by id; a deleted one is "".
	comments map[int64]string
	nextID   int64
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	asApp := func() {
		if claims := verifyJWT(f.t, bearer, f.key); claims["iss"] != "777" {
			f.t.Errorf("JWT issuer %v", claims["iss"])
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions":
		io.WriteString(w, `{"id":777,"slug":"musdash-test","name":"musdash test","html_url":"https://github.com/apps/musdash-test","client_id":"Iv1.abc","client_secret":"cs","webhook_secret":"whs","pem":"-----BEGIN RSA PRIVATE KEY-----\nMII\n-----END RSA PRIVATE KEY-----\n"}`)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/app-manifests/"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
	case r.Method == "GET" && r.URL.Path == "/repos/acme/shop/installation":
		asApp()
		io.WriteString(w, `{"id":4242}`)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/installation"):
		asApp()
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
	case r.Method == "POST" && r.URL.Path == "/app/installations/4242/access_tokens":
		asApp()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tokenReq = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"ghs_installation_token"}`)
	case r.Method == "GET" && r.URL.Path == "/app/installations":
		asApp()
		io.WriteString(w, `[{"id":4242}]`)
	case r.Method == "GET" && r.URL.Path == "/installation/repositories":
		if bearer != "ghs_installation_token" {
			f.t.Errorf("repositories called with %q", bearer)
		}
		io.WriteString(w, `{"repositories":[{"full_name":"acme/shop","default_branch":"main","private":true},{"full_name":"acme/site","default_branch":"trunk","private":false}]}`)
	case r.Method == "POST" && r.URL.Path == "/repos/acme/shop/issues/12/comments",
		r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/acme/shop/issues/comments/"):
		if bearer != "ghs_installation_token" {
			f.t.Errorf("comment written with %q", bearer)
		}
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.comments == nil {
			f.comments, f.nextID = map[int64]string{}, 9000
		}
		id := f.nextID + 1
		if r.Method == "PATCH" {
			id, _ = strconv.ParseInt(path.Base(r.URL.Path), 10, 64)
			if f.comments[id] == "" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"message":"Not Found"}`)
				return
			}
		} else {
			f.nextID = id
			w.WriteHeader(http.StatusCreated)
		}
		f.comments[id] = body.Body
		io.WriteString(w, `{"id":`+strconv.FormatInt(id, 10)+`}`)
	default:
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, `{"message":"unexpected request"}`)
	}
}

func newFakeGitHub(t *testing.T) (*GitHub, *fakeGitHub, []byte) {
	key, pemBytes := testKey(t)
	fake := &fakeGitHub{t: t, key: &key.PublicKey}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &GitHub{APIBase: srv.URL, HTTP: srv.Client()}, fake, pemBytes
}

func TestUnreachableGitHubDoesNotQuoteTheManifestCode(t *testing.T) {
	// Nothing listens here, so the request fails before GitHub sees it and
	// the one-time code is still good.
	gh := &GitHub{APIBase: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := gh.ConvertManifest(context.Background(), "one-time-c0de")
	if err == nil {
		t.Fatal("no error from an unreachable API")
	}
	if strings.Contains(err.Error(), "one-time-c0de") || strings.Contains(err.Error(), "app-manifests") {
		t.Fatalf("the error quotes the request address: %v", err)
	}
}

func TestGitHubClient(t *testing.T) {
	ctx := context.Background()
	gh, fake, key := newFakeGitHub(t)

	res, err := gh.ConvertManifest(ctx, "good-code")
	if err != nil || res.ID != 777 || res.Slug != "musdash-test" || res.WebhookSecret != "whs" || !strings.Contains(res.PEM, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("manifest: %+v %v", res, err)
	}
	if _, err := gh.ConvertManifest(ctx, "expired/../code"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a bad code must fail with GitHub's status: %v", err)
	}

	token, err := gh.InstallationToken(ctx, 777, key, "acme", "shop")
	if err != nil || token != "ghs_installation_token" {
		t.Fatalf("token: %q %v", token, err)
	}
	// The token is asked for one repository and read access only.
	fake.mu.Lock()
	req := fake.tokenReq
	fake.mu.Unlock()
	repos, _ := req["repositories"].([]any)
	perms, _ := req["permissions"].(map[string]any)
	if len(repos) != 1 || repos[0] != "shop" || perms["contents"] != "read" || len(perms) != 1 {
		t.Fatalf("token request was not narrowed: %v", req)
	}

	if _, err := gh.InstallationToken(ctx, 777, key, "acme", "not-installed"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}

	list, err := gh.Repositories(ctx, 777, key)
	if err != nil || len(list) != 2 || list[0].FullName != "acme/shop" || list[1].DefaultBranch != "trunk" {
		t.Fatalf("repositories: %+v %v", list, err)
	}
}

func TestBasicAuthHeader(t *testing.T) {
	got := BasicAuthHeader("ghs_abc")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Authorization: Basic "))
	if err != nil || string(raw) != "x-access-token:ghs_abc" {
		t.Fatalf("%q → %q %v", got, raw, err)
	}
}

func TestCommentOnPullRequest(t *testing.T) {
	ctx := context.Background()
	gh, fake, key := newFakeGitHub(t)

	id, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "shop", 12, 0, "deployed at https://pr-12.example.com")
	if err != nil || id != 9001 {
		t.Fatalf("first comment: %d %v", id, err)
	}
	// The token is asked for one repository and for pull requests only:
	// it could not read the code.
	fake.mu.Lock()
	req := fake.tokenReq
	fake.mu.Unlock()
	repos, _ := req["repositories"].([]any)
	perms, _ := req["permissions"].(map[string]any)
	if len(repos) != 1 || repos[0] != "shop" || perms["pull_requests"] != "write" || len(perms) != 1 {
		t.Fatalf("token request was not narrowed: %v", req)
	}

	// The same comment is rewritten, not a second one added.
	again, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "shop", 12, id, "deployed again")
	if err != nil || again != id {
		t.Fatalf("rewrite: %d %v", again, err)
	}
	fake.mu.Lock()
	n, text := len(fake.comments), fake.comments[id]
	fake.mu.Unlock()
	if n != 1 || text != "deployed again" {
		t.Fatalf("%d comments, the first says %q", n, text)
	}

	// Somebody deleted it: a new one takes its place.
	fake.mu.Lock()
	fake.comments[id] = ""
	fake.mu.Unlock()
	fresh, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "shop", 12, id, "back")
	if err != nil || fresh == id || fresh == 0 {
		t.Fatalf("after the comment was deleted: %d %v", fresh, err)
	}

	// A long text is cut, and what is not a pull request is refused before
	// anything is asked of GitHub.
	if _, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "shop", 12, fresh, strings.Repeat("x", 3*maxComment)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	long := len(fake.comments[fresh])
	before := len(fake.requests)
	fake.mu.Unlock()
	if long != maxComment {
		t.Fatalf("a comment of %d bytes was sent", long)
	}
	for _, number := range []int{0, -3} {
		if _, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "shop", number, 0, "x"); err == nil {
			t.Errorf("a comment on pull request %d", number)
		}
	}
	fake.mu.Lock()
	after := len(fake.requests)
	fake.mu.Unlock()
	if after != before {
		t.Fatal("GitHub was asked about something that is not a pull request")
	}
	// An App that is not installed there writes nothing.
	if _, err := gh.CommentOnPullRequest(ctx, 777, key, "acme", "not-installed", 12, 0, "x"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

// pullRequestBody is a pull request event as GitHub sends it, cut down to
// its shape.
func pullRequestBody(action, base, head, branch string, number int) string {
	headRepo := `{"id":2,"full_name":"` + head + `","fork":` + strconv.FormatBool(base != head) + `}`
	if head == "" {
		headRepo = "null" // the fork was deleted
	}
	return `{"action":"` + action + `","number":` + strconv.Itoa(number) + `,
		"pull_request":{"number":` + strconv.Itoa(number) + `,"state":"open","title":"a title with \"action\":\"closed\" in it",
			"user":{"login":"sam"},
			"head":{"label":"x:` + branch + `","ref":"` + branch + `","sha":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","repo":` + headRepo + `},
			"base":{"ref":"main","sha":"0000000000000000000000000000000000000001","repo":{"id":1,"full_name":"` + base + `"}}},
		"repository":{"id":1,"full_name":"` + base + `","owner":{"login":"acme"}},
		"sender":{"login":"sam"}}`
}

func TestReadEventPullRequests(t *testing.T) {
	secret := []byte("s3cret")
	read := func(body string) Event {
		t.Helper()
		ev, signed := ReadEvent(strings.NewReader(body), secret, Sign(secret, []byte(body)))
		if !signed {
			t.Fatalf("a signed body was refused: %s", body)
		}
		return ev
	}
	ev := read(pullRequestBody("opened", "acme/shop", "acme/shop", "feature/login", 12))
	want := PullRequest{Repo: "acme/shop", Number: 12, Action: "opened", Branch: "feature/login",
		Commit: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", HeadRepo: "acme/shop", BaseBranch: "main"}
	if ev.PR != want {
		t.Fatalf("%+v", ev.PR)
	}
	if ev.PR.FromFork() {
		t.Fatal("a branch of the repository itself counts as a fork")
	}
	// A pull request is not also a push: its head's "ref" is deeper in.
	if ev.Push.Branch != "" {
		t.Fatalf("a pull request was read as a push to %q", ev.Push.Branch)
	}

	// Where the code is decides whether it is run.
	forks := map[string]string{
		"a fork":                   pullRequestBody("opened", "acme/shop", "mallory/shop", "main", 13),
		"a fork that was deleted":  pullRequestBody("opened", "acme/shop", "", "main", 14),
		"a repository named alike": pullRequestBody("opened", "acme/shop", "acme/shop-fork", "main", 15),
	}
	for name, body := range forks {
		if pr := read(body).PR; pr.Number == 0 || !pr.FromFork() {
			t.Errorf("%s: not seen as a fork: %+v", name, pr)
		}
	}
	if pr := read(pullRequestBody("opened", "Acme/Shop", "acme/shop", "x", 16)).PR; pr.FromFork() {
		t.Error("the same repository in another case is seen as a fork")
	}

	// What is not about a pull request is not read as one.
	for name, body := range map[string]string{
		"a push":                 `{"ref":"refs/heads/main","after":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","repository":{"full_name":"acme/shop"}}`,
		"an issue with a number": `{"action":"opened","number":7,"issue":{"number":7},"repository":{"full_name":"acme/shop"}}`,
		"no number":              `{"action":"opened","pull_request":{"head":{"ref":"x","repo":{"full_name":"acme/shop"}}},"repository":{"full_name":"acme/shop"}}`,
		"a number that is text":  `{"action":"opened","number":"12","pull_request":{"head":{"ref":"x"}},"repository":{"full_name":"acme/shop"}}`,
		"a fraction":             `{"action":"opened","number":1.5,"pull_request":{},"repository":{"full_name":"acme/shop"}}`,
		"a negative number":      `{"action":"opened","number":-4,"pull_request":{},"repository":{"full_name":"acme/shop"}}`,
		"a huge number":          `{"action":"opened","number":1e40,"pull_request":{},"repository":{"full_name":"acme/shop"}}`,
		"not an object":          `[{"action":"opened","number":3,"pull_request":{}}]`,
		"not json":               `action=opened&number=3`,
	} {
		if pr := read(body).PR; pr.Number != 0 {
			t.Errorf("%s: read as pull request %d", name, pr.Number)
		}
	}
	// And a body with a bad signature gives nothing at all.
	body := pullRequestBody("opened", "acme/shop", "acme/shop", "x", 12)
	if ev, signed := ReadEvent(strings.NewReader(body), secret, Sign([]byte("guess"), []byte(body))); signed || ev.PR.Number != 0 {
		t.Fatalf("a wrongly signed pull request was read: %+v", ev)
	}
}
