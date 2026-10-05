package servers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

type env struct {
	t    *testing.T
	db   *db.DB
	box  *secret.Box
	pool *Pool
	team string
	srv  *sshtest.Server
	// server is the row for srv, with its data directory in a temp dir.
	server db.Server
}

// newEnv sets up a database with one remote server that is really an SSH
// server inside the test, signing in with a key generated here.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_, team, err := d.CreateFirstUser(ctx, "o@example.com", "O", "hash")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(secret.RandomBytes(secret.KeySize))
	e := &env{t: t, db: d, box: box, team: team}
	e.pool = New()
	e.pool.DB, e.pool.Box = d, box
	t.Cleanup(func() {
		for id := range e.pool.conns {
			e.pool.Forget(id)
		}
	})

	// The server accepts exactly the key stored for it.
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(private)
	e.srv = sshtest.StartWithKey(t, signer)
	sealed, _ := box.Seal(pem.EncodeToMemory(block))
	key, err := d.CreateSSHKey(ctx, team, "server key", HostKeyLine(signer.PublicKey()), sealed)
	if err != nil {
		t.Fatal(err)
	}
	e.server, err = d.CreateServer(ctx, db.Server{TeamID: team, Name: "second", Host: e.srv.Host, Port: e.srv.Port, SSHUser: "deploy", SSHKeyID: key.ID, DataDir: filepath.Join(t.TempDir(), "remote-data")})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) runner() runner.Runner {
	e.t.Helper()
	s, err := e.db.ServerByID(context.Background(), e.server.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := e.pool.Runner(context.Background(), s)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestRemoteServerNeedsItsHostKeyFirst(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Before a check, nothing is sent to whoever answers at the address.
	if _, err := e.runner().Output(ctx, runner.Cmd{Name: "true"}); !errors.Is(err, ErrNotChecked) {
		t.Fatalf("a command on a server never checked: %v", err)
	}
	if len(e.srv.Commands()) != 0 {
		t.Fatal("something ran on a server whose key was never seen")
	}

	// First contact records the key.
	first, line, err := e.pool.FirstContact(ctx, e.server)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	got, _ := e.db.ServerByID(ctx, e.server.ID)
	if got.HostKey != line || got.HostKey != HostKeyLine(e.srv.HostKey) || !strings.HasPrefix(Fingerprint(got.HostKey), "SHA256:") {
		t.Fatalf("recorded %q", got.HostKey)
	}

	// From then on commands run, on one shared connection.
	r := e.runner()
	for range 3 {
		out, err := r.Output(ctx, runner.Cmd{Name: "echo", Args: []string{"over ssh"}})
		if err != nil || strings.TrimSpace(string(out)) != "over ssh" {
			t.Fatalf("%q %v", out, err)
		}
	}
	if e.pool.Connections() != 1 || e.srv.Accepted() != 2 { // first contact, then the pooled one
		t.Fatalf("%d pooled connections, %d accepted by the server", e.pool.Connections(), e.srv.Accepted())
	}
	if dir := runner.DataDirOf(r); dir != e.server.DataDir {
		t.Fatalf("data directory of the runner: %q", dir)
	}

	// A different machine answering at the address is not talked to.
	other := sshtest.Start(t)
	e.db.Exec(`UPDATE servers SET port = ? WHERE id = ?`, other.Port, e.server.ID)
	e.pool.Forget(e.server.ID)
	_, err = e.runner().Output(ctx, runner.Cmd{Name: "true"})
	if !errors.Is(err, runner.ErrHostKeyChanged) || !strings.Contains(err.Error(), "Forget host key") {
		t.Fatalf("a changed host key: %v", err)
	}
	if len(other.Commands()) != 0 {
		t.Fatal("something ran on the machine with the wrong key")
	}
	// A first contact does not quietly replace a recorded key either.
	moved, _ := e.db.ServerByID(ctx, e.server.ID)
	if _, _, err := e.pool.FirstContact(ctx, moved); !errors.Is(err, runner.ErrHostKeyChanged) {
		t.Fatalf("first contact with another machine, with a key already recorded: %v", err)
	}
	after, _ := e.db.ServerByID(ctx, e.server.ID)
	if after.HostKey != got.HostKey {
		t.Fatal("the recorded host key was changed")
	}
}

func TestPoolReconnectsAndClosesIdleConnections(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _, err := e.pool.FirstContact(ctx, e.server)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	r := e.runner()
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); err != nil {
		t.Fatal(err)
	}

	// The connection is cut: one command may fail; the next has a new one.
	e.srv.DropConnections()
	r.Output(ctx, runner.Cmd{Name: "true"})
	out, err := r.Output(ctx, runner.Cmd{Name: "echo", Args: []string{"back"}})
	if err != nil || strings.TrimSpace(string(out)) != "back" {
		t.Fatalf("after the connection dropped: %q %v", out, err)
	}

	// A command that fails is not a dead connection.
	accepted := e.srv.Accepted()
	r.Output(ctx, runner.Cmd{Name: "false"})
	r.Output(ctx, runner.Cmd{Name: "true"})
	if e.srv.Accepted() != accepted {
		t.Fatal("a failed command made the pool reconnect")
	}

	// Idle connections are closed; ones in use are not.
	e.pool.idle = 0
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c)
		}
	}()
	held, err := r.Dial(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	e.pool.CloseIdle()
	if e.pool.Connections() != 1 {
		t.Fatal("a connection that carries a forwarded connection was closed as idle")
	}
	held.Close()
	time.Sleep(10 * time.Millisecond)
	e.pool.CloseIdle()
	if e.pool.Connections() != 0 {
		t.Fatal("an idle connection was kept")
	}
	// And the next command simply connects again.
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteFilesGoToTheServersDataDirectory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _, err := e.pool.FirstContact(ctx, e.server)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	r := e.runner()
	dir := filepath.Join(e.server.DataDir, "apps", "x")
	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile(ctx, filepath.Join(dir, "env"), 0o600, strings.NewReader("A=1\n")); err != nil {
		t.Fatal(err)
	}
	f, err := r.ReadFile(ctx, filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(f)
	f.Close()
	if string(raw) != "A=1\n" {
		t.Fatalf("%q", raw)
	}
	// A command with an environment leaves nothing in the work directory.
	out, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", `printf %s "$TOKEN"`}, Env: []string{"TOKEN=s3cret"}})
	if err != nil || string(out) != "s3cret" {
		t.Fatalf("%q %v", out, err)
	}
	if left, _ := filepath.Glob(filepath.Join(e.server.DataDir, "work", ".env-*")); len(left) != 0 {
		t.Fatalf("left in the work directory: %v", left)
	}
	if _, err := os.Stat(filepath.Join(e.server.DataDir, "work")); err != nil {
		t.Fatal("the work directory was not made under the server's data directory")
	}
	if err := r.RemoveAll(ctx, dir); err != nil {
		t.Fatal(err)
	}
}

func TestLocalServerAndUnknownKinds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	local, err := e.db.EnsureLocalServer(ctx, e.team, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.pool.Runner(ctx, local)
	if err != nil || runner.DataDirOf(r) != "" {
		t.Fatalf("the local runner: %v, data directory %q", err, runner.DataDirOf(r))
	}
	if _, err := e.pool.Runner(ctx, db.Server{Kind: "carrier-pigeon"}); err == nil {
		t.Fatal("an unknown kind of server got a runner")
	}
	// A pool without a database cannot reach remote servers, and says so.
	if _, err := New().Runner(ctx, e.server); err == nil {
		t.Fatal("a pool with no database handed out a remote runner")
	}
	if DefaultDataDir("root") != "/var/lib/musdash" || DefaultDataDir("deploy") != "/home/deploy/.musdash" {
		t.Fatal("default data directories")
	}
}
