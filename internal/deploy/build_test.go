package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

const testCommit = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"

// stubTokens stands in for GitHub's token endpoint.
type stubTokens struct {
	token string
	err   error
	calls []string
}

func (s *stubTokens) InstallationToken(_ context.Context, appID int64, _ []byte, owner, repo string) (string, error) {
	s.calls = append(s.calls, strconv.FormatInt(appID, 10)+":"+owner+"/"+repo)
	return s.token, s.err
}

// gitEnvRecorder scripts a server for Git deployments and records the
// environment each command ran with.
type gitEnvRecorder struct {
	mu       sync.Mutex
	envs     map[string][]string // first word of the command → environment
	links    map[string]bool
	images   string
	failWith map[string]error // command prefix → error
}

func (g *gitEnvRecorder) handle(line string, c runner.Cmd) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.envs == nil {
		g.envs = make(map[string][]string)
	}
	key := c.Name + " " + c.Args[0]
	if _, seen := g.envs[key]; !seen {
		g.envs[key] = c.Env
	}
	for prefix, err := range g.failWith {
		if strings.HasPrefix(line, prefix) {
			return "", err
		}
	}
	switch {
	case strings.Contains(line, "rev-parse HEAD"):
		return testCommit + "\n", nil
	case strings.Contains(line, "ls-tree HEAD -- "):
		_, p, _ := strings.Cut(line, "ls-tree HEAD -- ")
		if g.links[p] {
			return "120000 blob 9c3a0f1\t" + p + "\n", nil
		}
		return "100644 blob 1a2b3c4\t" + p + "\n", nil
	case strings.HasPrefix(line, "docker images"):
		return g.images, nil
	case strings.HasPrefix(line, "docker inspect"):
		return running, nil
	}
	return "", nil
}

// gitApp turns the test app into one deployed from a repository.
func (e *env) gitApp(mutate func(*db.App)) {
	e.t.Helper()
	app := e.reload()
	app.RepoURL = "https://github.com/acme/shop"
	app.RepoName = "acme/shop"
	app.Branch = "main"
	app.BuildPack = PackDockerfile
	app.AutoDeploy = true
	if mutate != nil {
		mutate(&app)
	}
	_, err := e.db.Exec(`UPDATE apps SET source = 'git', image = '', repo_url = ?, repo_name = ?, branch = ?, build_pack = ?, dockerfile_path = ?,
		base_dir = ?, publish_dir = ?, spa_fallback = ?, git_source_id = ?, ssh_key_id = ?, start_command = ?, docker_options = ? WHERE id = ?`,
		app.RepoURL, app.RepoName, app.Branch, app.BuildPack, app.DockerfilePath, app.BaseDir, app.PublishDir, app.SPAFallback,
		app.GitSourceID, app.SSHKeyID, app.StartCommand, app.DockerOptions, app.ID)
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestGitDeployBuildsThenRuns(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	sealed, _ := e.d.Box.SealString("npm_s3cret")
	runtime, _ := e.d.Box.SealString("runtime-only")
	e.db.ReplaceEnvVars(context.Background(), db.KindApp, e.app.ID, []db.EnvVar{
		{Key: "NPM_TOKEN", Value: sealed, BuildTime: true}, {Key: "PORT", Value: runtime},
	})
	e.gitApp(func(a *db.App) { a.BaseDir = "apps/web"; a.DockerfilePath = "docker/Dockerfile.prod" })

	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	image := ImageRepository(e.app.ID) + ":" + testCommit[:12]
	if dep.Image != image || dep.CommitSHA != testCommit {
		t.Fatalf("deployment records image %q commit %q", dep.Image, dep.CommitSHA)
	}
	if app := e.reload(); app.DeployedImage != image || app.Status != db.AppRunning {
		t.Fatalf("app: %+v", app)
	}

	calls := e.fake.Calls()
	work := filepath.Join(e.cfg.WorkDir(), dep.ID)
	checkout := filepath.Join(work, "src")
	wantClone := "git clone --depth 1 --single-branch --no-tags --branch main -- https://github.com/acme/shop " + checkout
	wantBuild := "docker build --progress plain --tag " + image + " --file " + checkout + "/apps/web/docker/Dockerfile.prod --label musdash.managed=true --build-arg NPM_TOKEN -- " + checkout + "/apps/web"
	order := []string{wantClone, "git -C " + checkout + " rev-parse HEAD",
		"git -C " + checkout + " ls-tree HEAD -- apps", "git -C " + checkout + " ls-tree HEAD -- apps/web",
		"git -C " + checkout + " ls-tree HEAD -- apps/web/docker", "git -C " + checkout + " ls-tree HEAD -- apps/web/docker/Dockerfile.prod",
		wantBuild, "docker run"}
	last := -1
	for _, want := range order {
		i := indexOf(calls, want)
		if i <= last {
			t.Fatalf("%q is missing or out of order in:\n%s", want, strings.Join(calls, "\n"))
		}
		last = i
	}
	if indexOf(calls, "docker pull") >= 0 {
		t.Error("a Git deployment pulled an image")
	}
	if run := calls[indexOf(calls, "docker run")]; !strings.HasSuffix(run, " "+image) {
		t.Errorf("the container does not run the built image: %s", run)
	}
	// The checkout is removed once the image is built.
	if indexOf(calls, "rm-all "+work) < 0 {
		t.Error("the build directory was left on the server")
	}
	// The build-time secret reaches docker through its environment only.
	all := strings.Join(calls, "\n")
	if strings.Contains(all, "npm_s3cret") {
		t.Fatal("a build-time secret appeared on a command line")
	}
	if env := strings.Join(rec.envs["docker build"], "\n"); !strings.Contains(env, "NPM_TOKEN=npm_s3cret") || strings.Contains(env, "PORT=") {
		t.Fatalf("docker build environment: %q", rec.envs["docker build"])
	}
	// git never prompts and ignores configuration found on the server.
	if env := strings.Join(rec.envs["git clone"], "\n"); !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") || !strings.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("git environment: %q", rec.envs["git clone"])
	}
	// Builds on one server are serialised.
	var lock string
	e.db.QueryRow(`SELECT lock_key FROM jobs ORDER BY created_at DESC LIMIT 1`).Scan(&lock)
	if lock != "build:"+e.server.ID {
		t.Fatalf("lock key %q", lock)
	}
}

func TestGitHubAppTokenStaysOutOfCommandLines(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	tokens := &stubTokens{token: "ghs_SuperSecretInstallationToken"}
	e.d.Tokens = tokens

	src, _ := e.db.StartGitSource(ctx, e.team, "musdash-test", "state1")
	src.AppID = 777
	src.Slug = "musdash-test"
	src.PrivateKey, _ = e.d.Box.SealString("-----BEGIN RSA PRIVATE KEY-----\nMII\n-----END RSA PRIVATE KEY-----\n")
	if err := e.db.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	// The address was entered in SSH form; an App still clones over HTTPS.
	e.gitApp(func(a *db.App) { a.GitSourceID = src.ID; a.RepoURL = "git@github.com:acme/shop.git" })

	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if len(tokens.calls) != 1 || tokens.calls[0] != "777:acme/shop" {
		t.Fatalf("token requests: %v", tokens.calls)
	}
	calls := strings.Join(e.fake.Calls(), "\n")
	if !strings.Contains(calls, "-- https://github.com/acme/shop.git ") {
		t.Fatalf("clone address:\n%s", calls)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_SuperSecretInstallationToken"))
	for _, secret := range []string{"ghs_SuperSecretInstallationToken", encoded} {
		if strings.Contains(calls, secret) {
			t.Fatal("the token appeared on a command line")
		}
		if strings.Contains(e.log(dep), secret) {
			t.Fatal("the token appeared in the deployment log")
		}
	}
	env := strings.Join(rec.envs["git clone"], "\n")
	// Scoped to the host, so a redirect elsewhere does not carry the token.
	if !strings.Contains(env, "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader") || !strings.Contains(env, "GIT_CONFIG_VALUE_0=Authorization: Basic "+encoded) {
		t.Fatalf("git environment: %q", rec.envs["git clone"])
	}

	// An App without access to the repository gives a message that says so.
	tokens.err = source.ErrNotInstalled
	dep = e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "install it on that repository") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
}

func TestDeployKeyIsWrittenPrivateAndRemoved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	sealed, _ := e.d.Box.SealString("-----BEGIN OPENSSH PRIVATE KEY-----\nkeydata\n-----END OPENSSH PRIVATE KEY-----\n")
	key, err := e.db.CreateSSHKey(ctx, e.team, "shop deploy key", "ssh-ed25519 AAAA shop", sealed)
	if err != nil {
		t.Fatal(err)
	}
	e.gitApp(func(a *db.App) { a.SSHKeyID = key.ID; a.RepoURL = "git@git.example.com:acme/shop.git" })

	// Capture the key file while it exists.
	var keyBody string
	var keyMode fs.FileMode
	inner := rec.handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "git clone") {
			for _, kv := range c.Env {
				if path, ok := strings.CutPrefix(kv, "GIT_SSH_COMMAND=ssh -i "); ok {
					keyBody, keyMode, _ = e.fake.File(strings.Fields(path)[0])
				}
			}
		}
		return inner(line, c)
	}
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if !strings.Contains(keyBody, "keydata") || keyMode != 0o600 {
		t.Fatalf("key file during the clone: mode %o, %d bytes", keyMode, len(keyBody))
	}
	keyPath := filepath.Join(e.cfg.WorkDir(), dep.ID, "deploy-key")
	if _, _, still := e.fake.File(keyPath); still {
		t.Fatal("the deploy key was left on the server after the build")
	}
	env := strings.Join(rec.envs["git clone"], "\n")
	for _, want := range []string{"-i " + keyPath, "IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=accept-new"} {
		if !strings.Contains(env, want) {
			t.Errorf("GIT_SSH_COMMAND is missing %q: %s", want, env)
		}
	}
	if strings.Contains(strings.Join(e.fake.Calls(), "\n"), "keydata") {
		t.Fatal("key material appeared on a command line")
	}

	// An SSH address with no key cannot be cloned; say why.
	e.gitApp(func(a *db.App) { a.SSHKeyID = ""; a.RepoURL = "git@git.example.com:acme/shop.git" })
	if dep := e.deploy(); dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "choose a deploy key") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
}

func TestFailedBuildKeepsOldContainerAndCleansUp(t *testing.T) {
	e := newEnv(t)
	first := e.deploy() // an image deployment that is serving
	before := e.reload()
	rec := &gitEnvRecorder{failWith: map[string]error{"docker build": runnertest.Exit("docker", 1, "")}}
	e.fake.Handle = rec.handle
	e.gitApp(nil)

	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.HasPrefix(dep.Error, "build:") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	after := e.reload()
	if after.Container != before.Container || after.Status != db.AppRunning {
		t.Fatalf("the previous container must keep serving: %+v", after)
	}
	calls := e.fake.Calls()
	if indexOf(calls, "rm-all "+filepath.Join(e.cfg.WorkDir(), dep.ID)) < 0 {
		t.Error("the build directory was left behind after a failed build")
	}
	for _, c := range calls {
		if c == "docker stop --time 30 "+ContainerName(e.app.ID, first.ID) {
			t.Fatal("the serving container was stopped by a failed build")
		}
	}
}

func TestBuildRefusesSymlinksOutOfTheRepository(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{links: map[string]bool{"Dockerfile": true}}
	e.fake.Handle = rec.handle
	e.gitApp(nil)
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "Dockerfile is a symbolic link") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if indexOf(e.fake.Calls(), "docker build") >= 0 {
		t.Fatal("a build ran with a symlinked Dockerfile")
	}

	// A directory on the way to the build context.
	rec.links = map[string]bool{"apps": true}
	e.gitApp(func(a *db.App) { a.BaseDir = "apps/web" })
	if dep := e.deploy(); dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "apps is a symbolic link") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
}

func TestStaticBuildPack(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.gitApp(func(a *db.App) { a.BuildPack = PackStatic; a.PublishDir = "dist"; a.SPAFallback = true })

	var dockerfile string
	inner := rec.handle
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker build") {
			for i, a := range c.Args {
				if a == "--file" {
					dockerfile, _, _ = e.fake.File(c.Args[i+1])
				}
			}
		}
		return inner(line, c)
	}
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	for _, want := range []string{"FROM nginx:alpine", "COPY dist/ /usr/share/nginx/html/", "try_files $uri $uri/ /index.html;"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("generated Dockerfile is missing %q:\n%s", want, dockerfile)
		}
	}
	if plain := StaticDockerfile("", false); !strings.Contains(plain, "COPY ./ /usr/share/nginx/html/") || strings.Contains(plain, "try_files") {
		t.Errorf("plain static Dockerfile:\n%s", plain)
	}
}

func TestOldImagesArePruned(t *testing.T) {
	e := newEnv(t)
	current := testCommit[:12]
	rec := &gitEnvRecorder{images: current + "\nt1\nt2\nt3\nt4\nt5\nt6\nt7\n"}
	e.fake.Handle = rec.handle
	e.gitApp(nil)
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	var removed []string
	for _, c := range e.fake.Calls() {
		if ref, ok := strings.CutPrefix(c, "docker rmi "); ok {
			removed = append(removed, strings.TrimPrefix(ref, ImageRepository(e.app.ID)+":"))
		}
	}
	// The newest five stay (the current one among them); the rest go.
	if strings.Join(removed, ",") != "t5,t6,t7" {
		t.Fatalf("removed %v, want t5 t6 t7", removed)
	}
}

func TestStalledCloneAndBuildAreStopped(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.d.cloneTimeout, e.d.buildTimeout = 100*time.Millisecond, 100*time.Millisecond
	e.gitApp(nil)

	e.fake.Hang = func(line string) bool { return strings.HasPrefix(line, "git clone") }
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "clone acme/shop: stopped after 100ms") {
		t.Fatalf("stalled clone: %s %q", dep.Status, dep.Error)
	}

	e.fake.Hang = func(line string) bool { return strings.HasPrefix(line, "docker build") }
	dep = e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "build: stopped after 100ms") {
		t.Fatalf("stalled build: %s %q", dep.Status, dep.Error)
	}
	// The server's build lock is free again: the next deployment runs.
	e.fake.Hang = nil
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("after a stalled build: %s %q", dep.Status, dep.Error)
	}
}

func TestGitHubAppTokenOnlyGoesToGitHub(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	tokens := &stubTokens{token: "ghs_secret"}
	e.d.Tokens = tokens
	key, _ := e.d.Box.SealString("pem")
	ctx := context.Background()
	pending, err := e.db.StartGitSource(ctx, e.team, "musdash-test", "state")
	if err != nil {
		t.Fatal(err)
	}
	pending.AppID, pending.PrivateKey = 777, key
	if err := e.db.FinishGitSource(ctx, pending); err != nil {
		t.Fatal(err)
	}
	// Saved by some other path than the form, which only accepts github.com.
	e.gitApp(func(a *db.App) { a.RepoURL = "https://git.evil.test/acme/shop"; a.GitSourceID = pending.ID })
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "only read repositories on github.com") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if len(tokens.calls) != 0 || indexOf(e.fake.Calls(), "git clone") >= 0 {
		t.Fatal("a token was minted, or a clone started, for a host that is not GitHub")
	}
}

func TestDestroyRemovesBuiltImages(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{images: testCommit[:12] + "\nolder1\n"}
	e.fake.Handle = rec.handle
	e.gitApp(nil)
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if err := e.d.Destroy(context.Background(), e.app.ID); err != nil {
		t.Fatal(err)
	}
	repo := ImageRepository(e.app.ID)
	for _, tag := range []string{testCommit[:12], "older1"} {
		if indexOf(e.fake.Calls(), "docker rmi "+repo+":"+tag) < 0 {
			t.Errorf("image %s was left on the server", tag)
		}
	}
}

func TestRunOptionsAndStartCommand(t *testing.T) {
	e := newEnv(t)
	e.db.Exec(`UPDATE apps SET docker_options = '--shm-size 256m --init', start_command = 'node server.js --port $PORT' WHERE id = ?`, e.app.ID)
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	calls := e.fake.Calls()
	run := calls[indexOf(calls, "docker run")]
	if !strings.Contains(run, " --shm-size 256m --init nginx:alpine sh -c node server.js --port $PORT") {
		t.Fatalf("docker run: %s", run)
	}

	// A refused option fails the deployment before anything is pulled.
	e.db.Exec(`UPDATE apps SET docker_options = '--privileged' WHERE id = ?`, e.app.ID)
	before := len(e.fake.Calls())
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "--privileged is not allowed") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.HasPrefix(c, "docker pull") || strings.HasPrefix(c, "docker run") {
			t.Fatalf("work was done with a refused option: %s", c)
		}
	}
}

// gitOnly runs git for real and scripts everything else.
type gitOnly struct {
	*runnertest.Fake
	local runner.Runner
}

func (g gitOnly) Run(ctx context.Context, c runner.Cmd) error {
	if c.Name == "git" {
		return g.local.Run(ctx, c)
	}
	return g.Fake.Run(ctx, c)
}

func (g gitOnly) Output(ctx context.Context, c runner.Cmd) ([]byte, error) {
	if c.Name == "git" {
		return g.local.Output(ctx, c)
	}
	return g.Fake.Output(ctx, c)
}

func (g gitOnly) WriteFile(ctx context.Context, p string, m fs.FileMode, r io.Reader) error {
	return g.local.WriteFile(ctx, p, m, r)
}
func (g gitOnly) MkdirAll(ctx context.Context, p string, m fs.FileMode) error {
	return g.local.MkdirAll(ctx, p, m)
}
func (g gitOnly) RemoveAll(ctx context.Context, p string) error { return g.local.RemoveAll(ctx, p) }

// makeRepo creates a Git repository with the given files and returns its
// directory. Addresses under https://git.test/ are redirected to its parent.
func makeRepo(t *testing.T, name string, files map[string]string) (dir string, gitEnv []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	dir = filepath.Join(root, name+".git")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(dir, 0o755)
	git("init", "--quiet", "--initial-branch", "main")
	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if target, ok := strings.CutPrefix(content, "symlink:"); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		os.WriteFile(full, []byte(content), 0o644)
	}
	git("add", "--all")
	git("commit", "--quiet", "--message", "first")
	return dir, []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.file://" + root + "/.insteadOf",
		"GIT_CONFIG_VALUE_0=https://git.test/acme/",
	}
}

func TestCloneWithRealGit(t *testing.T) {
	e := newEnv(t)
	repo, gitEnv := makeRepo(t, "shop", map[string]string{"Dockerfile": "FROM scratch\n", "index.html": "hello"})
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.d.Runners = fixedRunners{gitOnly{Fake: e.fake, local: runner.NewLocal()}}
	e.d.extraGitEnv = gitEnv
	e.gitApp(func(a *db.App) { a.RepoURL = "https://git.test/acme/shop.git" })

	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))

	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	if dep.CommitSHA != commit || dep.Image != ImageRepository(e.app.ID)+":"+commit[:12] {
		t.Fatalf("built commit %q image %q, want %s", dep.CommitSHA, dep.Image, commit)
	}
	// The checkout existed for the build and is gone afterwards.
	if _, err := os.Stat(filepath.Join(e.cfg.WorkDir(), dep.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the build directory still exists: %v", err)
	}

	// A branch that does not exist fails with git's message in the log.
	e.gitApp(func(a *db.App) { a.RepoURL = "https://git.test/acme/shop.git"; a.Branch = "no-such-branch" })
	dep = e.deploy()
	if dep.Status != db.DeployFailed || !strings.HasPrefix(dep.Error, "clone acme/shop") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if !strings.Contains(e.log(dep), "no-such-branch") {
		t.Fatalf("git's explanation is not in the log:\n%s", e.log(dep))
	}
}

func TestRealSymlinkElsewhereIsAllowed(t *testing.T) {
	e := newEnv(t)
	// A link that is neither the Dockerfile nor on the way to it, as in a
	// monorepo that shares a LICENSE file.
	_, gitEnv := makeRepo(t, "mono", map[string]string{
		"LICENSE": "MIT", "apps/web/Dockerfile": "FROM scratch\n", "apps/web/LICENSE": "symlink:../../LICENSE",
	})
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.d.Runners = fixedRunners{gitOnly{Fake: e.fake, local: runner.NewLocal()}}
	e.d.extraGitEnv = gitEnv
	e.gitApp(func(a *db.App) { a.RepoURL = "https://git.test/acme/mono.git"; a.BaseDir = "apps/web" })
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
}

func TestRealSymlinkIsRefused(t *testing.T) {
	e := newEnv(t)
	// The repository's Dockerfile is a link to a file on the server.
	_, gitEnv := makeRepo(t, "evil", map[string]string{"Dockerfile": "symlink:" + e.cfg.MasterKeyPath(), "app.txt": "x"})
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.d.Runners = fixedRunners{gitOnly{Fake: e.fake, local: runner.NewLocal()}}
	e.d.extraGitEnv = gitEnv
	e.gitApp(func(a *db.App) { a.RepoURL = "https://git.test/acme/evil.git" })
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "symbolic link") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if indexOf(e.fake.Calls(), "docker build") >= 0 {
		t.Fatal("a build ran with a Dockerfile that links outside the repository")
	}
}

// TestGitDeployWithDocker builds and runs a real image from a real local
// repository, then deploys a second commit. It needs Docker:
//
//	MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestGitDeployWithDocker -v
func TestGitDeployWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	e := newEnv(t)
	repo, gitEnv := makeRepo(t, "site", map[string]string{"public/index.html": "<h1>version one</h1>\n"})
	local := runner.NewLocal()
	e.d.Runners = fixedRunners{local}
	e.d.Probe = newLocalProbe()
	e.d.extraGitEnv = gitEnv
	e.d.healthEvery = 300 * time.Millisecond
	e.db.Exec(`UPDATE apps SET health_timeout = 60, health_path = '/' WHERE id = ?`, e.app.ID)
	e.gitApp(func(a *db.App) {
		a.RepoURL = "https://git.test/acme/site.git"
		a.BuildPack = PackStatic
		a.PublishDir = "public"
		a.SPAFallback = true
	})

	dk := docker.Client{R: local}
	t.Cleanup(func() {
		ctx := context.Background()
		out, _ := local.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"ps", "--all", "--quiet", "--filter", "label=" + docker.LabelResource + "=" + e.app.ID}})
		for _, id := range strings.Fields(string(out)) {
			dk.Remove(ctx, id)
		}
		tags, _ := dk.ImageTags(ctx, ImageRepository(e.app.ID))
		for _, tag := range tags {
			dk.RemoveImage(ctx, ImageRepository(e.app.ID)+":"+tag)
		}
		local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"network", "rm", NetworkName(e.app.EnvironmentID)}})
	})

	fetch := func(path string) string {
		t.Helper()
		res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(e.reload().HostPort) + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	first := e.deployWithin(5 * time.Minute)
	if first.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", first.Status, first.Error, e.log(first))
	}
	if got := fetch("/"); !strings.Contains(got, "version one") {
		t.Fatalf("served %q", got)
	}
	// With the single-page-app fallback, an unknown path gets index.html.
	if got := fetch("/orders/42"); !strings.Contains(got, "version one") {
		t.Fatalf("the fallback did not serve index.html for a deep path: %q", got)
	}

	// A new commit, then a redeploy.
	os.WriteFile(filepath.Join(repo, "public/index.html"), []byte("<h1>version two</h1>\n"), 0o644)
	for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "--message", "second"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	second := e.deployWithin(5 * time.Minute)
	if second.Status != db.DeploySuccess || second.CommitSHA == first.CommitSHA {
		t.Fatalf("%s %q (commit %s)", second.Status, second.Error, second.CommitSHA)
	}
	if got := fetch("/"); !strings.Contains(got, "version two") {
		t.Fatalf("after the second deploy, served %q", got)
	}
	t.Logf("built and served two commits: %s then %s", first.CommitSHA[:12], second.CommitSHA[:12])
}
