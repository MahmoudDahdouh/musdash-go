package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// TestTerminalShellEndsWithDocker opens a terminal in a real container with
// the handler's own command, leaves things running in it, closes it, and
// runs the handler's hang-up. Nothing of the terminal may be left, and the
// container's own process must still run.
//
//	MUSDASH_DOCKER_TEST=1 go test ./internal/web -run TestTerminalShellEndsWithDocker -v
func TestTerminalShellEndsWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx := context.Background()
	local := runner.NewLocal()
	srv := sshtest.Start(t)
	remote, err := runner.DialSSH(ctx, runner.SSHConfig{Host: srv.Host, Port: srv.Port, User: "deploy", Signer: srv.Signer, HostKey: srv.HostKey.Marshal(), WorkDir: filepath.Join(t.TempDir(), "work")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })

	// BusyBox's shell and su, and Debian's: bash, and a su that does not
	// take a hang-up itself.
	for _, image := range []string{"alpine:3", "debian:stable-slim"} {
		for way, r := range map[string]runner.Runner{"local": local, "ssh": remote} {
			t.Run(image+" "+way, func(t *testing.T) {
				docker := func(args ...string) string {
					out, err := local.Output(ctx, runner.Cmd{Name: "docker", Args: args})
					if err != nil {
						t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
					}
					return string(out)
				}
				// What runs in the container, one command line per line.
				running := func(name string) []string {
					lines := strings.Split(strings.TrimSpace(docker("top", name, "-eo", "pid,args")), "\n")
					return lines[1:]
				}
				has := func(lines []string, want string) bool {
					for _, line := range lines {
						if strings.Contains(line, want) {
							return true
						}
					}
					return false
				}
				name := "termtest-" + secret.RandomID()
				docker("run", "--detach", "--name", name, image, "sleep", "600")
				t.Cleanup(func() { local.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"rm", "--force", name}}) })

				// open starts a terminal and types into it.
				open := func(typed ...string) (runner.Terminal, string) {
					mark := secret.RandomID()
					cmd, err := terminalCmd(name, mark)
					if err != nil {
						t.Fatal(err)
					}
					term, err := r.Terminal(ctx, cmd, 80, 24)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { term.Close() })
					go func() {
						buf := make([]byte, 4096)
						for {
							if _, err := term.Read(buf); err != nil {
								return
							}
						}
					}()
					for _, line := range typed {
						time.Sleep(300 * time.Millisecond)
						if _, err := term.Write([]byte(line + "\r")); err != nil {
							t.Fatal(err)
						}
					}
					return term, mark
				}
				hangUp := func(mark string) {
					hang, err := hangUpCmd(name, mark)
					if err != nil {
						t.Fatal(err)
					}
					if err := r.Run(ctx, hang); err != nil {
						t.Fatalf("the hang-up: %v", err)
					}
				}
				// shells counts what is a shell, or on the way to one.
				shells := func(lines []string) int {
					n := 0
					for _, line := range lines {
						if f := strings.Fields(line); len(f) > 1 && (f[1] == "sh" || f[1] == "bash" || f[1] == "su") {
							n++
						}
					}
					return n
				}

				// The terminal that will be closed: a job in the background,
				// one that left the terminal, then a shell of another user
				// with a job of its own and a program in front.
				first, firstMark := open("sleep 1111 &", "setsid sleep 5555 &", "su -s /bin/sh nobody", "sleep 2222 &", "sleep 3333")
				// A second terminal in the same container, which stays open.
				second, secondMark := open("sleep 7777 &")
				// A third, whose shell is ended by the person, with a job
				// left behind.
				_, thirdMark := open("sleep 8888 &", "exit")
				waitFor(t, "everything typed running in the container", func() bool {
					now := running(name)
					return has(now, "sleep 1111") && has(now, "sleep 5555") && has(now, "sleep 2222") && has(now, "sleep 3333") &&
						has(now, "sleep 7777") && has(now, "sleep 8888")
				})

				first.Close()
				time.Sleep(500 * time.Millisecond)
				// Docker ends its client, not what the client started: this
				// is why the hang-up exists. Should Docker ever end it
				// itself, this says so.
				if left := running(name); has(left, "sleep 3333") {
					t.Logf("after the terminal was closed, still running: %q", left)
				} else {
					t.Log("Docker ended the shell with its client; the hang-up had nothing to do")
				}

				hangUp(firstMark)
				hangUp(thirdMark)
				var left []string
				gone := func(what ...string) bool {
					left = running(name)
					for _, w := range what {
						if has(left, w) {
							return false
						}
					}
					return true
				}
				deadline := time.Now().Add(5 * time.Second)
				for !(gone("sleep 1111", "sleep 2222", "sleep 3333") && shells(left) == 1) && time.Now().Before(deadline) {
					time.Sleep(100 * time.Millisecond)
				}
				if !gone("sleep 1111", "sleep 2222", "sleep 3333") || shells(left) != 1 {
					t.Fatalf("after the hang-up the container still runs something of the closed terminal: %q", left)
				}
				// What was not the closed terminal's is still there: the
				// container's own process, the other terminal with its job,
				// the program that had left the terminal, and the job of a
				// shell that was ended with exit, as on any terminal.
				for _, want := range []string{"sleep 600", "sleep 7777", "sleep 5555", "sleep 8888"} {
					if !has(left, want) {
						t.Errorf("the hang-up of one terminal ended %q: %q", want, left)
					}
				}

				second.Close()
				hangUp(secondMark)
				deadline = time.Now().Add(5 * time.Second)
				for !(gone("sleep 7777") && shells(left) == 0) && time.Now().Before(deadline) {
					time.Sleep(100 * time.Millisecond)
				}
				if !gone("sleep 7777") || shells(left) != 0 || !has(left, "sleep 600") {
					t.Fatalf("after the second hang-up: %q", left)
				}
			})
		}
	}
}
