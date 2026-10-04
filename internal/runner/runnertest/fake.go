// Package runnertest provides a scripted Runner for tests that need to
// inject failures a real server cannot be made to produce on demand, such as
// a pull that fails or a host port that is already taken.
package runnertest

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// Fake records every command and answers through Handle. File operations
// act on an in-memory tree.
type Fake struct {
	// Handle decides a command's stdout and error. A nil Handle succeeds
	// with no output.
	Handle func(line string, c runner.Cmd) (stdout string, err error)

	mu    sync.Mutex
	calls []string
	files map[string][]byte
	modes map[string]fs.FileMode
}

// Line is how a command appears in Calls and is passed to Handle.
func Line(c runner.Cmd) string { return c.Name + " " + strings.Join(c.Args, " ") }

// Exit builds the error of a command that exited non-zero.
func Exit(name string, code int, stderr string) error {
	return &runner.ExitError{Name: name, Code: code, Stderr: stderr}
}

func (f *Fake) answer(c runner.Cmd) (string, error) {
	line := Line(c)
	f.mu.Lock()
	f.calls = append(f.calls, line)
	h := f.Handle
	f.mu.Unlock()
	if h == nil {
		return "", nil
	}
	return h(line, c)
}

func (f *Fake) Run(_ context.Context, c runner.Cmd) error {
	out, err := f.answer(c)
	if c.Stdout != nil && out != "" {
		io.WriteString(c.Stdout, out)
	}
	return err
}

func (f *Fake) Output(_ context.Context, c runner.Cmd) ([]byte, error) {
	out, err := f.answer(c)
	return []byte(out), err
}

func (f *Fake) WriteFile(_ context.Context, path string, mode fs.FileMode, r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = make(map[string][]byte)
		f.modes = make(map[string]fs.FileMode)
	}
	f.files[path] = raw
	f.modes[path] = mode
	return nil
}

func (f *Fake) ReadFile(_ context.Context, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (f *Fake) MkdirAll(context.Context, string, fs.FileMode) error { return nil }

func (f *Fake) RemoveAll(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "rm-all "+path)
	for p := range f.files {
		if p == path || strings.HasPrefix(p, path+"/") {
			delete(f.files, p)
			delete(f.modes, p)
		}
	}
	return nil
}

func (f *Fake) Close() error { return nil }

// Calls returns the commands run so far, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// File returns a written file's content and mode.
func (f *Fake) File(path string) (string, fs.FileMode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.files[path]
	return string(raw), f.modes[path], ok
}

// PutFile places a file as if it already existed on the server.
func (f *Fake) PutFile(path, content string) {
	f.WriteFile(context.Background(), path, 0o644, strings.NewReader(content))
}
