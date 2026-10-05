package docker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

func TestValidImage(t *testing.T) {
	good := []string{
		"nginx", "nginx:1.29-alpine", "library/nginx", "ghcr.io/acme/worker:4f2a91c",
		"registry.example.com:5000/team/app:v1.2.3", "postgres:17", "localhost:5000/app",
		"nginx@sha256:" + strings.Repeat("a", 64), "a/b/c/d:tag_1",
	}
	bad := []string{
		"", "-nginx", "--privileged", "nginx --privileged", "nginx;rm -rf /", "nginx:", "nginx:-tag",
		"NGINX/App", "nginx\n", "$(id)", "nginx:tag with space", "a//b", "nginx@sha256:short", "/nginx",
		strings.Repeat("a", 300),
	}
	for _, ref := range good {
		if !ValidImage(ref) {
			t.Errorf("rejected %q", ref)
		}
	}
	for _, ref := range bad {
		if ValidImage(ref) {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestRunArgs(t *testing.T) {
	spec := RunSpec{
		Name: "musdash-app1-dep1", Image: "nginx:alpine", Network: "musdash-env1", Alias: "web",
		HostPort: 20417, ContainerPort: 80, EnvFile: "/var/lib/musdash/apps/app1/env",
		MemoryMB: 256, CPUs: 0.5,
		Mounts: []Mount{
			{Kind: MountVolume, Source: "musdash-app1-data", Target: "/data"},
			{Kind: MountBind, Source: "/srv/files", Target: "/files", ReadOnly: true},
		},
		Labels: map[string]string{ManagedLabel: "true", LabelResource: "app1"},
	}
	got, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--detach", "--name", "musdash-app1-dep1", "--restart", "unless-stopped",
		"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--network", "musdash-env1", "--network-alias", "web",
		"--publish", "127.0.0.1:20417:80",
		"--env-file", "/var/lib/musdash/apps/app1/env",
		"--memory", "256m", "--cpus", "0.5",
		"--mount", "type=volume,source=musdash-app1-data,target=/data",
		"--mount", "type=bind,source=/srv/files,target=/files,readonly",
		"--label", "musdash.managed=true", "--label", "musdash.resource=app1",
		"nginx:alpine",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args:\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsRejectInjection(t *testing.T) {
	base := func() RunSpec {
		return RunSpec{Name: "c", Image: "nginx", HostPort: 20000, ContainerPort: 80}
	}
	cases := map[string]func(*RunSpec){
		"image as flag":          func(s *RunSpec) { s.Image = "--privileged" },
		"image with extra arg":   func(s *RunSpec) { s.Image = "nginx -v /:/host" },
		"name as flag":           func(s *RunSpec) { s.Name = "-v" },
		"network as flag":        func(s *RunSpec) { s.Network = "--net=host" },
		"alias with space":       func(s *RunSpec) { s.Network = "n"; s.Alias = "a b" },
		"mount target adds opt":  func(s *RunSpec) { s.Mounts = []Mount{{Kind: MountVolume, Source: "v", Target: "/data,readonly"}} },
		"bind source adds opt":   func(s *RunSpec) { s.Mounts = []Mount{{Kind: MountBind, Source: "/a,target=/etc", Target: "/x"}} },
		"relative bind source":   func(s *RunSpec) { s.Mounts = []Mount{{Kind: MountBind, Source: "relative", Target: "/x"}} },
		"bind source escapes":    func(s *RunSpec) { s.Mounts = []Mount{{Kind: MountBind, Source: "/srv/../etc", Target: "/x"}} },
		"volume name is a path":  func(s *RunSpec) { s.Mounts = []Mount{{Kind: MountVolume, Source: "/etc", Target: "/x"}} },
		"unknown mount kind":     func(s *RunSpec) { s.Mounts = []Mount{{Kind: "tmpfs", Source: "x", Target: "/x"}} },
		"port out of range":      func(s *RunSpec) { s.HostPort = 70000 },
		"container port missing": func(s *RunSpec) { s.ContainerPort = 0 },
		"label key with space":   func(s *RunSpec) { s.Labels = map[string]string{"a b": "c"} },
		"label value newline":    func(s *RunSpec) { s.Labels = map[string]string{"a": "b\n--privileged"} },
	}
	for name, mutate := range cases {
		s := base()
		mutate(&s)
		if args, err := s.Args(); err == nil {
			t.Errorf("%s: accepted, args %q", name, args)
		}
	}
}

func TestCheckBindSource(t *testing.T) {
	const dataDir = "/var/lib/musdash"
	refused := []string{
		"/", "/var/run/docker.sock", "/var/run", "/run/docker.sock", "/etc", "/etc/shadow", "/root/.ssh",
		"/proc", "/sys/fs", "/dev/sda", "/var", "/var/lib", "/var/lib/docker/volumes", "/usr/bin",
		dataDir, dataDir + "/master.key", dataDir + "/apps/other/env",
		"/srv/../etc", "relative/path", "/srv/a,readonly",
	}
	for _, p := range refused {
		if CheckBindSource(p, dataDir) == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"/srv/files", "/home/deploy/uploads", "/opt/app/config", "/mnt/storage", "/data", "/etcetera", "/var2/x"} {
		if err := CheckBindSource(p, dataDir); err != nil {
			t.Errorf("refused %q: %v", p, err)
		}
	}
}

func TestInternalBindSkipsDenyList(t *testing.T) {
	spec := RunSpec{Name: "c", Image: "nginx", Mounts: []Mount{{Kind: MountBind, Source: "/run/musdash/apps/a1/files/f1", Target: "/etc/app.conf", Internal: true}}}
	if _, err := spec.Args(); err != nil {
		t.Fatalf("a file mount musdash wrote itself was refused: %v", err)
	}
	spec.Mounts[0].Internal = false
	if _, err := spec.Args(); err == nil {
		t.Fatal("a person's bind mount under /run was accepted")
	}
}

// scripted is a Runner that answers from a table keyed by the command line.
type scripted struct {
	calls   []string
	answers map[string]answer
}

type answer struct {
	out    string
	stderr string
	code   int
}

func (s *scripted) line(c runner.Cmd) string { return c.Name + " " + strings.Join(c.Args, " ") }

func (s *scripted) reply(c runner.Cmd) (answer, error) {
	line := s.line(c)
	s.calls = append(s.calls, line)
	for prefix, a := range s.answers {
		if strings.HasPrefix(line, prefix) {
			if a.code != 0 {
				return a, &runner.ExitError{Name: c.Name, Code: a.code, Stderr: a.stderr}
			}
			return a, nil
		}
	}
	return answer{}, nil
}

func (s *scripted) Run(_ context.Context, c runner.Cmd) error {
	a, err := s.reply(c)
	if c.Stdout != nil {
		io.WriteString(c.Stdout, a.out)
	}
	return err
}

func (s *scripted) Output(_ context.Context, c runner.Cmd) ([]byte, error) {
	a, err := s.reply(c)
	return []byte(a.out), err
}

func (s *scripted) WriteFile(context.Context, string, fs.FileMode, io.Reader) error { return nil }
func (s *scripted) ReadFile(context.Context, string) (io.ReadCloser, error) {
	return nil, fs.ErrNotExist
}
func (s *scripted) MkdirAll(context.Context, string, fs.FileMode) error { return nil }
func (s *scripted) RemoveAll(context.Context, string) error             { return nil }
func (s *scripted) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not scripted")
}
func (s *scripted) Terminal(context.Context, runner.Cmd, int, int) (runner.Terminal, error) {
	return nil, runner.ErrNoTerminal
}
func (s *scripted) Close() error { return nil }

func TestState(t *testing.T) {
	ctx := context.Background()
	r := &scripted{answers: map[string]answer{
		"docker inspect --type container --format {{json .State}} up":      {out: `{"Status":"running","Running":true,"ExitCode":0,"Health":{"Status":"healthy"}}` + "\n"},
		"docker inspect --type container --format {{json .State}} crashed": {out: `{"Status":"exited","Running":false,"ExitCode":137}`},
		"docker inspect --type container --format {{json .State}} gone":    {code: 1, stderr: "Error response from daemon: No such container: gone"},
		"docker inspect --type container --format {{json .State}} broken":  {code: 1, stderr: "Cannot connect to the Docker daemon"},
	}}
	c := Client{R: r}

	if st, err := c.State(ctx, "up"); err != nil || !st.Running || st.Health != "healthy" {
		t.Fatalf("up: %+v %v", st, err)
	}
	if st, err := c.State(ctx, "crashed"); err != nil || st.Running || st.ExitCode != 137 || st.Health != "" {
		t.Fatalf("crashed: %+v %v", st, err)
	}
	if _, err := c.State(ctx, "gone"); !errors.Is(err, ErrNoContainer) {
		t.Fatalf("gone: %v", err)
	}
	if _, err := c.State(ctx, "broken"); err == nil || errors.Is(err, ErrNoContainer) {
		t.Fatalf("a daemon failure must not look like a missing container: %v", err)
	}
}

func TestRunDetectsTakenPort(t *testing.T) {
	r := &scripted{answers: map[string]answer{
		"docker run": {code: 125, stderr: "docker: Error response from daemon: driver failed programming external connectivity: Bind for 127.0.0.1:20000 failed: port is already allocated."},
	}}
	err := Client{R: r}.Run(context.Background(), RunSpec{Name: "c", Image: "nginx", HostPort: 20000, ContainerPort: 80})
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("want ErrPortTaken, got %v", err)
	}
}

func TestStopAndRemoveIgnoreMissing(t *testing.T) {
	r := &scripted{answers: map[string]answer{
		"docker stop": {code: 1, stderr: "Error response from daemon: No such container: x"},
		"docker rm":   {code: 1, stderr: "Error: No such container: x"},
	}}
	c := Client{R: r}
	if err := c.Stop(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureNetwork(t *testing.T) {
	ctx := context.Background()
	exists := &scripted{}
	if err := (Client{R: exists}).EnsureNetwork(ctx, "musdash-env1"); err != nil || len(exists.calls) != 1 {
		t.Fatalf("existing network: %v, calls %q", err, exists.calls)
	}
	missing := &scripted{answers: map[string]answer{"docker network inspect": {code: 1, stderr: "not found"}}}
	if err := (Client{R: missing}).EnsureNetwork(ctx, "musdash-env1"); err != nil {
		t.Fatal(err)
	}
	if want := "docker network create --label musdash.managed=true musdash-env1"; missing.calls[1] != want {
		t.Fatalf("got %q", missing.calls[1])
	}
	raced := &scripted{answers: map[string]answer{
		"docker network inspect": {code: 1, stderr: "not found"},
		"docker network create":  {code: 1, stderr: "network with name musdash-env1 already exists"},
	}}
	if err := (Client{R: raced}).EnsureNetwork(ctx, "musdash-env1"); err != nil {
		t.Fatalf("a concurrent create must not fail: %v", err)
	}
}

func TestList(t *testing.T) {
	r := &scripted{answers: map[string]answer{
		"docker ps": {out: "musdash-a-1\trunning\tapp\ta\td1\nmusdash-b-2\texited\tapp\tb\td2\n"},
	}}
	got, err := Client{R: r}.List(context.Background())
	if err != nil || len(got) != 2 || got[1] != (Listed{Name: "musdash-b-2", State: "exited", Kind: "app", Resource: "b", Deployment: "d2"}) {
		t.Fatalf("%+v %v", got, err)
	}
	// A database's container has no deployment label, so its line ends
	// with an empty field. It must be listed, also as the last line.
	last := &scripted{answers: map[string]answer{
		"docker ps": {out: "musdash-a-1\trunning\tapp\ta\td1\nmusdash-db-x\trunning\tdatabase\tx\t\n"},
	}}
	got, err = Client{R: last}.List(context.Background())
	if err != nil || len(got) != 2 || got[1] != (Listed{Name: "musdash-db-x", State: "running", Kind: "database", Resource: "x"}) {
		t.Fatalf("%+v %v", got, err)
	}
	empty := &scripted{}
	if got, err := (Client{R: empty}).List(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
}
