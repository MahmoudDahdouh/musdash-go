package runner_test

import (
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
)

// screen collects what a terminal prints, so that a test can wait for a
// piece of text to appear.
type screen struct {
	t    *testing.T
	term runner.Terminal
	seen chan string
	all  strings.Builder
	eof  bool
}

func watch(t *testing.T, term runner.Terminal) *screen {
	s := &screen{t: t, term: term, seen: make(chan string, 256)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				s.seen <- string(buf[:n])
			}
			if err != nil {
				close(s.seen)
				return
			}
		}
	}()
	return s
}

// until waits for text to appear on the screen and returns what was
// printed since the last call.
func (s *screen) until(text string) string {
	s.t.Helper()
	start := s.all.Len()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(s.all.String()[start:], text) {
		select {
		case part, ok := <-s.seen:
			if !ok {
				s.eof = true
				s.t.Fatalf("the terminal ended before printing %q; it printed %q", text, s.all.String()[start:])
			}
			s.all.WriteString(part)
		case <-deadline:
			s.t.Fatalf("%q did not appear; the terminal printed %q", text, s.all.String()[start:])
		}
	}
	return s.all.String()[start:]
}

// ended waits for the terminal to report its end.
func (s *screen) ended() bool {
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-s.seen:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func typeIn(t *testing.T, term runner.Terminal, text string) {
	t.Helper()
	if _, err := io.WriteString(term, text); err != nil {
		t.Fatalf("type %q: %v", text, err)
	}
}

// terminalBehaves is what a terminal must do, wherever it runs.
func terminalBehaves(t *testing.T, r runner.Runner) {
	ctx := context.Background()
	term, err := r.Terminal(ctx, runner.Cmd{Name: "sh"}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	s := watch(t, term)

	// Markers are typed with a quote in the middle: the terminal echoes
	// what is typed, and the echo must not be taken for the answer.
	//
	// The command has a terminal of the size asked for: it is told so, and
	// it believes it is being typed to.
	typeIn(t, term, "stty size; test -t 0 && echo on-a-''terminal\n")
	if first := s.until("on-a-terminal\r\n"); !strings.Contains(first, "30 100") {
		t.Fatalf("the size of the screen: %q", first)
	}
	// A resize reaches it. Some shells (the old bash that is macOS's sh)
	// read the size and write it back while they draw their prompt, and a
	// resize that lands in between is undone: so the shell is given a
	// moment to settle, and the resize a second chance.
	var got string
	for try := 0; try < 3 && !strings.Contains(got, "43 132"); try++ {
		time.Sleep(300 * time.Millisecond)
		if err := term.Resize(132, 43); err != nil {
			t.Fatal(err)
		}
		typeIn(t, term, "stty size; echo re-''sized\n")
		got = s.until("re-sized\r\n")
	}
	if !strings.Contains(got, "43 132") {
		t.Fatalf("after a resize: %q", got)
	}
	// What is typed is what the command reads, control characters too:
	// Ctrl-C ends what is running in front, and the shell goes on.
	typeIn(t, term, "sleep 600\n")
	time.Sleep(300 * time.Millisecond)
	typeIn(t, term, "\x03")
	typeIn(t, term, "echo still-''here $$ en''d\n")
	got = s.until(" end")
	found := regexp.MustCompile(`still-here (\d+) end`).FindStringSubmatch(got)
	if found == nil {
		t.Fatalf("no process id in %q", got)
	}
	pid, _ := strconv.Atoi(found[1])
	// A background job that would outlive a careless close.
	typeIn(t, term, "sleep 700 & echo star''ted\n")
	s.until("started\r\n")

	// Closing hangs up: the shell ends, and reading says so.
	term.Close()
	if !s.ended() {
		t.Fatal("reading did not end after the terminal was closed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the shell (process %d) outlived its terminal", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLocalTerminal(t *testing.T) {
	terminalBehaves(t, runner.NewLocal())
}

func TestSSHTerminal(t *testing.T) {
	srv := sshtest.Start(t)
	terminalBehaves(t, mustDial(t, srv))
}

func TestTerminalEndsWithItsCommandAndItsContext(t *testing.T) {
	for name, r := range map[string]runner.Runner{"local": runner.NewLocal(), "ssh": mustDial(t, sshtest.Start(t))} {
		// A command that ends: everything it printed is read, then the end.
		term, err := r.Terminal(context.Background(), runner.Cmd{Name: "sh", Args: []string{"-c", "echo last words"}}, 80, 24)
		if err != nil {
			t.Fatal(name, err)
		}
		s := watch(t, term)
		s.until("last words")
		if !s.ended() {
			t.Fatalf("%s: no end after the command ended", name)
		}
		term.Close()
		term.Close() // twice is harmless

		// A context that ends takes the terminal with it.
		ctx, cancel := context.WithCancel(context.Background())
		term, err = r.Terminal(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "echo up; sleep 600"}}, 80, 24)
		if err != nil {
			t.Fatal(name, err)
		}
		s = watch(t, term)
		s.until("up")
		cancel()
		if !s.ended() {
			t.Fatalf("%s: the terminal outlived its context", name)
		}
	}
}

func TestSSHTerminalTakesNoEnvironmentAndFreesItsPlace(t *testing.T) {
	srv := sshtest.Start(t)
	r := mustDial(t, srv)
	ctx := context.Background()
	if _, err := r.Terminal(ctx, runner.Cmd{Name: "sh", Env: []string{"A=b"}}, 80, 24); err == nil {
		t.Fatal("a terminal with an environment was started")
	}
	// Every terminal gives its place on the connection back: more than a
	// connection carries at once can be opened one after another.
	for i := 0; i < 30; i++ {
		term, err := r.Terminal(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "true"}}, 80, 24)
		if err != nil {
			t.Fatalf("terminal %d: %v", i, err)
		}
		term.Close()
	}
	if n := srv.Accepted(); n > 2 {
		t.Fatalf("%d connections for terminals opened one at a time", n)
	}
}
