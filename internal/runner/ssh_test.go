package runner_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
)

func dial(t *testing.T, srv *sshtest.Server, change func(*runner.SSHConfig)) (*runner.SSHRunner, error) {
	t.Helper()
	cfg := runner.SSHConfig{Host: srv.Host, Port: srv.Port, User: "deploy", Signer: srv.Signer, HostKey: srv.HostKey.Marshal(), WorkDir: filepath.Join(t.TempDir(), "work")}
	if change != nil {
		change(&cfg)
	}
	r, err := runner.DialSSH(context.Background(), cfg)
	if err == nil {
		t.Cleanup(func() { r.Close() })
	}
	return r, err
}

func mustDial(t *testing.T, srv *sshtest.Server) *runner.SSHRunner {
	t.Helper()
	r, err := dial(t, srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// hostile are values a person might type into a field that ends up in a
// command. Each must arrive as itself, and none may run anything.
var hostile = []string{
	"plain", "", "two words", "semi;colon", "$(touch PWNED)", "`touch PWNED`", "a'b", `a"b`, "a\\b", "new\nline", "-rf", "--", "*", "~", "$HOME", "é ü 日本",
	"'; touch PWNED; echo '", "|| touch PWNED", "& touch PWNED &", "> PWNED", "a\tb", "!history", "#comment", "{a,b}", "%s",
}

func TestSSHRunRunsExactlyWhatItIsGiven(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ctx := context.Background()
	dir := t.TempDir()

	// Every argument arrives as one argument, whatever it contains.
	out, err := r.Output(ctx, runner.Cmd{Name: "printf", Args: append([]string{"[%s]\n"}, hostile...), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, h := range hostile {
		want.WriteString("[" + h + "]\n")
	}
	if string(out) != want.String() {
		t.Fatalf("arguments were changed on the way:\n%q\nwant\n%q", out, want.String())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "PWNED*")); len(left) != 0 {
		t.Fatal("an argument was run as a command")
	}

	// The working directory, also one with a hostile name.
	odd := filepath.Join(dir, "it's a $(dir); x")
	os.Mkdir(odd, 0o755)
	out, err = r.Output(ctx, runner.Cmd{Name: "pwd", Dir: odd})
	resolved, _ := filepath.EvalSymlinks(odd)
	if err != nil || (strings.TrimSpace(string(out)) != odd && strings.TrimSpace(string(out)) != resolved) {
		t.Fatalf("pwd in %q: %q %v", odd, out, err)
	}

	// Standard input, standard output and standard error are streamed.
	var stdout, stderr bytes.Buffer
	err = r.Run(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "tr a-z A-Z; echo oops >&2"}, Stdin: strings.NewReader("streamed in"), Stdout: &stdout, Stderr: &stderr})
	if err != nil || stdout.String() != "STREAMED IN" || strings.TrimSpace(stderr.String()) != "oops" {
		t.Fatalf("%q %q %v", stdout.String(), stderr.String(), err)
	}

	// A non-zero exit is an ExitError with the code and what was complained.
	_, err = r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "echo no such thing >&2; exit 7"}})
	var exit *runner.ExitError
	if !errors.As(err, &exit) || exit.Code != 7 || exit.Stderr != "no such thing" {
		t.Fatalf("%#v", err)
	}
	if err := r.Run(ctx, runner.Cmd{Name: "definitely-not-a-command-xyz"}); !errors.As(err, &exit) || exit.Code != 127 {
		t.Fatalf("a missing command: %v", err)
	}
	// Output refuses more than it may hold rather than cutting it short.
	if _, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "head -c 2000000 /dev/zero"}}); !errors.Is(err, runner.ErrOutputTooLarge) {
		t.Fatalf("too much output: %v", err)
	}
}

func TestSSHEnvironmentNeverOnACommandLine(t *testing.T) {
	srv := sshtest.Start(t)
	work := filepath.Join(t.TempDir(), "work")
	r, err := dial(t, srv, func(c *runner.SSHConfig) { c.WorkDir = work })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	secret := "tok3n with 'quotes' $(touch PWNED) and\nnewline"
	out, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", `printf '%s|%s' "$GIT_TOKEN" "$PLAIN"`}, Env: []string{"GIT_TOKEN=" + secret, "PLAIN=1"}})
	if err != nil || string(out) != secret+"|1" {
		t.Fatalf("%q %v", out, err)
	}
	for _, line := range srv.Commands() {
		if strings.Contains(line, "tok3n") {
			t.Fatalf("the secret is on a command line: %s", line)
		}
	}
	// The file that carried it is gone, also when the command failed and
	// when it could not start.
	r.Run(ctx, runner.Cmd{Name: "false", Env: []string{"A=b"}})
	r.Run(ctx, runner.Cmd{Name: "no-such-command-xyz", Env: []string{"A=b"}, Dir: "/no/such/dir"})
	if left, _ := filepath.Glob(filepath.Join(work, ".env-*")); len(left) != 0 {
		t.Fatalf("environment files were left on the server: %v", left)
	}
	if info, err := os.Stat(work); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the work directory: %v %v", info, err)
	}
	// A name that is not a variable name is refused, not written.
	if err := r.Run(ctx, runner.Cmd{Name: "true", Env: []string{"BAD NAME; touch PWNED=x"}}); err == nil {
		t.Fatal("a bad variable name was accepted")
	}
}

func TestSSHCancelKillsTheCommand(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	marker := "musdash-ssh-test-" + strconv.Itoa(os.Getpid())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "sleep 300; echo " + marker}})
	}()
	running := func() bool {
		out, _ := exec.Command("pgrep", "-f", marker).Output()
		return len(bytes.TrimSpace(out)) > 0
	}
	deadline := time.Now().Add(5 * time.Second)
	for !running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !running() {
		t.Fatal("the command did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	deadline = time.Now().Add(5 * time.Second)
	for running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if running() {
		t.Fatal("the command is still running on the server after it was cancelled")
	}
}

func TestSSHHostKeyIsPinned(t *testing.T) {
	srv := sshtest.Start(t)
	other := sshtest.Start(t)

	// The first connection reports the key it saw; the caller records it.
	var seen ssh.PublicKey
	if _, err := dial(t, srv, func(c *runner.SSHConfig) {
		c.HostKey = nil
		c.Seen = func(k ssh.PublicKey) error { seen = k; return nil }
	}); err != nil {
		t.Fatal(err)
	}
	if seen == nil || !bytes.Equal(seen.Marshal(), srv.HostKey.Marshal()) {
		t.Fatal("the server's key was not reported")
	}
	// A caller that does not like the key stops the connection.
	if _, err := dial(t, srv, func(c *runner.SSHConfig) {
		c.HostKey = nil
		c.Seen = func(ssh.PublicKey) error { return errors.New("not that one") }
	}); err == nil {
		t.Fatal("a refused key was used")
	}
	// Neither a recorded key nor anyone to show a new one to: refused
	// before a connection is made, rather than trusting whoever answers.
	before := len(srv.Commands())
	if r, err := dial(t, srv, func(c *runner.SSHConfig) { c.HostKey, c.Seen = nil, nil }); err == nil {
		r.Run(context.Background(), runner.Cmd{Name: "true"})
		t.Fatal("connected without any way to check the server's key")
	}
	if len(srv.Commands()) != before {
		t.Fatal("something ran on a server whose key was never checked")
	}
	// Something else answering at the address, with another key.
	_, err := dial(t, other, func(c *runner.SSHConfig) { c.HostKey = srv.HostKey.Marshal(); c.Signer = other.Signer })
	if !errors.Is(err, runner.ErrHostKeyChanged) {
		t.Fatalf("a changed host key: %v", err)
	}
	if len(other.Commands()) != 0 {
		t.Fatal("something ran on a server with the wrong key")
	}
	// A key the server does not know.
	if _, err := dial(t, srv, func(c *runner.SSHConfig) { c.Signer = other.Signer }); err == nil {
		t.Fatal("signed in with a key the server does not accept")
	}
}

func TestSSHFiles(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ctx := context.Background()
	root := t.TempDir()

	// Parents are created private too.
	deep := filepath.Join(root, "a", "b c", "d")
	if err := r.MkdirAll(ctx, deep, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "a"), deep} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", p, info, err)
		}
	}

	// Content of any kind arrives whole, under its name only when complete.
	name := filepath.Join(deep, "it's $(x).env")
	content := strings.Repeat("line with 'quotes' and \x00 a zero byte\n", 5000)
	if err := r.WriteFile(ctx, name, 0o600, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(name)
	info, _ := os.Stat(name)
	if err != nil || string(got) != content || info.Mode().Perm() != 0o600 {
		t.Fatalf("written file: %d bytes, mode %v, %v", len(got), info.Mode(), err)
	}
	// Replaced in one step; a write that fails leaves the old file and no
	// temporary one.
	if err := r.WriteFile(ctx, name, 0o640, strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(name); string(got) != "second" {
		t.Fatalf("after replacing: %q", got)
	}
	if info, _ := os.Stat(name); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode after replacing: %v", info.Mode())
	}
	if err := r.WriteFile(ctx, filepath.Join(root, "no", "such", "dir", "f"), 0o600, strings.NewReader("x")); err == nil {
		t.Fatal("a write into a missing directory succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(deep, ".musdash-*")); len(left) != 0 {
		t.Fatalf("temporary files were left: %v", left)
	}

	// Reading streams the file; one that is not there says so at once.
	f, err := r.ReadFile(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := io.ReadAll(f)
	f.Close()
	if string(read) != "second" {
		t.Fatalf("read back %q", read)
	}
	if _, err := r.ReadFile(ctx, filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
	big := filepath.Join(root, "big")
	os.WriteFile(big, bytes.Repeat([]byte("0123456789abcdef"), 1<<17), 0o600) // 2 MiB: more than Output holds
	f, err = r.ReadFile(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, f)
	f.Close()
	if n != 2<<20 {
		t.Fatalf("a large file came back as %d bytes", n)
	}

	if err := r.RemoveAll(ctx, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatal("the directory is still there")
	}
	// Paths that could mean something else are refused outright.
	for _, bad := range []string{"", "relative/path", "/", "/a/../b", "/a/./b", "/a//b"} {
		if err := r.RemoveAll(ctx, bad); err == nil {
			t.Errorf("RemoveAll(%q) was accepted", bad)
		}
		if err := r.WriteFile(ctx, bad, 0o600, strings.NewReader("x")); err == nil {
			t.Errorf("WriteFile(%q) was accepted", bad)
		}
	}
}

func TestSSHDialIsFromTheServersSide(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.WriteString(c, "hello from the server's loopback\n")
			c.Close()
		}
	}()
	conn, err := r.Dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	said, _ := io.ReadAll(conn)
	conn.Close()
	if !strings.Contains(string(said), "hello from the server") {
		t.Fatalf("%q", said)
	}
	// A port nothing listens on is an error, not a hang.
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.Dial(ctx, "tcp", ln.Addr().String()); err == nil {
		t.Fatal("dialled a closed port")
	}
}

func TestSSHDroppedConnection(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	if !r.Alive() {
		t.Fatal("a fresh connection is not alive")
	}
	srv.DropConnections()
	if err := r.Run(context.Background(), runner.Cmd{Name: "true"}); err == nil {
		t.Fatal("a command ran on a dropped connection")
	}
	if r.Alive() {
		t.Fatal("a dropped connection reports alive")
	}
}

// took fails the test when f needs longer than limit.
func took(t *testing.T, what string, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s was still waiting after %s", what, limit)
	}
}

// A server that hangs keeps its connection open and never answers. A
// command on it must end with its context, not wait for ever.
func TestSSHAServerThatStopsAnsweringDoesNotHoldACommand(t *testing.T) {
	for _, what := range []string{sshtest.Sessions, sshtest.Exec} {
		t.Run(what, func(t *testing.T) {
			srv := sshtest.Start(t)
			r := mustDial(t, srv)
			srv.Silence(what)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			took(t, "Run", 5*time.Second, func() {
				if err := r.Run(ctx, runner.Cmd{Name: "true"}); err == nil {
					t.Error("a command ran on a server that does not answer")
				}
			})
			took(t, "ReadFile", 5*time.Second, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				defer cancel()
				if f, err := r.ReadFile(ctx, "/etc/hosts"); err == nil {
					f.Close()
					t.Error("a file was read from a server that does not answer")
				}
			})
		})
	}
}

func TestSSHAliveGivesUpOnASilentServer(t *testing.T) {
	defer runner.SetReplyWithinForTest(200 * time.Millisecond)()
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	srv.Silence(sshtest.Keepalive)
	took(t, "Alive", 5*time.Second, func() {
		if r.Alive() {
			t.Error("a server that says nothing counts as alive")
		}
	})
}

type failingWriter struct{ n int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n += len(p); f.n > 1000 {
		return 0, errors.New("nowhere to put it")
	}
	return len(p), nil
}

// The sending half of a pipe between two servers: when the receiving half
// fails, the sender must stop at once and not sit on a full window until
// its context ends.
func TestSSHACommandWhoseOutputHasNowhereToGoIsStopped(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	err := r.Run(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "yes | head -c 50000000"}, Stdout: &failingWriter{}})
	if err == nil {
		t.Fatal("no error for output that could not be written")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the command ended only after %s", took)
	}
}

// sshd allows a connection ten sessions unless told otherwise. More
// commands than one connection carries go over a second one, and beyond
// the last connection they are refused rather than left hanging.
func TestSSHMoreCommandsThanOneConnectionCarries(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	hold := func(n int) {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.Run(ctx, runner.Cmd{Name: "sleep", Args: []string{"30"}})
			}()
		}
	}
	running := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for len(srv.Commands()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("%d commands running, want %d", len(srv.Commands()), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	hold(runner.LaneSessions)
	running(runner.LaneSessions)
	if got := srv.Accepted(); got != 1 {
		t.Fatalf("%d connections for %d commands, want 1", got, runner.LaneSessions)
	}
	if out, err := r.Output(ctx, runner.Cmd{Name: "echo", Args: []string{"over"}}); err != nil || strings.TrimSpace(string(out)) != "over" {
		t.Fatalf("a command beyond the first connection: %q %v", out, err)
	}
	if got := srv.Accepted(); got != 2 {
		t.Fatalf("%d connections, want a second one", got)
	}
	all := runner.LaneSessions * runner.MaxLanes
	hold(all - runner.LaneSessions)
	running(all + 1) // the echo was one
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); !errors.Is(err, runner.ErrBusy) {
		t.Fatalf("a command beyond every connection: %v, want ErrBusy", err)
	}
	if got := srv.Accepted(); got != runner.MaxLanes {
		t.Fatalf("%d connections, want at most %d", got, runner.MaxLanes)
	}
	// Room again once commands end.
	cancel()
	wg.Wait()
	if _, err := r.Output(context.Background(), runner.Cmd{Name: "true"}); err != nil {
		t.Fatalf("after the commands ended: %v", err)
	}
}
