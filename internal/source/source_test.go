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
