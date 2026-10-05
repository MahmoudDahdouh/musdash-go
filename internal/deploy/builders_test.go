package deploy

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

// builderServer scripts a server for a build with Nixpacks or Railpack.
type builderServer struct {
	git gitEnvRecorder
	e   *env
	// plan is what the builder prints as its plan; planErr what it fails
	// with instead. arch is what Docker says the server is.
	plan    string
	planErr error
	arch    string
	// hasImage says the builder's image is already on the server.
	hasImage bool

	mu       sync.Mutex
	envs     map[string][]string // the whole command line → its environment
	stdin    map[string]string   // the command line of a build from a stream → the stream
	envFiles []string            // the variables file as each builder run found it
}

func (b *builderServer) handle(line string, c runner.Cmd) (string, error) {
	b.mu.Lock()
	if b.envs == nil {
		b.envs, b.stdin = map[string][]string{}, map[string]string{}
	}
	b.envs[line] = c.Env
	b.mu.Unlock()
	switch {
	case strings.HasPrefix(line, "docker image inspect --format {{.Id}} musdash/nixpacks:"),
		strings.HasPrefix(line, "docker image inspect --format {{.Id}} musdash/railpack:"):
		if !b.hasImage {
			return "", runnertest.Exit("docker", 1, "No such image")
		}
		return "sha256:abc\n", nil
	case line == "docker version --format {{.Server.Arch}}":
		return b.arch + "\n", nil
	case line == "id -u", line == "id -g":
		return "1000\n", nil
	case strings.HasPrefix(line, "docker build") && c.Stdin != nil:
		raw, _ := io.ReadAll(c.Stdin)
		b.mu.Lock()
		b.stdin[line] = string(raw)
		b.mu.Unlock()
		return "", nil
	case strings.HasPrefix(line, "docker run --rm --name musdash-plan-"):
		for i, a := range c.Args {
			if a == "--env-file" {
				body, _, _ := b.e.fake.File(c.Args[i+1])
				b.mu.Lock()
				b.envFiles = append(b.envFiles, body)
				b.mu.Unlock()
			}
		}
		if strings.Contains(line, " nixpacks build ") {
			return "nixpacks summary\n", nil
		}
		return b.plan, b.planErr
	}
	return b.git.handle(line, c)
}

func (b *builderServer) envOf(prefix string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for line, env := range b.envs {
		if strings.HasPrefix(line, prefix) {
			return strings.Join(env, "\n")
		}
	}
	return ""
}

const nixpacksPlan = `{"providers":[],"buildImage":"ghcr.io/railwayapp/nixpacks:ubuntu-1745885067",
	"variables":{"CI":"true","NODE_ENV":"production","NPM_TOKEN":"npm_s3cret","DOCKER_HOST":"tcp://203.0.113.9:2375","COUNT":3},
	"phases":{"install":{"cmds":["npm i"]}},"start":{"cmd":"npm run start"}}`

func builderEnv(t *testing.T, pack string) (*env, *builderServer) {
	e := newEnv(t)
	b := &builderServer{e: e, arch: "arm64", plan: nixpacksPlan}
	e.fake.Handle = b.handle
	sealed, _ := e.d.Box.SealString("npm_s3cret")
	runtime, _ := e.d.Box.SealString("runtime-only")
	e.db.ReplaceEnvVars(context.Background(), db.KindApp, e.app.ID, []db.EnvVar{
		{Key: "NPM_TOKEN", Value: sealed, BuildTime: true}, {Key: "GREETING", Value: runtime},
	})
	e.db.Exec(`UPDATE apps SET port = 3000 WHERE id = ?`, e.app.ID)
	e.gitApp(func(a *db.App) { a.BuildPack = pack; a.BaseDir = "apps/web" })
	return e, b
}

func TestNixpacksBuild(t *testing.T) {
	e, b := builderEnv(t, PackNixpacks)
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	calls := e.fake.Calls()
	all := strings.Join(calls, "\n")
	work := filepath.Join(e.cfg.WorkDir(), dep.ID)
	src := filepath.Join(work, "src", "apps/web")
	image := ImageRepository(e.app.ID) + ":" + testCommit[:12]

	// The builder's image is made once, from the release file of the
	// server's architecture and no other, checked against its checksum.
	setup := "docker build --progress plain --tag musdash/nixpacks:1.41.0 --file Dockerfile --label musdash.managed=true -- -"
	if indexOf(calls, setup) < 0 {
		t.Fatalf("the builder's image was not built:\n%s", all)
	}
	dockerfile := b.stdin[setup]
	for _, want := range []string{"nixpacks-v1.41.0-aarch64-unknown-linux-musl.tar.gz", "--checksum=sha256:" + builders[PackNixpacks].assets["arm64"].sum} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the builder's Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(dockerfile, "x86_64") {
		t.Error("the builder's Dockerfile names another architecture's file")
	}

	// Both runs of the builder are shut in.
	// The app is shown read-only both times; the run that writes gets one
	// directory to write to, where its Dockerfile is expected.
	sandbox := "docker run --rm --name musdash-plan-" + dep.ID + " --read-only --cap-drop ALL --security-opt no-new-privileges --memory 1g --pids-limit 256 --user 1000:1000 --workdir /tmp" +
		" --env-file " + work + "/builder.env --env HOME=/tmp --network none --tmpfs /tmp --mount type=bind,source=" + src + ",target=/src,readonly"
	plan := sandbox + " musdash/nixpacks:1.41.0 nixpacks plan /src --format json --env NPM_TOKEN"
	write := sandbox + " --mount type=bind,source=" + src + "/.nixpacks,target=/src/.nixpacks musdash/nixpacks:1.41.0 nixpacks build /src --out /src --env NPM_TOKEN"
	build := "docker build --progress plain --tag " + image + " --file " + src + "/.nixpacks/Dockerfile --label musdash.managed=true" +
		" --build-arg NPM_TOKEN --build-arg CI=true --build-arg DOCKER_HOST=tcp://203.0.113.9:2375 --build-arg NODE_ENV=production -- " + src
	last := -1
	for _, want := range []string{
		"git -C " + work + "/src ls-tree HEAD -- apps/web",
		"git -C " + work + "/src ls-tree HEAD -- apps/web/.nixpacks",
		"git -C " + work + "/src ls-tree HEAD -- apps/web/.nixpacks/Dockerfile",
		setup, plan, write, "sh -c test ! -L", build, "docker run --detach",
	} {
		i := indexOf(calls, want)
		if i <= last {
			t.Fatalf("%q is missing or out of order in:\n%s", want, all)
		}
		last = i
	}
	// The app's secret is named to the builder, and its value is nowhere
	// on a command line: it is in the variables file and in the
	// environment of the build.
	if strings.Contains(all, "npm_s3cret") {
		t.Fatal("a build-time secret appeared on a command line")
	}
	if len(b.envFiles) != 2 || b.envFiles[0] != "NPM_TOKEN=npm_s3cret\n" {
		t.Fatalf("the builder's variables file: %q", b.envFiles)
	}
	if _, mode, ok := e.fake.File(work + "/builder.env"); ok {
		t.Fatalf("the variables file was left on the server (mode %o)", mode)
	}
	env := b.envOf(build)
	if !strings.Contains(env, "NPM_TOKEN=npm_s3cret") || !strings.Contains(env, "BUILDX_GIT_INFO=0") {
		t.Fatalf("the build's environment: %q", env)
	}
	// A builder that ended well removed its own container.
	if indexOf(calls, "docker rm --force musdash-plan-") >= 0 {
		t.Error("the builder's container was removed by hand after a run that ended well")
	}
	// A variable the repository named is an argument of the build, never
	// an environment variable of the docker command.
	if strings.Contains(env, "DOCKER_HOST") || strings.Contains(env, "NODE_ENV") {
		t.Fatalf("a variable from the plan reached docker's own environment: %q", env)
	}
	// The app is told where to listen.
	if body, _, _ := e.fake.File(filepath.Join(e.cfg.AppDir(e.app.ID), "env")); !strings.Contains(body, "PORT=3000\n") || !strings.Contains(body, "GREETING=runtime-only\n") || strings.Contains(body, "NPM_TOKEN") {
		t.Fatalf("the container's variables: %q", body)
	}

	// A second deployment finds the builder's image there.
	b.hasImage = true
	e.fake.Handle = b.handle
	before := len(e.fake.Calls())
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if again := e.fake.Calls()[before:]; indexOf(again, "docker build --progress plain --tag musdash/nixpacks") >= 0 || indexOf(again, "docker version") >= 0 {
		t.Fatal("the builder's image was built again")
	}
}

func TestAnAppsOwnPORTStands(t *testing.T) {
	e, _ := builderEnv(t, PackNixpacks)
	own, _ := e.d.Box.SealString("8080")
	e.db.ReplaceEnvVars(context.Background(), db.KindApp, e.app.ID, []db.EnvVar{{Key: "PORT", Value: own}})
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if body, _, _ := e.fake.File(filepath.Join(e.cfg.AppDir(e.app.ID), "env")); body != "PORT=8080\n" {
		t.Fatalf("the container's variables: %q", body)
	}
}

func TestBuilderFailures(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(*builderServer)
		want   string
	}{
		"a plan that is not JSON": {func(b *builderServer) { b.plan = "Nixpacks build plan:\n setup | nodejs" }, "cannot be read"},
		"a builder that gives up": {func(b *builderServer) {
			b.planErr = runnertest.Exit("docker", 1, "Error: Nixpacks was unable to generate a build plan for this app.")
		}, "unable to generate a build plan"},
		"an architecture without a release": {func(b *builderServer) { b.arch = "riscv64" }, "no release for this server's architecture"},
		"an architecture that is a path":    {func(b *builderServer) { b.arch = "../../x86_64" }, "no release for this server's architecture"},
		"the generated directory is a link": {func(b *builderServer) { b.git.links = map[string]bool{"apps/web/.nixpacks": true} }, "symbolic link"},
		"the base directory is a link":      {func(b *builderServer) { b.git.links = map[string]bool{"apps/web": true} }, "symbolic link"},
		"no Dockerfile was written": {func(b *builderServer) {
			b.git.failWith = map[string]error{"sh -c test ! -L": runnertest.Exit("sh", 1, "")}
		}, "did not leave a Dockerfile"},
		"a variable name that is no name": {func(b *builderServer) {
			b.plan = `{"variables":{"--network=host":"x"}}`
		}, "is not valid"},
		"very many variables": {func(b *builderServer) {
			b.plan = `{"variables":{` + strings.TrimSuffix(strings.Repeat(`"V":"x",`, 1), ",") + manyVars(300) + `}}`
		}, "at most 200"},
	} {
		e, b := builderEnv(t, PackNixpacks)
		c.mutate(b)
		dep := e.deploy()
		if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, c.want) {
			t.Errorf("%s: %s %q", name, dep.Status, dep.Error)
		}
		calls := e.fake.Calls()
		if i := indexOf(calls, "docker run --detach"); i >= 0 {
			t.Errorf("%s: a container was started", name)
		}
		// A builder that failed may have left its container running.
		if name == "a builder that gives up" && indexOf(calls, "docker rm --force musdash-plan-"+dep.ID) < 0 {
			t.Errorf("%s: its container was not removed", name)
		}
		if indexOf(calls, "rm-all "+filepath.Join(e.cfg.WorkDir(), dep.ID)) < 0 {
			t.Errorf("%s: the build directory was left on the server", name)
		}
	}
}

func manyVars(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`,"V` + strings.Repeat("x", i%9) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + `":"1"`)
	}
	return b.String()
}

func TestRailpackBuild(t *testing.T) {
	e, b := builderEnv(t, PackRailpack)
	b.plan = `{"$schema":"https://schema.railpack.com","deploy":{"startCommand":"npm run start"},"secrets":["NPM_TOKEN"],"steps":[]}` + "\n"
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	calls := e.fake.Calls()
	all := strings.Join(calls, "\n")
	work := filepath.Join(e.cfg.WorkDir(), dep.ID)
	src := filepath.Join(work, "src", "apps/web")
	image := ImageRepository(e.app.ID) + ":" + testCommit[:12]

	setup := "docker build --progress plain --tag musdash/railpack:0.40.1 --file Dockerfile --label musdash.managed=true -- -"
	if !strings.Contains(b.stdin[setup], "railpack-v0.40.1-arm64-unknown-linux-musl.tar.gz") {
		t.Fatalf("the builder's Dockerfile: %q", b.stdin[setup])
	}
	// Railpack asks the network which versions exist; everything else of
	// its sandbox is as closed as Nixpacks's, and it only reads the app.
	prepare := "docker run --rm --name musdash-plan-" + dep.ID + " --read-only --cap-drop ALL --security-opt no-new-privileges --memory 1g --pids-limit 256 --user 1000:1000 --workdir /tmp" +
		" --env-file " + work + "/builder.env --env HOME=/tmp --tmpfs /tmp:rw,exec,nosuid,size=512m --mount type=bind,source=" + src + ",target=/src,readonly" +
		" musdash/railpack:0.40.1 sh -c " + railpackPrepare + " sh --env NPM_TOKEN"
	if indexOf(calls, prepare) < 0 {
		t.Fatalf("the plan was not made as expected:\n%s", all)
	}
	if strings.Contains(all, "--network none") {
		t.Error("Railpack was run without a network, which it cannot plan without")
	}
	hash := regexp.MustCompile(`secrets-hash=([0-9a-f]{64}) `).FindStringSubmatch(all)
	if hash == nil {
		t.Fatalf("no secrets hash on the build:\n%s", all)
	}
	build := "docker build --progress plain --tag " + image + " --file " + work + "/railpack-plan.json --label musdash.managed=true" +
		" --build-arg BUILDKIT_SYNTAX=ghcr.io/railwayapp/railpack-frontend:v0.40.1@" + railpackFrontendDigest + " --build-arg cache-key=" + e.app.ID +
		" --build-arg secrets-hash=" + hash[1] + " --secret id=NPM_TOKEN,env=NPM_TOKEN -- " + src
	if indexOf(calls, build) < 0 {
		t.Fatalf("the build was not started as expected:\n%s", all)
	}
	if strings.Contains(all, "npm_s3cret") {
		t.Fatal("a build-time secret appeared on a command line")
	}
	if env := b.envOf(build); !strings.Contains(env, "NPM_TOKEN=npm_s3cret") {
		t.Fatalf("the build's environment: %q", env)
	}
	// Another value of the secret is another build.
	if secretsHash(map[string]string{"NPM_TOKEN": "npm_s3cret"}) != hash[1] || secretsHash(map[string]string{"NPM_TOKEN": "other"}) == hash[1] ||
		secretsHash(map[string]string{"A": "b=c"}) == secretsHash(map[string]string{"A=b": "c"}) {
		t.Fatal("the secrets hash does not follow the secrets")
	}

	e2, b2 := builderEnv(t, PackRailpack)
	b2.plan = "not a plan"
	if dep := e2.deploy(); dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "cannot be read") {
		t.Fatalf("a plan that is not JSON: %s %q", dep.Status, dep.Error)
	}
}

func TestBuildSpecForBuilders(t *testing.T) {
	base := docker.BuildSpec{Tag: "musdash/a:t", ContextDir: "/w/src", Dockerfile: "/w/src/Dockerfile"}
	for name, mutate := range map[string]func(*docker.BuildSpec){
		"a frontend that is a flag":      func(s *docker.BuildSpec) { s.Frontend = "--output=/" },
		"a frontend option with a value": func(s *docker.BuildSpec) { s.FrontendArgs = map[string]string{"cache-key": "a b"} },
		"a frontend option as a flag":    func(s *docker.BuildSpec) { s.FrontendArgs = map[string]string{"--network": "host"} },
		"a frontend option in capitals":  func(s *docker.BuildSpec) { s.FrontendArgs = map[string]string{"DOCKER_HOST": "x"} },
		"a secret the tools read":        func(s *docker.BuildSpec) { s.Secrets = map[string]string{"DOCKER_HOST": "x"} },
		"a secret with a comma":          func(s *docker.BuildSpec) { s.Secrets = map[string]string{"A,src=/etc/shadow": "x"} },
		"a plain argument as a flag":     func(s *docker.BuildSpec) { s.PlainArgs = map[string]string{"--network=host": "x"} },
		"a plain argument with an =":     func(s *docker.BuildSpec) { s.PlainArgs = map[string]string{"A=B": "x"} },
		"a huge plain argument":          func(s *docker.BuildSpec) { s.PlainArgs = map[string]string{"A": strings.Repeat("x", 9<<10)} },
		"a stream and a directory":       func(s *docker.BuildSpec) { s.Context = strings.NewReader("x"); s.Dockerfile = "Dockerfile" },
		"a stream with an absolute file": func(s *docker.BuildSpec) { s.Context = strings.NewReader("x"); s.ContextDir = "" },
		"a stream with a file as a flag": func(s *docker.BuildSpec) {
			s.Context = strings.NewReader("x")
			s.ContextDir, s.Dockerfile = "", "--output=x"
		},
		"a stream with a file outside": func(s *docker.BuildSpec) {
			s.Context = strings.NewReader("x")
			s.ContextDir, s.Dockerfile = "", "../Dockerfile"
		},
	} {
		s := base
		mutate(&s)
		if cm, err := s.Cmd(); err == nil {
			t.Errorf("%s: accepted as %q", name, cm.Args)
		}
	}
	// A name the docker command reads may be a build argument with its
	// value written out: that is the point of writing it out.
	s := base
	s.PlainArgs = map[string]string{"DOCKER_HOST": "tcp://x", "A": "from the plan"}
	s.BuildArgs = map[string]string{"A": "the app's own"}
	cm, err := s.Cmd()
	if err != nil {
		t.Fatal(err)
	}
	if args, env := strings.Join(cm.Args, " "), strings.Join(cm.Env, "\n"); !strings.Contains(args, "--build-arg DOCKER_HOST=tcp://x") ||
		strings.Contains(env, "DOCKER_HOST") || strings.Contains(args, "from the plan") || !strings.Contains(env, "A=the app's own") {
		t.Fatalf("args %q env %q", args, env)
	}
}

// TestBuildersWithDocker builds a small app that has no Dockerfile with
// the real Nixpacks and the real Railpack, and asks the running container
// what it was given. It needs Docker and the network, and takes minutes:
//
//	MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestBuildersWithDocker -v -timeout 40m
func TestBuildersWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	for _, pack := range []string{PackNixpacks, PackRailpack} {
		t.Run(pack, func(t *testing.T) {
			e := newEnv(t)
			repo, gitEnv := makeRepo(t, "hello", map[string]string{
				"package.json": `{"name":"hello","version":"1.0.0","scripts":{"start":"node server.js"}}` + "\n",
				"server.js":    `require("http").createServer((q, s) => s.end("hello " + process.env.GREETING + " on " + process.env.PORT + "\n")).listen(process.env.PORT)` + "\n",
			})
			_ = repo
			local := runner.NewLocal()
			e.d.Runners = fixedRunners{local}
			e.d.Probe = newLocalProbe()
			e.d.extraGitEnv = gitEnv
			e.d.healthEvery = 300 * time.Millisecond
			greeting, _ := e.d.Box.SealString("world")
			note, _ := e.d.Box.SealString("a build secret")
			e.db.ReplaceEnvVars(context.Background(), db.KindApp, e.app.ID, []db.EnvVar{
				{Key: "GREETING", Value: greeting}, {Key: "BUILD_NOTE", Value: note, BuildTime: true},
			})
			e.db.Exec(`UPDATE apps SET port = 3000, health_timeout = 90, health_path = '/' WHERE id = ?`, e.app.ID)
			e.gitApp(func(a *db.App) {
				a.RepoURL = "https://git.test/acme/hello.git"
				a.BuildPack = pack
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

			dep := e.deployWithin(30 * time.Minute)
			if dep.Status != db.DeploySuccess {
				t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
			}
			res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(e.reload().HostPort) + "/")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if string(body) != "hello world on 3000\n" {
				t.Fatalf("served %q\n%s", body, e.log(dep))
			}
			// Nothing of the build is left on the server, and the secret
			// is not in the log.
			if left, _ := os.ReadDir(e.cfg.WorkDir()); len(left) != 0 {
				t.Errorf("%d entries left in the work directory", len(left))
			}
			if strings.Contains(e.log(dep), "a build secret") {
				t.Error("the build secret is in the deployment log")
			}
		})
	}
}
