package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBuildCmd(t *testing.T) {
	spec := BuildSpec{
		Tag: "musdash/app1:a1b2c3d4e5f6", ContextDir: "/var/lib/musdash/work/d1/src", Dockerfile: "/var/lib/musdash/work/d1/src/Dockerfile",
		BuildArgs: map[string]string{"NPM_TOKEN": "s3cret token", "API_URL": "https://api.example.com"},
	}
	cm, err := spec.Cmd()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "--progress", "plain", "--tag", "musdash/app1:a1b2c3d4e5f6", "--file", "/var/lib/musdash/work/d1/src/Dockerfile",
		"--label", "musdash.managed=true", "--build-arg", "API_URL", "--build-arg", "NPM_TOKEN", "--", "/var/lib/musdash/work/d1/src"}
	if !reflect.DeepEqual(cm.Args, want) {
		t.Fatalf("args:\n got %q\nwant %q", cm.Args, want)
	}
	// Values travel in the environment, never on the command line.
	if strings.Contains(strings.Join(cm.Args, " "), "s3cret") {
		t.Fatal("a build-arg value is on the command line")
	}
	env := strings.Join(cm.Env, "\n")
	if !strings.Contains(env, "NPM_TOKEN=s3cret token") || !strings.Contains(env, "DOCKER_BUILDKIT=1") {
		t.Fatalf("env: %q", cm.Env)
	}

	for name, mutate := range map[string]func(*BuildSpec){
		"tag as flag":      func(s *BuildSpec) { s.Tag = "--output=/" },
		"relative context": func(s *BuildSpec) { s.ContextDir = "src" },
		"relative file":    func(s *BuildSpec) { s.Dockerfile = "Dockerfile" },
		"arg name as flag": func(s *BuildSpec) { s.BuildArgs = map[string]string{"--network=host": "x"} },
		"arg name with =":  func(s *BuildSpec) { s.BuildArgs = map[string]string{"A=B": "x"} },
	} {
		s := spec
		s.BuildArgs = map[string]string{}
		mutate(&s)
		if _, err := s.Cmd(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReservedBuildArgs(t *testing.T) {
	reserved := []string{
		"DOCKER_HOST", "docker_host", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "BUILDKIT_HOST", "BUILDX_BUILDER",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "PATH", "Path", "HOME", "TMPDIR",
		"HTTP_PROXY", "https_proxy", "NO_PROXY", "ALL_PROXY", "SSH_AUTH_SOCK", "GIT_SSH_COMMAND", "MUSDASH_MASTER_KEY",
	}
	for _, name := range reserved {
		if !ReservedBuildArg(name) {
			t.Errorf("%s is not refused", name)
		}
		spec := BuildSpec{Tag: "musdash/a:t", ContextDir: "/w/src", Dockerfile: "/w/src/Dockerfile", BuildArgs: map[string]string{name: "x"}}
		if cm, err := spec.Cmd(); err == nil {
			t.Errorf("%s reached the docker CLI's environment: %q", name, cm.Env)
		}
	}
	for _, name := range []string{"NPM_TOKEN", "API_URL", "NODE_ENV", "VERSION", "LDFLAGS", "DOCKERFILE_VERSION", "GITHUB_SHA", "PATHS"} {
		if ReservedBuildArg(name) {
			t.Errorf("%s is refused but is an ordinary name", name)
		}
	}
}

func TestImageTagsAndRemove(t *testing.T) {
	ctx := context.Background()
	r := &scripted{answers: map[string]answer{
		"docker images --format {{.Tag}} musdash/app1": {out: "newest\nolder\n<none>\noldest\n"},
		"docker rmi musdash/app1:inuse":                {code: 1, stderr: "conflict: unable to remove repository reference \"musdash/app1:inuse\" (must force) - container 1a2b is using its referenced image"},
		"docker rmi musdash/app1:gone":                 {code: 1, stderr: "Error response from daemon: No such image: musdash/app1:gone"},
		"docker rmi musdash/app1:broken":               {code: 1, stderr: "Cannot connect to the Docker daemon"},
	}}
	c := Client{R: r}
	tags, err := c.ImageTags(ctx, "musdash/app1")
	if err != nil || !reflect.DeepEqual(tags, []string{"newest", "older", "oldest"}) {
		t.Fatalf("%q %v", tags, err)
	}
	if _, err := c.ImageTags(ctx, "musdash/app1:tag"); err == nil {
		t.Error("a tagged reference was accepted as a repository")
	}
	for _, ref := range []string{"musdash/app1:inuse", "musdash/app1:gone", "musdash/app1:fine"} {
		if err := c.RemoveImage(ctx, ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	if err := c.RemoveImage(ctx, "musdash/app1:broken"); err == nil {
		t.Error("a daemon failure was swallowed")
	}
	if err := c.RemoveImage(ctx, "--force"); err == nil {
		t.Error("a flag was accepted as an image")
	}
}
