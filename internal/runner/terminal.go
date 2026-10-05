package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Terminal is a command running on a terminal of its own, as a shell does
// when a person sits in front of it: what is written is typed, and what is
// read is what would appear on the screen.
type Terminal interface {
	// Read returns what the command printed. It returns io.EOF once the
	// command has ended and everything it printed was read.
	io.Reader
	// Write types.
	io.Writer
	// Resize tells the command how large the screen now is.
	Resize(cols, rows int) error
	// Close hangs up: the command is ended and the terminal released.
	Close() error
}

// ErrNoTerminal is returned by a Runner that cannot give a command a
// terminal.
var ErrNoTerminal = errors.New("this server cannot run a command on a terminal")

// clampSize keeps a screen size within what a terminal can be told.
func clampSize(cols, rows int) (uint16, uint16) {
	clamp := func(n, def int) uint16 {
		switch {
		case n < 1:
			return uint16(def)
		case n > 1000:
			return 1000
		}
		return uint16(n)
	}
	return clamp(cols, 80), clamp(rows, 24)
}

// winsize is the kernel's description of a terminal's size.
type winsize struct {
	rows, cols, x, y uint16
}

// control runs an ioctl on f. The descriptor is reached through
// SyscallConn: File.Fd would switch the file to blocking mode, and a read
// of the terminal could then no longer be interrupted by closing it.
func control(f *os.File, request uintptr, arg unsafe.Pointer) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// localTerminal is a command on a pseudo-terminal of this machine. The
// command holds one end; this holds the other.
type localTerminal struct {
	master *os.File
	cmd    *exec.Cmd
	// done is closed when the command has ended.
	done chan struct{}
	once sync.Once
	stop func() bool
}

// Terminal starts c with a new terminal as its input, its output and its
// controlling terminal, in a session of its own: closing the terminal then
// hangs up everything the command started.
func (LocalRunner) Terminal(ctx context.Context, c Cmd, cols, rows int) (Terminal, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	t := &localTerminal{master: master, done: make(chan struct{})}
	if err := t.Resize(cols, rows); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(childEnv(os.Environ()), c.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Ctty is a descriptor number in the child: 0, its standard input.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	err = cmd.Start()
	// The command has its own copy of this end now.
	slave.Close()
	if err != nil {
		master.Close()
		return nil, err
	}
	t.cmd = cmd
	go func() {
		cmd.Wait()
		close(t.done)
	}()
	t.stop = context.AfterFunc(ctx, func() { t.Close() })
	return t, nil
}

func (t *localTerminal) Read(p []byte) (int, error) {
	n, err := t.master.Read(p)
	if err != nil && n == 0 {
		// Linux reports the other end's closing as an input/output error;
		// either way there is nothing more to read.
		return 0, io.EOF
	}
	return n, nil
}

func (t *localTerminal) Write(p []byte) (int, error) { return t.master.Write(p) }

func (t *localTerminal) Resize(cols, rows int) error {
	c, r := clampSize(cols, rows)
	return control(t.master, syscall.TIOCSWINSZ, unsafe.Pointer(&winsize{rows: r, cols: c}))
}

// Close hangs up. The session the command leads is told so (SIGHUP, as
// when a terminal window is closed) and, if it is still there a moment
// later, killed.
func (t *localTerminal) Close() error {
	t.once.Do(func() {
		if t.stop != nil {
			t.stop()
		}
		pid := t.cmd.Process.Pid
		syscall.Kill(-pid, syscall.SIGHUP)
		t.master.Close()
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
			syscall.Kill(-pid, syscall.SIGKILL)
			<-t.done
		}
	})
	return nil
}
