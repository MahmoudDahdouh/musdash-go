package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// lookupFailures are what tools print when no name server answered, in
// lower case: glibc's words (through curl, wget, apt, pip, cargo), Node's,
// libcurl's own in both its spellings (the older is what Nix prints),
// Python's number for it, which is the same with musl, BusyBox wget's, and
// apk's in versions 2 and 3. Not what a name that does not exist gives
// ("no such host", "name or service not known", "ENOTFOUND"): that is a
// mistyped name, and it is also what the Docker daemon's own lookups say.
var lookupFailures = [][]byte{
	[]byte("temporary failure in name resolution"),
	[]byte("temporary failure resolving"),
	[]byte("eai_again"),
	[]byte("could not resolve host"),
	[]byte("couldn't resolve host"),
	[]byte("failed to lookup address information"),
	[]byte("[errno -3]"),
	[]byte("bad address '"),
	[]byte("temporary error (try again later)"),
	[]byte("dns: transient error"),
}

// longestLookupFailure is how much of one write has to be kept to find a
// phrase that the next write completes.
var longestLookupFailure = func() int {
	n := 0
	for _, phrase := range lookupFailures {
		n = max(n, len(phrase))
	}
	return n
}()

// lookupWatch passes a build's output on and notes whether a step in it
// could not look up a name.
//
// On some servers the steps of a build have no DNS while containers have:
// Docker gives the two their resolver in different ways. Every build that
// downloads something then fails in the words of whichever tool ran, which
// say nothing about Docker. musdash cannot see this before a build without
// running one, so it says it when it happens.
//
// It also notes the line the build gave up with (see errorMarks).
//
// Nothing of the output is kept but the end of the last write and that one
// line. The buffer it is looked through in is one write large and is used
// again.
type lookupWatch struct {
	w io.Writer

	mu   sync.Mutex
	held []byte
	seen bool

	line    []byte // the line being written, up to errorLineLimit
	lastErr string // the last whole line that a tool marked as an error
}

// errorLineLimit is how much of one line of a build's output is looked at
// and kept.
const errorLineLimit = 300

// errorMarks are how the tools of a build begin the line that says why
// they gave up: BuildKit and Compose, git, the Docker daemon. Only such a
// line is repeated in a deployment's error, which is also what
// notifications carry: not whatever a step of the build printed last.
var errorMarks = []string{"ERROR: ", "error: ", "fatal: ", "Error response from daemon: "}

// noteLines keeps the last line of p, with what came before it, that
// starts with one of errorMarks. It holds one line at a time, and of a
// long one only its start.
func (l *lookupWatch) noteLines(p []byte) {
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if room := errorLineLimit - len(l.line); room > 0 {
			l.line = append(l.line, part[:min(len(part), room)]...)
		}
		if end < 0 {
			return
		}
		text := string(bytes.TrimSpace(l.line))
		for _, mark := range errorMarks {
			if strings.HasPrefix(text, mark) {
				l.lastErr = text
				break
			}
		}
		l.line = l.line[:0]
		p = p[end+1:]
	}
}

func (l *lookupWatch) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.noteLines(p)
	if !l.seen {
		// What was kept of the write before, then this one in lower case.
		from := len(l.held)
		l.held = append(l.held, p...)
		for i := from; i < len(l.held); i++ {
			if c := l.held[i]; c >= 'A' && c <= 'Z' {
				l.held[i] = c + ('a' - 'A')
			}
		}
		for _, phrase := range lookupFailures {
			if bytes.Contains(l.held, phrase) {
				l.seen = true
				break
			}
		}
		keep := min(len(l.held), longestLookupFailure-1)
		l.held = l.held[:copy(l.held, l.held[len(l.held)-keep:])]
	}
	l.mu.Unlock()
	return l.w.Write(p)
}

// explain is the error of a build that failed: what failed, and the
// command's own error. After a line such as the above, what may be behind
// it comes between the two. It comes first because the command's error
// ends with the last of what it printed, and a notification is cut off
// long before that ends. The sentence is fixed: it repeats nothing the
// build printed.
func (l *lookupWatch) explain(what string, err error, server string) error {
	l.mu.Lock()
	seen, lastErr := l.seen, l.lastErr
	l.mu.Unlock()
	// A command whose output went to the log has nothing to say but its
	// exit status. The line it gave up with is in the log, above whatever
	// it printed afterwards, and this is where a person looks first.
	var exit *runner.ExitError
	if lastErr != "" && errors.As(err, &exit) && exit.Stderr == "" {
		err = fmt.Errorf("%w: %s", err, strings.TrimPrefix(lastErr, "ERROR: "))
	}
	if !seen {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf(`%s: a step could not look up a name. The name may be mistyped; but if containers on %s can look names up and builds cannot, Docker's builds have no DNS there: add a "dns" entry to /etc/docker/daemon.json on that server, such as {"dns": ["1.1.1.1", "8.8.8.8"]}, and restart Docker. The build ended with: %w`, what, server, err)
}
