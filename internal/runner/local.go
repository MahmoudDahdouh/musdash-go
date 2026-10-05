package runner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LocalRunner runs commands on the machine musdash itself runs on.
type LocalRunner struct{}

// NewLocal returns a Runner for the local machine.
func NewLocal() *LocalRunner { return &LocalRunner{} }

func (LocalRunner) command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(childEnv(os.Environ()), c.Env...)
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	// Own process group: cancelling must also stop children such as the
	// helpers `docker build` and `git` spawn, not only the direct child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	// After SIGTERM the group gets a moment to exit; then Wait kills the
	// direct child and run kills whatever is left of the group.
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// childEnv removes musdash's own settings from the environment handed to
// child processes. The master key may be among them, and tools such as
// docker compose substitute environment variables into user-supplied files.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "MUSDASH_") {
			out = append(out, kv)
		}
	}
	return out
}

// run executes cmd. When the context was cancelled it then kills the whole
// process group: os/exec only kills the direct child, and a grandchild that
// ignored SIGTERM would otherwise live on unnoticed.
func run(ctx context.Context, cmd *exec.Cmd) error {
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return err
}

func (r LocalRunner) Run(ctx context.Context, c Cmd) error {
	return wrapExit(c.Name, run(ctx, r.command(ctx, c)), ctx, "")
}

func (r LocalRunner) Output(ctx context.Context, c Cmd) ([]byte, error) {
	out := &capWriter{limit: OutputLimit}
	tail := &tailWriter{limit: stderrTail}
	c.Stdout = out
	if c.Stderr == nil {
		c.Stderr = tail
	} else {
		c.Stderr = io.MultiWriter(c.Stderr, tail)
	}
	err := wrapExit(c.Name, run(ctx, r.command(ctx, c)), ctx, strings.TrimSpace(string(tail.buf)))
	if err != nil {
		return out.buf.Bytes(), err
	}
	if out.overflow {
		return nil, ErrOutputTooLarge
	}
	return out.buf.Bytes(), nil
}

// wrapExit converts exec's error into *ExitError, and prefers the context's
// error when the command died because it was cancelled.
func wrapExit(name string, err error, ctx context.Context, stderr string) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Name: name, Code: ee.ExitCode(), Stderr: stderr}
	}
	return err
}

// WriteFile writes to a temporary file in the same directory and renames it
// over path, so a reader never sees a half-written file.
func (LocalRunner) WriteFile(_ context.Context, path string, mode fs.FileMode, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".musdash-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (LocalRunner) ReadFile(_ context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (LocalRunner) MkdirAll(_ context.Context, path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (LocalRunner) RemoveAll(_ context.Context, path string) error {
	return os.RemoveAll(path)
}

func (LocalRunner) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

func (LocalRunner) Close() error { return nil }
