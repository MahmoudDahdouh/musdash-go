// Package servers hands out the Runner for a server. Everything that touches
// a machine asks here, so the rest of the code never needs to know whether a
// server is this machine or one reached over SSH.
package servers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/MahmoudDahdouh/musdash-go/internal/backup"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// idleAfter is how long a connection nothing uses is kept.
const idleAfter = 5 * time.Minute

// ErrNotChecked is returned for a server whose host key was never recorded.
var ErrNotChecked = errors.New("this server has not been checked yet: open Servers and choose Check, so that its host key is recorded")

// Pool returns runners for servers. A remote server has one SSH connection,
// opened when first needed, shared by everything that talks to the server,
// and closed once nothing has used it for a while.
type Pool struct {
	local runner.Runner
	// DB and Box give the pool a server's key and host key. Without them
	// only the local server can be reached.
	DB  *db.DB
	Box *secret.Box

	idle time.Duration

	mu    sync.Mutex
	conns map[string]*conn
	// swept holds the servers whose leftovers were cleared: once per
	// server, on the first connection this process makes to it.
	swept map[string]bool
}

// conn is one server's connection and who is using it.
type conn struct {
	// dial serialises connecting, so that two callers that find no
	// connection open one between them.
	dial sync.Mutex

	mu       sync.Mutex
	r        *runner.SSHRunner
	inUse    int
	lastUsed time.Time
}

// New returns a pool that can reach the local machine.
func New() *Pool {
	return &Pool{local: runner.NewLocal(), idle: idleAfter, conns: map[string]*conn{}}
}

// NewWith returns a pool whose local server is reached through r. Tests use
// it to script what the server answers.
func NewWith(r runner.Runner) *Pool {
	return &Pool{local: r, idle: idleAfter, conns: map[string]*conn{}}
}

// Runner returns the Runner for a server. For a remote server nothing is
// connected yet: the connection is made by the first command.
func (p *Pool) Runner(_ context.Context, s db.Server) (runner.Runner, error) {
	switch s.Kind {
	case db.ServerLocal:
		return p.local, nil
	case db.ServerSSH:
		if p.DB == nil || p.Box == nil {
			return nil, fmt.Errorf("server %s: remote servers are not set up in this process", s.Name)
		}
		return &remote{p: p, id: s.ID, dataDir: s.DataDir}, nil
	}
	return nil, fmt.Errorf("server %s: unknown kind %q", s.Name, s.Kind)
}

// Docker returns a docker client for a server.
func (p *Pool) Docker(ctx context.Context, s db.Server) (docker.Client, error) {
	r, err := p.Runner(ctx, s)
	if err != nil {
		return docker.Client{}, err
	}
	return docker.Client{R: r}, nil
}

// IsLocal reports whether the server is the machine musdash runs on.
func IsLocal(s db.Server) bool { return s.Kind == db.ServerLocal }

// DefaultDataDir is where musdash keeps its files on a remote server when
// nothing else was chosen: under the system's state directory for root,
// otherwise in the account's home.
func DefaultDataDir(user string) string {
	if user == "root" {
		return "/var/lib/musdash"
	}
	return "/home/" + user + "/.musdash"
}

// HostKeyLine is a host key as it is stored and shown: an authorized_keys
// line without a comment.
func HostKeyLine(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// Fingerprint is the SHA-256 fingerprint of a stored host key, as `ssh`
// prints it, or "" when the line cannot be read.
func Fingerprint(line string) string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(key)
}

// HostKeyKind names the kind of a stored host key as `ssh-keygen -l` does
// (ED25519, ECDSA, RSA), or "" for a kind that has no key file of its own
// to compare with: a certificate, for one.
func HostKeyKind(line string) string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return ""
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519:
		return "ED25519"
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		return "ECDSA"
	case ssh.KeyAlgoRSA:
		return "RSA"
	}
	return ""
}

// HostKeyFile is where an sshd usually keeps the public half of a stored
// host key: the file whose fingerprint is the one to compare. It is "" for
// a kind HostKeyKind does not name.
func HostKeyFile(line string) string {
	kind := HostKeyKind(line)
	if kind == "" {
		return ""
	}
	return "/etc/ssh/ssh_host_" + strings.ToLower(kind) + "_key.pub"
}

// config builds the SSH settings for a server from what is stored.
func (p *Pool) config(ctx context.Context, s db.Server) (runner.SSHConfig, error) {
	cfg := runner.SSHConfig{Host: s.Host, Port: s.Port, User: s.SSHUser, DataDir: s.DataDir}
	if s.DataDir != "" {
		cfg.WorkDir = path.Join(s.DataDir, "work")
	}
	key, err := p.DB.SSHKeyByID(ctx, s.SSHKeyID)
	if err != nil {
		return cfg, fmt.Errorf("server %s: the key musdash signs in with no longer exists", s.Name)
	}
	private, err := p.Box.Open(key.PrivateKey)
	if err != nil {
		return cfg, fmt.Errorf("server %s: its key cannot be decrypted: was the master key changed?", s.Name)
	}
	if cfg.Signer, err = ssh.ParsePrivateKey(private); err != nil {
		return cfg, fmt.Errorf("server %s: its key cannot be read", s.Name)
	}
	if s.HostKey != "" {
		host, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.HostKey))
		if err != nil {
			return cfg, fmt.Errorf("server %s: the recorded host key cannot be read; forget it and check the server again", s.Name)
		}
		cfg.HostKey = host.Marshal()
	}
	return cfg, nil
}

// FirstContact connects to a server for a check. When no host key is
// recorded yet it records the one the server presents; when one is, the
// server must present it. It returns a Runner on that connection, which
// the caller closes, and the server's key.
//
// It looks at nothing on the server before it records the key. Check is
// what the dashboard calls; this is the same contact for tests that need a
// server with a recorded key and nothing else.
func (p *Pool) FirstContact(ctx context.Context, s db.Server) (*runner.SSHRunner, string, error) {
	return p.firstContact(ctx, s, nil)
}

// firstContact is FirstContact with a look at the server before its key is
// recorded. A server whose key is recorded is one that everything else may
// talk to; when look returns an error it is not recorded, and the server
// stays one that only a check reaches.
func (p *Pool) firstContact(ctx context.Context, s db.Server, look func(*runner.SSHRunner) error) (*runner.SSHRunner, string, error) {
	cfg, err := p.config(ctx, s)
	if err != nil {
		return nil, "", err
	}
	if s.HostKey != "" {
		// A key is recorded: this is not a first contact, and only that
		// key will do.
		r, err := runner.DialSSH(ctx, cfg)
		return r, s.HostKey, err
	}
	seen := ""
	cfg.Seen = func(key ssh.PublicKey) error {
		seen = HostKeyLine(key)
		return nil
	}
	r, err := runner.DialSSH(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	if look != nil {
		if err := look(r); err != nil {
			r.Close()
			return nil, seen, err
		}
	}
	recorded, err := p.DB.SetServerHostKey(ctx, s.ID, seen)
	if err == nil && !recorded {
		// Somebody recorded a key in the meantime. Only that one counts.
		current, cerr := p.DB.ServerByID(ctx, s.ID)
		if cerr != nil || current.HostKey != seen {
			err = runner.ErrHostKeyChanged
		}
	}
	if err != nil {
		r.Close()
		return nil, "", err
	}
	return r, seen, nil
}

// acquire returns the server's connection, connecting if there is none or
// the one there has died, and counts the caller as using it.
func (p *Pool) acquire(ctx context.Context, id string) (*conn, *runner.SSHRunner, error) {
	p.mu.Lock()
	c := p.conns[id]
	if c == nil {
		c = &conn{}
		p.conns[id] = c
	}
	p.mu.Unlock()

	c.dial.Lock()
	defer c.dial.Unlock()
	// Found and counted in one step: between the two, the idle sweep could
	// close it.
	c.mu.Lock()
	if r := c.r; r != nil {
		c.inUse++
		c.lastUsed = time.Now()
		c.mu.Unlock()
		return c, r, nil
	}
	c.mu.Unlock()

	// The server as it is now: its address or host key may have changed
	// since the caller loaded it.
	s, err := p.DB.ServerByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if s.HostKey == "" {
		return nil, nil, ErrNotChecked
	}
	cfg, err := p.config(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	r, err := runner.DialSSH(ctx, cfg)
	if err != nil {
		if errors.Is(err, runner.ErrHostKeyChanged) {
			return nil, nil, fmt.Errorf("server %s: %w. If the server was reinstalled, choose Forget host key on its page and check it again", s.Name, err)
		}
		return nil, nil, fmt.Errorf("server %s: %w", s.Name, err)
	}

	p.mu.Lock()
	// Forgotten while connecting: the server's settings changed or it was
	// removed, and this connection was made with the old ones.
	if p.conns[id] != c {
		p.mu.Unlock()
		r.Close()
		return nil, nil, fmt.Errorf("server %s: its settings changed while connecting; try again", s.Name)
	}
	first := !p.swept[id]
	if first {
		if p.swept == nil {
			p.swept = map[string]bool{}
		}
		p.swept[id] = true
	}
	c.mu.Lock()
	c.r = r
	c.inUse++
	c.lastUsed = time.Now()
	c.mu.Unlock()
	p.mu.Unlock()
	if first {
		// Still under c.dial: nothing of this process runs on the server
		// before its leftovers are gone.
		sweep(ctx, r, s.DataDir)
	}
	return c, r, nil
}

// sweep removes what a process that died, or a connection that dropped,
// left on a server: build directories (a checkout may hold a deploy key),
// files with a command's environment, files with a storage's keys, and
// files that were being written and never got their name: a backup as
// large as the dump had got, an app's variables, the routes, a copy of the
// binary. It runs before this process has put anything there, so
// everything it finds is left over. Best effort: a server where it fails
// is no worse off.
//
// It does nothing in a directory that is not musdash's (oursFunc). A check
// refuses such a directory, but a check is not the only way here: a row
// from before there was that look, or a directory replaced since, would
// have its work directory emptied at the next start of the dashboard.
func sweep(ctx context.Context, r *runner.SSHRunner, dataDir string) {
	if dataDir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const clear = oursFunc + `ours "$8" || exit 0
if [ -d "$1" ]; then find "$1" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; fi
if [ -d "$2" ]; then find "$2" -mindepth 2 -maxdepth 2 -type f \( -name "$3" -o -name "$4" \) -exec rm -f -- {} +; fi
if [ -d "$5" ]; then find "$5" -mindepth 2 -maxdepth 2 -type f -name "$4" -exec rm -f -- {} +; fi
for d in "$6" "$7"; do if [ -d "$d" ]; then find "$d" -mindepth 1 -maxdepth 1 -type f -name "$4" -exec rm -f -- {} +; fi; done`
	r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", clear, "sh",
		path.Join(dataDir, "work"), path.Join(dataDir, "backups"), backup.EnvFilePrefix + "*", runner.TempPrefix + "*",
		path.Join(dataDir, "apps"), path.Join(dataDir, "proxy"), path.Join(dataDir, "bin"), dataDir}})
}

// release ends one use. A connection that turned out dead is dropped, so
// the next command connects afresh.
func (p *Pool) release(c *conn, r *runner.SSHRunner, failed bool) {
	c.mu.Lock()
	c.inUse--
	c.lastUsed = time.Now()
	suspect := failed && c.r == r
	c.mu.Unlock()
	// Asked without the lock: a server that hung answers after a while or
	// not at all, and nothing else may wait on that.
	if !suspect || r.Alive() {
		return
	}
	c.mu.Lock()
	dead := c.r == r
	if dead {
		c.r = nil
	}
	c.mu.Unlock()
	if dead {
		r.Close()
	}
}

// Forget closes a server's connection: after its address, key or host key
// changed, or when the server was removed.
func (p *Pool) Forget(id string) {
	p.mu.Lock()
	c := p.conns[id]
	delete(p.conns, id)
	p.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	r := c.r
	c.r = nil
	c.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// CloseIdle closes connections nothing has used for a while. Run calls it
// once a minute.
func (p *Pool) CloseIdle() {
	p.mu.Lock()
	list := make([]*conn, 0, len(p.conns))
	for _, c := range p.conns {
		list = append(list, c)
	}
	p.mu.Unlock()
	for _, c := range list {
		c.mu.Lock()
		var stale *runner.SSHRunner
		if c.r != nil && c.inUse == 0 && time.Since(c.lastUsed) > p.idle {
			stale, c.r = c.r, nil
		}
		c.mu.Unlock()
		if stale != nil {
			stale.Close()
		}
	}
}

// Run closes idle connections until ctx ends. The ones still open then are
// left for Close: work that is finishing may need them.
func (p *Pool) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.CloseIdle()
		}
	}
}

// Close closes every connection. It is called once nothing uses the pool
// any more.
func (p *Pool) Close() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.conns))
	for id := range p.conns {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.Forget(id)
	}
}

// Connections reports how many servers have an open connection.
func (p *Pool) Connections() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.conns {
		c.mu.Lock()
		if c.r != nil {
			n++
		}
		c.mu.Unlock()
	}
	return n
}

// remote is the Runner of a server reached over SSH. It holds no
// connection itself: every call borrows the pool's.
type remote struct {
	p       *Pool
	id      string
	dataDir string
}

// DataDir is where musdash keeps its files on the server.
func (m *remote) DataDir() string { return m.dataDir }

func (m *remote) Run(ctx context.Context, c runner.Cmd) error {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return err
	}
	err = r.Run(ctx, c)
	m.p.release(conn, r, failedConnection(err))
	return err
}

func (m *remote) Output(ctx context.Context, c runner.Cmd) ([]byte, error) {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return nil, err
	}
	out, err := r.Output(ctx, c)
	m.p.release(conn, r, failedConnection(err))
	return out, err
}

func (m *remote) WriteFile(ctx context.Context, p string, mode fs.FileMode, src io.Reader) error {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return err
	}
	err = r.WriteFile(ctx, p, mode, src)
	m.p.release(conn, r, failedConnection(err))
	return err
}

func (m *remote) MkdirAll(ctx context.Context, p string, mode fs.FileMode) error {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return err
	}
	err = r.MkdirAll(ctx, p, mode)
	m.p.release(conn, r, failedConnection(err))
	return err
}

func (m *remote) RemoveAll(ctx context.Context, p string) error {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return err
	}
	err = r.RemoveAll(ctx, p)
	m.p.release(conn, r, failedConnection(err))
	return err
}

// ReadFile keeps the connection in use until the file is closed.
func (m *remote) ReadFile(ctx context.Context, p string) (io.ReadCloser, error) {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return nil, err
	}
	f, err := r.ReadFile(ctx, p)
	if err != nil {
		m.p.release(conn, r, failedConnection(err))
		return nil, err
	}
	return &heldFile{ReadCloser: f, done: func() { m.p.release(conn, r, false) }}, nil
}

// Dial keeps the connection in use until the forwarded one is closed.
func (m *remote) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return nil, err
	}
	c, err := r.Dial(ctx, network, address)
	if err != nil {
		m.p.release(conn, r, true)
		return nil, err
	}
	return &heldConn{Conn: c, done: func() { m.p.release(conn, r, false) }}, nil
}

// Terminal keeps the connection in use until the terminal is closed.
func (m *remote) Terminal(ctx context.Context, c runner.Cmd, cols, rows int) (runner.Terminal, error) {
	conn, r, err := m.p.acquire(ctx, m.id)
	if err != nil {
		return nil, err
	}
	t, err := r.Terminal(ctx, c, cols, rows)
	if err != nil {
		m.p.release(conn, r, failedConnection(err))
		return nil, err
	}
	return &heldTerminal{Terminal: t, done: func() { m.p.release(conn, r, false) }}, nil
}

type heldTerminal struct {
	runner.Terminal
	once sync.Once
	done func()
}

func (h *heldTerminal) Close() error {
	err := h.Terminal.Close()
	h.once.Do(h.done)
	return err
}

// Close does nothing: the connection is the pool's.
func (m *remote) Close() error { return nil }

// failedConnection reports whether an error may mean the connection is
// gone, as opposed to a command that ran and failed or was cancelled.
func failedConnection(err error) bool {
	if err == nil {
		return false
	}
	var exit *runner.ExitError
	return !errors.As(err, &exit) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, runner.ErrOutputTooLarge)
}

type heldFile struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (h *heldFile) Close() error {
	err := h.ReadCloser.Close()
	h.once.Do(h.done)
	return err
}

type heldConn struct {
	net.Conn
	once sync.Once
	done func()
}

func (h *heldConn) Close() error {
	err := h.Conn.Close()
	h.once.Do(h.done)
	return err
}
