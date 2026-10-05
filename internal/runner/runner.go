// Package runner runs commands and touches files on a server. It is the only
// way the rest of musdash reaches a machine, local or remote, so the same
// deploy code drives both.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
)

// Cmd describes one command. Name and Args are passed as an argument vector,
// never through a shell, so no value in them is interpreted.
type Cmd struct {
	Name   string
	Args   []string
	Env    []string // KEY=VALUE pairs added to the inherited environment
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner executes commands and file operations on one server.
type Runner interface {
	// Run executes c and streams its output to c.Stdout and c.Stderr. A
	// non-zero exit is returned as *ExitError. Cancelling ctx kills the
	// command.
	Run(ctx context.Context, c Cmd) error
	// Output runs c and returns its stdout. It is for short answers such as
	// `docker port`; output beyond OutputLimit is an error rather than a
	// silent truncation.
	Output(ctx context.Context, c Cmd) ([]byte, error)
	// WriteFile writes r to path atomically with the given mode.
	WriteFile(ctx context.Context, path string, mode fs.FileMode, r io.Reader) error
	// ReadFile opens path for streaming.
	ReadFile(ctx context.Context, path string) (io.ReadCloser, error)
	MkdirAll(ctx context.Context, path string, mode fs.FileMode) error
	RemoveAll(ctx context.Context, path string) error
	// Dial opens a network connection as the server sees it: "127.0.0.1"
	// is the server's own loopback interface, wherever musdash runs. A
	// health check of a container's port goes through it.
	Dial(ctx context.Context, network, address string) (net.Conn, error)
	// Close releases any connection the runner holds.
	Close() error
}

// Located is implemented by a Runner whose server keeps musdash's files
// somewhere other than the control plane's own data directory: a remote
// server. Paths handed to such a Runner are built under DataDir.
type Located interface {
	DataDir() string
}

// DataDirOf returns the data directory of r's server, or "" when it is the
// control plane's own.
func DataDirOf(r Runner) string {
	if l, ok := r.(Located); ok {
		return l.DataDir()
	}
	return ""
}

// OutputLimit caps what Output will hold in memory.
const OutputLimit = 1 << 20

// ExitError reports a command that ran and exited non-zero.
type ExitError struct {
	Name   string
	Code   int
	Stderr string // the last part of stderr, when Output captured it
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s exited with status %d: %s", e.Name, e.Code, e.Stderr)
	}
	return fmt.Sprintf("%s exited with status %d", e.Name, e.Code)
}

// ErrOutputTooLarge is returned by Output when the command wrote more than
// OutputLimit bytes.
var ErrOutputTooLarge = fmt.Errorf("command output exceeded %d bytes", OutputLimit)

// capWriter keeps at most limit bytes and records whether more arrived.
type capWriter struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room < len(p) {
		w.overflow = true
		if room > 0 {
			w.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

// tailWriter keeps only the last limit bytes, for error messages.
type tailWriter struct {
	buf   []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-w.limit:]...)
	}
	return len(p), nil
}

// stderrTail is how much stderr an ExitError carries.
const stderrTail = 2048
