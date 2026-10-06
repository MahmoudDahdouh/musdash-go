package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
)

func TestLookupWatch(t *testing.T) {
	failed := errors.New("docker exited with status 1")
	for _, c := range []struct {
		name   string
		writes []string
		seen   bool
	}{
		{"curl", []string{"#8 0.412 curl: (6) Could not resolve host: registry.npmjs.org\n"}, true},
		{"apt", []string{"W: Failed to fetch http://deb.debian.org/debian/dists/x  Temporary failure resolving 'deb.debian.org'\n"}, true},
		{"pip, in two writes", []string{"[Errno -3] Temporary failure in na", "me resolution\n"}, true},
		{"npm, a letter at a time", strings.Split("npm ERR! code EAI_AGAIN", ""), true},
		{"cargo", []string{"failed to lookup address information: Try again"}, true},
		{"busybox wget", []string{"wget: bad address 'dl-cdn.alpinelinux.org'\n"}, true},
		{"apk 2", []string{"WARNING: Ignoring https://dl-cdn.alpinelinux.org/alpine/v3.19/main: temporary error (try again later)\n"}, true},
		{"apk 3", []string{"WARNING: fetching https://dl-cdn.alpinelinux.org/alpine/v3.22/main: DNS: transient error (try again later)\n"}, true},
		{"pip on musl", []string{"NewConnectionError: Failed to establish a new connection: [Errno -3] Try again\n"}, true},
		{"nix", []string{"error: unable to download 'https://cache.nixos.org/x.narinfo': Couldn't resolve host name (6)\n"}, true},
		{"a write far larger than a phrase", []string{strings.Repeat("x", 70000) + " EAI_AGAIN " + strings.Repeat("y", 70000)}, true},
		{"large writes without one", []string{strings.Repeat("x", 70000), strings.Repeat("Y", 70000)}, false},
		{"three writes with noise between", []string{"x could not res", "olve h", "ost y"}, true},
		// A name that does not exist is the repository's own mistake, and
		// what the daemon says when it cannot pull.
		{"a name that does not exist", []string{"curl: (6) ", "getaddrinfo ENOTFOUND regisrty.npmjs.org\n", "dial tcp: lookup x: no such host\n", "Name or service not known\n"}, false},
		{"an ordinary failure", []string{"npm ERR! missing script: build\n", "exit code: 1\n"}, false},
		{"nothing", nil, false},
	} {
		var out bytes.Buffer
		w := &lookupWatch{w: &out}
		for _, part := range c.writes {
			if n, err := io.WriteString(w, part); n != len(part) || err != nil {
				t.Fatalf("%s: wrote %d of %d: %v", c.name, n, len(part), err)
			}
		}
		if out.String() != strings.Join(c.writes, "") {
			t.Errorf("%s: the output was changed on the way: %q", c.name, out.String())
		}
		err := w.explain("build", failed, "build-1")
		if !errors.Is(err, failed) || !strings.HasPrefix(err.Error(), "build: ") || !strings.HasSuffix(err.Error(), failed.Error()) {
			t.Errorf("%s: the build's own error was lost: %v", c.name, err)
		}
		if said := strings.Contains(err.Error(), "daemon.json") && strings.Contains(err.Error(), "containers on build-1 can"); said != c.seen {
			t.Errorf("%s: advice given = %v, want %v: %v", c.name, said, c.seen, err)
		}
		if !c.seen && err.Error() != "build: "+failed.Error() {
			t.Errorf("%s: an error without advice is not the plain one: %v", c.name, err)
		}
		// Only the end of the last write is kept.
		if len(w.held) >= longestLookupFailure {
			t.Errorf("%s: %d bytes of output are held", c.name, len(w.held))
		}
	}
}

// On a server where the steps of a build have no DNS, every build that
// downloads something failed in the words of whichever tool ran.
func TestABuildThatCannotLookUpNamesSaysWhatMayBeBehindIt(t *testing.T) {
	e := newEnv(t)
	said := "#9 1.204 npm ERR! request to https://registry.npmjs.org/express failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org\n"
	rec := &gitEnvRecorder{}
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker build") {
			// In two writes, as a stream arrives. And the command's error
			// ends with the last of what it printed, as a real one does:
			// two thousand bytes of it.
			io.WriteString(c.Stdout, said[:40])
			io.WriteString(c.Stdout, said[40:])
			return "", runnertest.Exit("docker", 1, strings.Repeat("#9 ERROR: process \"/bin/sh -c npm ci\" did not complete successfully: exit code: 1\n", 25))
		}
		return rec.handle(line, c)
	}
	e.gitApp(nil)
	dep := e.deploy()
	if dep.Status != db.DeployFailed || !strings.HasPrefix(dep.Error, "build:") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	for _, want := range []string{"could not look up a name", "containers on " + e.server.Name + " can", `"dns"`, "/etc/docker/daemon.json"} {
		if !strings.Contains(dep.Error, want) {
			t.Errorf("the error does not say %q: %q", want, dep.Error)
		}
	}
	// The advice is musdash's own sentence: it repeats nothing the build
	// printed. And it comes before the command's own error, which is long:
	// a notification carries the first 1500 bytes and no more.
	advice, rest, _ := strings.Cut(dep.Error, "The build ended with:")
	if strings.Contains(advice, "registry.npmjs.org") || strings.Contains(advice, "EAI_AGAIN") || strings.Contains(advice, "npm") {
		t.Errorf("the build's output is in the advice: %q", advice)
	}
	if len(advice) > 600 || !strings.Contains(advice, "daemon.json") || !strings.Contains(rest, "did not complete successfully") || len(dep.Error) < 2000 {
		t.Errorf("the advice (%d bytes of %d) must come first: %q", len(advice), len(dep.Error), dep.Error)
	}
	if log := e.log(dep); !strings.Contains(log, said) || !strings.Contains(log, "daemon.json") {
		t.Errorf("the log:\n%s", log)
	}

	// A build that fails for another reason gets no such advice, and
	// neither does one whose clone could not find its host: that is the
	// server's own DNS.
	for what, fail := range map[string]string{"docker build": "exit code: 1\n", "git clone": "fatal: unable to access: Could not resolve host: github.com\n"} {
		e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
			if strings.HasPrefix(line, what) {
				if c.Stdout != nil {
					io.WriteString(c.Stdout, fail)
				}
				return "", runnertest.Exit(strings.Fields(what)[0], 1, fail)
			}
			return rec.handle(line, c)
		}
		if dep := e.deploy(); dep.Status != db.DeployFailed || strings.Contains(dep.Error, "daemon.json") {
			t.Errorf("a failed %s: %s %q", what, dep.Status, dep.Error)
		}
	}
}

// The same for a service built from its repository.
func TestAServiceBuildThatCannotLookUpNamesSaysSo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.db.CreateService(ctx, e.team, db.Service{
		EnvironmentID: e.app.EnvironmentID, ServerID: e.server.ID, Name: "stack", Template: db.TemplateGit,
		RepoURL: "https://github.com/acme/stack", RepoName: "acme/stack", Branch: "main", ComposePath: "deploy/compose.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &gitStack{project: ServiceProject(s.ID), links: map[string]bool{}, compose: gitComposeFile}
	scripted := g.handle(e)
	output := "#7 0.9 E: Temporary failure resolving 'archive.ubuntu.com'\n"
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasSuffix(line, " build") {
			io.WriteString(c.Stderr, output)
			return "", runnertest.Exit("docker", 1, "")
		}
		return scripted(line, c)
	}
	got := e.deployService(s, 10*time.Second)
	if !strings.Contains(got.LastError, "build the images") || !strings.Contains(got.LastError, "daemon.json") || !strings.Contains(got.LastError, "containers on "+e.server.Name+" can") {
		t.Fatalf("%q", got.LastError)
	}
	if advice, _, _ := strings.Cut(got.LastError, "The build ended with:"); strings.Contains(advice, "archive.ubuntu.com") || !strings.Contains(advice, "daemon.json") {
		t.Fatalf("the advice: %q", advice)
	}
	output = "#7 0.9 E: Unable to locate package nosuchpackage\n"
	if got = e.deployService(got, 10*time.Second); !strings.Contains(got.LastError, "build the images") || strings.Contains(got.LastError, "daemon.json") {
		t.Fatalf("a build that failed for another reason: %q", got.LastError)
	}
}

// An sshd that does not forward failed every deployment of an app after
// its whole health timeout, with the SSH library's words and nothing else.
func TestAHealthCheckRefusedBySSHDSaysWhatToChange(t *testing.T) {
	e := newEnv(t)
	answer := func(err error) db.Deployment {
		e.probe.mu.Lock()
		e.probe.fn = func() error { return err }
		e.probe.mu.Unlock()
		return e.deploy()
	}
	dep := answer(fmt.Errorf("%w: ssh: rejected: administratively prohibited (open failed)", runner.ErrForwardRefused))
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "did not pass within 1s") ||
		!strings.Contains(dep.Error, "AllowTcpForwarding") || !strings.Contains(dep.Error, "administratively prohibited") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	// Any other failure keeps its own words.
	if dep = answer(errors.New("connection refused")); !strings.Contains(dep.Error, "connection refused") || strings.Contains(dep.Error, "AllowTcpForwarding") {
		t.Fatalf("%q", dep.Error)
	}

	// The probes as they are made for a remote server: the refusal must
	// still be recognisable after the HTTP client has wrapped it.
	srv := sshtest.Start(t)
	r, err := runner.DialSSH(context.Background(), runner.SSHConfig{Host: srv.Host, Port: srv.Port, User: "deploy",
		Signer: srv.Signer, HostKey: srv.HostKey.Marshal(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	srv.Forward(sshtest.Refuse)
	probe := newDialProbe(r.Dial)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := probe.TCP(ctx, srv.Port); !errors.Is(err, runner.ErrForwardRefused) {
		t.Errorf("the port check: %v", err)
	}
	if err := probe.HTTP(ctx, srv.Port, "/healthz"); !errors.Is(err, runner.ErrForwardRefused) {
		t.Errorf("the path check: %v", err)
	}
}

// A build's output goes to the log, so its error is only an exit status.
// The line the build gave up with is put next to it: on the page it is
// what a person reads first, and in the log other lines come after it.
func TestAFailedBuildNamesItsCause(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	out := "#1 [internal] load build definition from Dockerfile\n" +
		"#1 0.2 ERROR: not the build's own line\n" +
		"ERROR: failed to build: failed to solve: failed to read dockerfile: open Dockerfile: no such file or directory\n" +
		"\nView build details: docker-desktop://dashboard/build/default\n"
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker build") {
			// As a stream arrives: the line in pieces.
			for len(out) > 0 {
				n := min(len(out), 37)
				io.WriteString(c.Stdout, out[:n])
				out = out[n:]
			}
			return "", runnertest.Exit("docker", 1, "")
		}
		return rec.handle(line, c)
	}
	e.gitApp(nil)
	dep := e.deploy()
	want := "build: docker exited with status 1: failed to build: failed to solve: failed to read dockerfile: open Dockerfile: no such file or directory"
	if dep.Status != db.DeployFailed || dep.Error != want {
		t.Fatalf("%s\n got %q\nwant %q", dep.Status, dep.Error, want)
	}

	// Of a very long line only the start is kept, and a step's own output
	// is never taken for the cause.
	var sink strings.Builder
	w := &lookupWatch{w: &sink}
	io.WriteString(w, "error: "+strings.Repeat("x", 5000)+"\n#4 1.0 password=hunter2\nlast words")
	if err := w.explain("build", &runner.ExitError{Name: "docker", Code: 1}, "s"); len(err.Error()) > 400 || !strings.Contains(err.Error(), "status 1: error: xxx") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("%d bytes: %q", len(err.Error()), err)
	}
}
