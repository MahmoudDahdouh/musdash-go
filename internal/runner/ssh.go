package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyChanged is returned when a server presents a different host key
// from the one recorded for it. Either the server was reinstalled, or
// something between musdash and the server is answering in its place.
var ErrHostKeyChanged = errors.New("the server's host key is not the one recorded for it")

// SSHConfig says how to reach a server over SSH.
type SSHConfig struct {
	Host   string
	Port   int
	User   string
	Signer ssh.Signer
	// HostKey is the key the server must present, in wire format. When it
	// is empty the key the server presents is handed to Seen, which
	// records or refuses it: that is how a new server's key is first
	// learned. One of the two must be set.
	HostKey []byte
	Seen    func(key ssh.PublicKey) error
	// WorkDir is a private directory on the server for the short-lived
	// files that carry a command's environment.
	WorkDir string
	// DataDir is where musdash keeps its files on this server.
	DataDir string
	// Timeout bounds connecting and signing in. Zero means 15 seconds.
	Timeout time.Duration
}

// SSHRunner runs commands on a server reached over SSH. Nothing of musdash
// runs there: each command is one session on a shared connection.
//
// Every value is quoted for the remote shell with Quote; a command line is
// never built any other way.
type SSHRunner struct {
	cfg SSHConfig
	// client is the first connection. Alive and Dial use it.
	client *ssh.Client
	// hostKey is the key the server presented on the first connection.
	// Further connections accept only that one.
	hostKey []byte

	mu sync.Mutex
	// lanes are the connections commands run on: lanes[0] is client, and
	// more are opened when one is full. sshd allows a connection only so
	// many sessions (MaxSessions, 10 unless changed), and a server with a
	// few log streams open must still take a deploy's commands.
	lanes  []*lane
	closed chan struct{}
	once   sync.Once
}

// lane is one connection and the number of sessions open on it.
type lane struct {
	client *ssh.Client
	open   int
}

const (
	// laneSessions is how many sessions one connection carries: below
	// sshd's default limit of ten.
	laneSessions = 8
	// maxLanes bounds the connections to one server.
	maxLanes = 3
	// keepaliveEvery and replyWithin find a server that went away without
	// closing the connection: without them a command on it would wait for
	// ever.
	keepaliveEvery = 30 * time.Second
)

// replyWithin is how long a server has to answer a keepalive.
var replyWithin = 15 * time.Second

// SetReplyWithinForTest shortens the wait for a keepalive's answer. It is
// for tests, which cannot wait a quarter of a minute for a server to count
// as gone.
func SetReplyWithinForTest(d time.Duration) (restore func()) {
	old := replyWithin
	replyWithin = d
	return func() { replyWithin = old }
}

// ErrBusy is returned when a server already runs as many commands as its
// connections can carry.
var ErrBusy = errors.New("ssh: too many commands are running on this server at once")

// DialSSH connects and signs in.
func DialSSH(ctx context.Context, cfg SSHConfig) (*SSHRunner, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	client, presented, err := dialClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r := &SSHRunner{cfg: cfg, client: client, hostKey: presented, lanes: []*lane{{client: client}}, closed: make(chan struct{})}
	go r.keepalive()
	return r, nil
}

// dialClient makes one connection. It returns the host key the server
// presented.
func dialClient(ctx context.Context, cfg SSHConfig) (*ssh.Client, []byte, error) {
	var presented []byte
	if cfg.Signer == nil {
		return nil, nil, errors.New("ssh: no key to sign in with")
	}
	// With no key to compare with and nobody to show a new one to, any
	// server would do. That is never what a caller means.
	if len(cfg.HostKey) == 0 && cfg.Seen == nil {
		return nil, nil, errors.New("ssh: no host key is recorded for this server and there is no way to record one")
	}
	clientCfg := &ssh.ClientConfig{
		User:    cfg.User,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)},
		Timeout: cfg.Timeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = key.Marshal()
			if len(cfg.HostKey) > 0 {
				if !bytes.Equal(key.Marshal(), cfg.HostKey) {
					return ErrHostKeyChanged
				}
				return nil
			}
			return cfg.Seen(key)
		},
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	// The handshake has no context of its own; a deadline on the
	// connection bounds it.
	conn.SetDeadline(time.Now().Add(cfg.Timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrHostKeyChanged) {
			return nil, nil, ErrHostKeyChanged
		}
		return nil, nil, fmt.Errorf("sign in to %s as %s: %w", addr, cfg.User, err)
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), presented, nil
}

// keepalive closes the connections of a server that stopped answering, so
// that the commands waiting on them fail instead of waiting for ever.
func (r *SSHRunner) keepalive() {
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	for {
		select {
		case <-r.closed:
			return
		case <-t.C:
			if !r.Alive() {
				r.Close()
				return
			}
		}
	}
}

// within runs f, giving up when ctx ends or, with a limit above zero, when
// that long has passed. f goes on in the background until the connection
// answers or is closed; undo is then given what it returned.
func within[T any](ctx context.Context, limit time.Duration, f func() (T, error), undo func(T)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	var timeout <-chan time.Time
	if limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		timeout = t.C
	}
	var zero T
	late := func() {
		if res := <-done; res.err == nil && undo != nil {
			undo(res.v)
		}
	}
	select {
	case res := <-done:
		return res.v, res.err
	case <-ctx.Done():
		go late()
		return zero, ctx.Err()
	case <-timeout:
		go late()
		return zero, errors.New("ssh: the server did not answer")
	}
}

// session opens a session on a connection that has room, connecting once
// more when none has. The returned function gives the place back and must
// be called when the session has ended.
func (r *SSHRunner) session(ctx context.Context) (*ssh.Session, func(), error) {
	r.mu.Lock()
	var l *lane
	for _, have := range r.lanes {
		if have.open < laneSessions {
			l = have
			break
		}
	}
	if l == nil && len(r.lanes) >= maxLanes {
		r.mu.Unlock()
		return nil, nil, ErrBusy
	}
	if l == nil {
		// Connected under the lock: commands that arrive meanwhile wait
		// for this connection rather than each making one.
		cfg := r.cfg
		cfg.HostKey, cfg.Seen = r.hostKey, nil
		client, _, err := dialClient(ctx, cfg)
		if err != nil {
			r.mu.Unlock()
			return nil, nil, err
		}
		select {
		case <-r.closed:
			r.mu.Unlock()
			client.Close()
			return nil, nil, net.ErrClosed
		default:
		}
		l = &lane{client: client}
		r.lanes = append(r.lanes, l)
	}
	l.open++
	r.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			l.open--
			r.mu.Unlock()
		})
	}
	s, err := within(ctx, 0, l.client.NewSession, func(s *ssh.Session) { s.Close() })
	if err != nil {
		release()
		r.drop(l)
		return nil, nil, fmt.Errorf("ssh: open a session: %w", err)
	}
	return s, release, nil
}

// drop closes an extra connection that failed and has nothing left on it.
// The first connection is not dropped here: when it is dead the Runner as
// a whole is, and whoever holds it finds out through Alive.
func (r *SSHRunner) drop(l *lane) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.client == r.client || l.open > 0 {
		return
	}
	for i, have := range r.lanes {
		if have == l {
			r.lanes = append(r.lanes[:i], r.lanes[i+1:]...)
			l.client.Close()
			return
		}
	}
}

// DataDir is where musdash keeps its files on this server.
func (r *SSHRunner) DataDir() string { return r.cfg.DataDir }

// Alive reports whether the connection still answers. A server that says
// nothing for a while counts as gone.
func (r *SSHRunner) Alive() bool {
	_, err := within(context.Background(), replyWithin, func() (struct{}, error) {
		_, _, err := r.client.SendRequest("keepalive@openssh.com", true, nil)
		return struct{}{}, err
	}, nil)
	return err == nil
}

// Close ends the connections and every command running on them.
func (r *SSHRunner) Close() error {
	var err error
	r.once.Do(func() {
		close(r.closed)
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, l := range r.lanes {
			if cerr := l.client.Close(); cerr != nil && err == nil && l.client == r.client {
				err = cerr
			}
		}
	})
	return err
}

// stopOnError ends a command whose output has nowhere to go. Without it
// the command would sit blocked on a full pipe until its context ended:
// `docker save` into a `docker load` that failed, for one.
type stopOnError struct {
	w    io.Writer
	stop func()
}

func (s *stopOnError) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		s.stop()
	}
	return n, err
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func randomName() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// script is the shell text that runs c: the working directory, then the
// command itself in place of the shell, so that a signal sent to the
// session reaches the command.
func script(c Cmd) string {
	var b strings.Builder
	if c.Dir != "" {
		b.WriteString("cd " + Quote(c.Dir) + " && ")
	}
	b.WriteString("exec " + QuoteJoin(c.Name, c.Args...))
	return b.String()
}

// envFile writes c's environment to a private file on the server and
// returns its path with the shell text that loads and removes it.
//
// sshd accepts almost no variables from a client, and writing them in front
// of the command would put clone tokens and passwords in the server's
// process list. The file is gone before the command starts.
func (r *SSHRunner) envFile(ctx context.Context, env []string) (file, prefix string, err error) {
	if r.cfg.WorkDir == "" {
		return "", "", errors.New("ssh: no work directory for a command's environment")
	}
	var b strings.Builder
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !envNameRE.MatchString(name) {
			return "", "", fmt.Errorf("ssh: bad environment entry %q", name)
		}
		b.WriteString("export " + name + "=" + Quote(value) + "\n")
	}
	file = path.Join(r.cfg.WorkDir, ".env-"+randomName())
	if err := r.MkdirAll(ctx, r.cfg.WorkDir, 0o700); err != nil {
		return "", "", err
	}
	if err := r.WriteFile(ctx, file, 0o600, strings.NewReader(b.String())); err != nil {
		return "", "", err
	}
	return file, ". " + Quote(file) + " && rm -f " + Quote(file) + " && ", nil
}

// start begins c in a new session. The returned cleanup removes what start
// left on the server and must be called once the command has ended.
func (r *SSHRunner) start(ctx context.Context, c Cmd) (*ssh.Session, func(), error) {
	text := script(c)
	cleanup := func() {}
	if len(c.Env) > 0 {
		file, prefix, err := r.envFile(ctx, c.Env)
		if err != nil {
			return nil, cleanup, err
		}
		text = prefix + text
		// The command removes the file itself; this is for one that never
		// got that far.
		cleanup = func() {
			clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			r.remove(clean, file)
		}
	}
	session, release, err := r.session(ctx)
	if err != nil {
		return nil, cleanup, err
	}
	removeEnv := cleanup
	cleanup = func() { release(); removeEnv() }
	session.Stdin = c.Stdin
	if c.Stdout != nil {
		session.Stdout = &stopOnError{w: c.Stdout, stop: func() {
			session.Signal(ssh.SIGKILL)
			session.Close()
		}}
	}
	session.Stderr = c.Stderr
	// Through sh, whatever the account's own shell is.
	if err := begin(ctx, session, "exec sh -c "+Quote(text)); err != nil {
		session.Close()
		return nil, cleanup, fmt.Errorf("ssh: start %s: %w", c.Name, err)
	}
	return session, cleanup, nil
}

// begin starts a command, giving up when ctx ends: a server that accepts
// the session and then never answers must not hold the caller.
func begin(ctx context.Context, session *ssh.Session, text string) error {
	_, err := within(ctx, 0, func() (struct{}, error) { return struct{}{}, session.Start(text) }, nil)
	return err
}

// wait waits for a started command. When ctx ends first, the command is
// killed: without a terminal, sshd leaves a command running whose client
// has gone away.
func wait(ctx context.Context, session *ssh.Session, name, stderr string) error {
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		session.Close()
		return sshExit(name, err, stderr)
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		session.Close()
		<-done
		return ctx.Err()
	}
}

func sshExit(name string, err error, stderr string) error {
	if err == nil {
		return nil
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return &ExitError{Name: name, Code: exit.ExitStatus(), Stderr: stderr}
	}
	return fmt.Errorf("ssh: %s: %w", name, err)
}

func (r *SSHRunner) Run(ctx context.Context, c Cmd) error {
	session, cleanup, err := r.start(ctx, c)
	defer cleanup()
	if err != nil {
		return err
	}
	return wait(ctx, session, c.Name, "")
}

func (r *SSHRunner) Output(ctx context.Context, c Cmd) ([]byte, error) {
	out := &capWriter{limit: OutputLimit}
	tail := &tailWriter{limit: stderrTail}
	c.Stdout = out
	if c.Stderr == nil {
		c.Stderr = tail
	} else {
		c.Stderr = io.MultiWriter(c.Stderr, tail)
	}
	session, cleanup, err := r.start(ctx, c)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	// Wait returns once the output has been copied, so the tail is complete
	// by the time it is read.
	err = wait(ctx, session, c.Name, "")
	var exit *ExitError
	if errors.As(err, &exit) {
		exit.Stderr = strings.TrimSpace(string(tail.buf))
	}
	if err != nil {
		return out.buf.Bytes(), err
	}
	if out.overflow {
		return nil, ErrOutputTooLarge
	}
	return out.buf.Bytes(), nil
}

// checkPath refuses paths that are not plain absolute ones: a relative path
// would depend on where the session happens to start.
func checkPath(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
		return fmt.Errorf("ssh: bad path %q", p)
	}
	return nil
}

// WriteFile writes to a temporary file next to path and renames it over
// path. The file is private while it is being written and gets its mode
// before it gets its name.
func (r *SSHRunner) WriteFile(ctx context.Context, p string, mode fs.FileMode, src io.Reader) error {
	if err := checkPath(p); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(p), ".musdash-"+randomName())
	const write = `umask 077; cat > "$1" && chmod "$2" "$1" && mv -f "$1" "$3" || { rm -f "$1"; exit 1; }`
	_, err := r.Output(ctx, Cmd{Name: "sh", Args: []string{"-c", write, "sh", tmp, fmt.Sprintf("%04o", mode.Perm()), p}, Stdin: src})
	if err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

type sessionReader struct {
	io.Reader
	session *ssh.Session
	release func()
}

func (s sessionReader) Close() error {
	defer s.release()
	s.session.Signal(ssh.SIGKILL)
	return s.session.Close()
}

// ReadFile streams a file. A file that is not there is reported when it is
// opened, as it is locally.
func (r *SSHRunner) ReadFile(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	if _, err := r.Output(ctx, Cmd{Name: "test", Args: []string{"-f", p, "-a", "-r", p}}); err != nil {
		var exit *ExitError
		if errors.As(err, &exit) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return nil, err
	}
	session, release, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	out, err := session.StdoutPipe()
	if err == nil {
		err = begin(ctx, session, "exec cat -- "+Quote(p))
	}
	if err != nil {
		session.Close()
		release()
		return nil, err
	}
	return sessionReader{Reader: out, session: session, release: release}, nil
}

func (r *SSHRunner) MkdirAll(ctx context.Context, p string, mode fs.FileMode) error {
	if err := checkPath(p); err != nil {
		return err
	}
	// The mode is given to every directory that is created, not only the
	// last: a parent left open would expose what is put below it.
	const mkdir = `umask "$1"; mkdir -p -- "$2"`
	umask := fmt.Sprintf("%04o", 0o777&^mode.Perm())
	if _, err := r.Output(ctx, Cmd{Name: "sh", Args: []string{"-c", mkdir, "sh", umask, p}}); err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	return nil
}

func (r *SSHRunner) remove(ctx context.Context, p string) error {
	_, err := r.Output(ctx, Cmd{Name: "rm", Args: []string{"-rf", "--", p}})
	return err
}

func (r *SSHRunner) RemoveAll(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	if err := r.remove(ctx, p); err != nil {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

// Dial opens a connection from the server's side: to a port on its own
// loopback interface, for example.
func (r *SSHRunner) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return r.client.DialContext(ctx, network, address)
}
