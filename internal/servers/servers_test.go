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
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
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

func TestCheckRecordsWhatItFinds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rep, err := e.pool.Check(ctx, e.server)
	if err != nil {
		t.Fatal(err)
	}
	item := func(name string) Item {
		for _, it := range rep.Items {
			if it.Name == name {
				return it
			}
		}
		return Item{}
	}
	if !item("Connection").OK || !rep.NewHostKey || !strings.HasPrefix(rep.Fingerprint, "SHA256:") {
		t.Fatalf("%+v", rep)
	}
	if it := item("Data directory"); !it.OK || it.Detail != e.server.DataDir {
		t.Fatalf("%+v", it)
	}
	for _, sub := range []string{"apps", "work", "backups", "proxy", "bin"} {
		info, err := os.Stat(filepath.Join(e.server.DataDir, sub))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", sub, info, err)
		}
	}
	got, _ := e.db.ServerByID(ctx, e.server.ID)
	if got.HostKey == "" || got.CheckedAt == 0 || got.Arch == "" || got.IP != "127.0.0.1" {
		t.Fatalf("stored: %+v", got)
	}
	// This machine is the "server": what it lacks decides the status.
	if rep.OK() != (got.Status == db.ServerOK) || (!rep.OK() && got.StatusDetail == "") {
		t.Fatalf("status %q (%q) for a report that is ok=%v", got.Status, got.StatusDetail, rep.OK())
	}

	// A second check is not a first contact.
	rep, err = e.pool.Check(ctx, got)
	if err != nil || rep.NewHostKey {
		t.Fatalf("second check: new key %v, %v", rep.NewHostKey, err)
	}

	// A server that does not answer is recorded as unreachable, with why.
	e.srv.Close()
	e.pool.Forget(got.ID)
	rep, err = e.pool.Check(ctx, got)
	if err != nil || rep.OK() {
		t.Fatalf("a server that is down: ok=%v %v", rep.OK(), err)
	}
	down, _ := e.db.ServerByID(ctx, e.server.ID)
	if down.Status != db.ServerUnreachable || !strings.Contains(down.StatusDetail, "nothing answers on that port") {
		t.Fatalf("%q %q", down.Status, down.StatusDetail)
	}

	// Something else at the address: said in so many words, key kept.
	other := sshtest.Start(t)
	e.db.Exec(`UPDATE servers SET port = ? WHERE id = ?`, other.Port, e.server.ID)
	moved, _ := e.db.ServerByID(ctx, e.server.ID)
	rep, _ = e.pool.Check(ctx, moved)
	if rep.OK() || !strings.Contains(rep.Problem(), "host key is not the one recorded") {
		t.Fatalf("a changed host key: %q", rep.Problem())
	}
	if after, _ := e.db.ServerByID(ctx, e.server.ID); after.HostKey != got.HostKey {
		t.Fatal("the check replaced the recorded host key")
	}
}

func TestInstallProxy(t *testing.T) {
	ctx := context.Background()
	binary := filepath.Join(t.TempDir(), "musdash")
	os.WriteFile(binary, []byte("\x7fELF the proxy"), 0o755)
	s := db.Server{Kind: db.ServerSSH, SSHUser: "deploy", DataDir: "/home/deploy/.musdash", Arch: "amd64"}

	uid := "1000\n"
	fake := &runnertest.Fake{}
	fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		if line == "id -u" {
			return uid, nil
		}
		return "", nil
	}
	if err := installProxy(ctx, fake, s, binary); err != nil {
		t.Fatal(err)
	}
	copied, mode, _ := fake.File("/home/deploy/.musdash/bin/musdash")
	if copied != "\x7fELF the proxy" || mode != 0o755 {
		t.Fatalf("the binary on the server: %q, mode %o", copied, mode)
	}
	unit, _, _ := fake.File("/home/deploy/.musdash/musdash-proxy.service")
	for _, want := range []string{"User=deploy\n", "Environment=MUSDASH_DATA=/home/deploy/.musdash\n", "ExecStart=/home/deploy/.musdash/bin/musdash proxy\n", "AmbientCapabilities=CAP_NET_BIND_SERVICE\n", "Restart=always\n"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit is missing %q:\n%s", want, unit)
		}
	}
	if routes, _, ok := fake.File("/home/deploy/.musdash/proxy/routes.json"); !ok || !strings.Contains(routes, `"routes":[]`) {
		t.Fatalf("the first routes file: %q", routes)
	}
	all := "\n" + strings.Join(fake.Calls(), "\n") + "\n"
	order := []string{
		"\nsudo -n install -m 0644 /home/deploy/.musdash/musdash-proxy.service /etc/systemd/system/musdash-proxy.service\n",
		"\nsudo -n systemctl daemon-reload\n",
		"\nsudo -n systemctl enable musdash-proxy.service\n",
		"\nsudo -n systemctl restart musdash-proxy.service\n",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(all[at:], want)
		if i < 0 {
			t.Fatalf("%q is missing or out of order in:%s", strings.TrimSpace(want), all)
		}
		at += i
	}

	// As root nothing goes through sudo, and routes already there stay.
	uid = "0\n"
	root := s
	root.SSHUser, root.DataDir = "root", "/var/lib/musdash"
	fake2 := &runnertest.Fake{Handle: fake.Handle}
	fake2.PutFile("/var/lib/musdash/proxy/routes.json", `{"routes":[{"host":"a.example.com"}]}`)
	if err := installProxy(ctx, fake2, root, binary); err != nil {
		t.Fatal(err)
	}
	if calls := strings.Join(fake2.Calls(), "\n"); strings.Contains(calls, "sudo") || !strings.Contains(calls, "systemctl restart musdash-proxy.service") {
		t.Fatalf("as root: %s", calls)
	}
	if routes, _, _ := fake2.File("/var/lib/musdash/proxy/routes.json"); !strings.Contains(routes, "a.example.com") {
		t.Fatal("an install replaced the routes that were there")
	}

	// A user without sudo is told what is missing.
	uid = "1000\n"
	fake3 := &runnertest.Fake{Handle: func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "sudo -n install") {
			return "", runnertest.Exit("sudo", 1, "sudo: a password is required")
		}
		return fake.Handle(line, c)
	}}
	if err := installProxy(ctx, fake3, s, binary); err == nil || !strings.Contains(err.Error(), "cannot use sudo without a password") {
		t.Fatalf("no sudo: %v", err)
	}

	// Names that could add a line to the unit file are refused before
	// anything is written, as is a server that was never checked.
	pool := New()
	for _, bad := range []db.Server{
		{Kind: db.ServerSSH, SSHUser: "deploy\nExecStartPre=/bin/evil", DataDir: "/srv/m", Arch: "amd64"},
		{Kind: db.ServerSSH, SSHUser: "deploy", DataDir: "/srv/m\nUser=root", Arch: "amd64"},
		{Kind: db.ServerSSH, SSHUser: "deploy", DataDir: "relative/dir", Arch: "amd64"},
		{Kind: db.ServerSSH, SSHUser: "deploy", DataDir: "/srv/m", Arch: ""},
		{Kind: db.ServerLocal},
	} {
		if err := pool.InstallProxy(ctx, bad, t.TempDir()); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// What a server says its architecture is ends up in a file name on
	// this machine. A server that answers with a path gets nothing.
	secretFile := filepath.Join(t.TempDir(), "master.key")
	os.WriteFile(secretFile, []byte("the master key"), 0o600)
	for _, answer := range []string{"../../../../../../.." + secretFile, "/../.." + secretFile, "amd64/../../x", "x86_64\nsomething", ""} {
		if got := archOf(answer); got != "" {
			t.Errorf("archOf(%q) = %q", answer, got)
		}
		if path, err := ProxyBinary(filepath.Dir(secretFile), answer); err == nil {
			t.Errorf("ProxyBinary accepted the architecture %q and would copy %s", answer, path)
		}
	}
	os.WriteFile(filepath.Join(filepath.Dir(secretFile), "musdash-linux-"), []byte("x"), 0o600)
	if _, err := ProxyBinary(filepath.Dir(secretFile), ""); err == nil {
		t.Error("an empty architecture found a file")
	}
	for machine, want := range map[string]string{"x86_64": "amd64", "aarch64\n": "arm64", "aarch64": "arm64", " arm64 ": "arm64", "armv7l": "arm", "riscv64": "riscv64", "sparc64": ""} {
		if got := archOf(machine); got != want {
			t.Errorf("archOf(%q) = %q, want %q", machine, got, want)
		}
	}

	// A server of another architecture needs its own binary, and the
	// message says where to put it.
	if _, err := ProxyBinary(t.TempDir(), "riscv64"); err == nil || !strings.Contains(err.Error(), "musdash-linux-riscv64") {
		t.Fatalf("%v", err)
	}
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "musdash-linux-riscv64"), []byte("x"), 0o755)
	if got, err := ProxyBinary(dist, "riscv64"); err != nil || got != filepath.Join(dist, "musdash-linux-riscv64") {
		t.Fatalf("%q %v", got, err)
	}
}

// Checking a server must not cut what is running on it: a deployment, a
// backup, a restore that is half applied.
func TestCheckLeavesRunningCommandsAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Check(ctx, e.server); err != nil {
		t.Fatal(err)
	}
	r := e.runner()
	done := make(chan error, 1)
	go func() {
		_, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", "sleep 1; echo finished"}})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	s, _ := e.db.ServerByID(ctx, e.server.ID)
	if _, err := e.pool.Check(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a check ended a running command: %v", err)
	}
}

// What a dead process left on a server is removed by the first connection
// the next one makes: a checkout may hold a deploy key, and the files with
// a command's environment or a storage's keys hold secrets.
func TestFirstConnectionClearsLeftovers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	data := e.server.DataDir
	write := func(rel string) string {
		p := filepath.Join(data, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	gone := []string{write("work/dep123/checkout/deploy-key"), write("work/.env-abcdef"), write("backups/db1/.rclone-123.env"),
		// What was being written when the last process died, or its
		// connection dropped: a backup, an app's variables, the routes, the
		// proxy's binary.
		write("backups/db1/.musdash-0123456789abcdef"), write("apps/app1/.musdash-0123456789abcdef"),
		write("proxy/.musdash-0123456789abcdef"), write("bin/.musdash-0123456789abcdef")}
	kept := []string{write("backups/db1/2026-01-01.sql.gz"), write("apps/app1/.env"), write("apps/app1/env"), write("proxy/routes.json"), write("bin/musdash"),
		// Deeper than musdash writes: a file an app's own mount holds.
		write("apps/app1/files/conf/.musdash-notours")}

	if _, err := e.pool.Check(ctx, e.server); err != nil {
		t.Fatal(err)
	}
	r := e.runner()
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range gone {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was left on the server", p)
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	// Only once: later connections must not remove what this process put
	// there for work that is going on.
	mine := write("work/dep456/checkout/file")
	e.srv.DropConnections()
	r.Output(ctx, runner.Cmd{Name: "true"})
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("a reconnection removed a build directory in use")
	}
}

// A server that hangs must not hold up anything but its own commands:
// forgetting it, and closing idle connections of the others, still work.
func TestAHungServerHoldsNothingElse(t *testing.T) {
	defer runner.SetReplyWithinForTest(300 * time.Millisecond)()
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Check(ctx, e.server); err != nil {
		t.Fatal(err)
	}
	r := e.runner()
	if _, err := r.Output(ctx, runner.Cmd{Name: "true"}); err != nil {
		t.Fatal(err)
	}
	e.srv.Silence(sshtest.Sessions, sshtest.Keepalive)
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		r.Output(short, runner.Cmd{Name: "true"})
		close(finished)
	}()
	// While that command is finding out that the server is gone:
	time.Sleep(250 * time.Millisecond)
	ok := make(chan struct{})
	go func() {
		e.pool.CloseIdle()
		e.pool.Forget(e.server.ID)
		close(ok)
	}()
	select {
	case <-ok:
	case <-time.After(3 * time.Second):
		t.Fatal("the pool was held by a server that does not answer")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a command on a hung server never ended")
	}
}

func itemOf(rep Report, name string) Item {
	for _, it := range rep.Items {
		if it.Name == name {
			return it
		}
	}
	return Item{}
}

// An sshd with AllowTcpForwarding no signs the account in and runs every
// command, so the check said the server was ready, and then every
// deployment of an app failed after its whole health timeout.
func TestCheckSaysWhenSSHDDoesNotForward(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, how string
		ok        bool
	}{
		{"an sshd that forwards", "", true},
		{"an sshd that does not", sshtest.Refuse, false},
		// Up to 7.4 a port that nothing listens on is answered as if it
		// were forbidden. Such a server forwards, and must not be told
		// that it does not.
		{"an old sshd that forwards", sshtest.LikeOldSSHD, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.srv.Forward(c.how)
			rep, err := e.pool.Check(ctx, e.server)
			if err != nil {
				t.Fatal(err)
			}
			it := itemOf(rep, "Forwarding")
			if it.OK != c.ok || !it.Needed || it.Detail == "" {
				t.Fatalf("%+v", it)
			}
			got, _ := e.db.ServerByID(ctx, e.server.ID)
			if c.ok {
				if strings.Contains(got.StatusDetail, "Forwarding") {
					t.Fatalf("stored: %q", got.StatusDetail)
				}
				return
			}
			for _, want := range []string{"AllowTcpForwarding", "authorized_keys", "restart sshd"} {
				if !strings.Contains(it.Detail, want) {
					t.Errorf("the advice does not mention %q: %s", want, it.Detail)
				}
			}
			if rep.OK() || got.Status != db.ServerProblem {
				t.Fatalf("a server that cannot be deployed to is %q, report ok=%v", got.Status, rep.OK())
			}
			// Everything else about the server was still looked at.
			if !itemOf(rep, "Data directory").OK || itemOf(rep, "Proxy install").Name == "" {
				t.Fatalf("%+v", rep.Items)
			}
		})
	}
}

// A data directory that is somebody else's: musdash would make its
// directories in it, and the first connection of a process empties the one
// named work.
func TestCheckLeavesSomebodyElsesDirectoryAlone(t *testing.T) {
	ctx := context.Background()
	theirs := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"notes.txt", "work/thesis.tex"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("theirs"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	untouched := func(dir string) {
		t.Helper()
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if strings.Join(names, " ") != "notes.txt work" {
			t.Fatalf("the directory now holds: %v", names)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "work", "thesis.tex")); err != nil || string(raw) != "theirs" {
			t.Fatalf("a file in their work directory: %q %v", raw, err)
		}
	}

	t.Run("on a first check", func(t *testing.T) {
		e := newEnv(t)
		theirs(e.server.DataDir)
		rep, err := e.pool.Check(ctx, e.server)
		if err != nil {
			t.Fatal(err)
		}
		it := itemOf(rep, "Data directory")
		if it.OK || !it.Needed || !strings.Contains(it.Detail, "already has other things in it") || !strings.Contains(it.Detail, e.server.DataDir+"/musdash") {
			t.Fatalf("%+v", it)
		}
		if rep.OK() || !itemOf(rep, "Connection").OK || rep.NewHostKey {
			t.Fatalf("%+v", rep)
		}
		untouched(e.server.DataDir)
		// The server stays one that nothing but a check talks to: its key
		// was not recorded, so no deployment gets as far as sweeping it.
		got, _ := e.db.ServerByID(ctx, e.server.ID)
		if got.HostKey != "" || got.Status != db.ServerProblem || !strings.Contains(got.StatusDetail, "other things") {
			t.Fatalf("stored: key %q, %s %q", got.HostKey, got.Status, got.StatusDetail)
		}
		if _, err := e.runner().Output(ctx, runner.Cmd{Name: "true"}); !errors.Is(err, ErrNotChecked) {
			t.Fatalf("a command on the server: %v", err)
		}
		untouched(e.server.DataDir)

		// Emptied by its owner, it is taken.
		os.RemoveAll(e.server.DataDir)
		os.MkdirAll(e.server.DataDir, 0o755)
		if rep, _ = e.pool.Check(ctx, e.server); !itemOf(rep, "Data directory").OK || !rep.NewHostKey {
			t.Fatalf("an empty directory: %+v", rep.Items)
		}
	})

	t.Run("on a later check", func(t *testing.T) {
		e := newEnv(t)
		if rep, err := e.pool.Check(ctx, e.server); err != nil || !itemOf(rep, "Data directory").OK {
			t.Fatalf("a directory that is not there yet: %+v %v", rep.Items, err)
		}
		// With what musdash made in it, and more: still its own.
		os.WriteFile(filepath.Join(e.server.DataDir, "README"), []byte("x"), 0o644)
		checked, _ := e.db.ServerByID(ctx, e.server.ID)
		if rep, _ := e.pool.Check(ctx, checked); !itemOf(rep, "Data directory").OK {
			t.Fatalf("a directory musdash uses: %+v", rep.Items)
		}
		// Replaced by something else since.
		os.RemoveAll(e.server.DataDir)
		theirs(e.server.DataDir)
		rep, _ := e.pool.Check(ctx, checked)
		if it := itemOf(rep, "Data directory"); it.OK || !strings.Contains(it.Detail, "other things") {
			t.Fatalf("%+v", it)
		}
		untouched(e.server.DataDir)
		// The rest of the check was still made.
		if itemOf(rep, "Forwarding").Name == "" {
			t.Fatalf("%+v", rep.Items)
		}
	})
}

// With three host keys on a server, the kind says which one a fingerprint
// is to be compared with.
func TestHostKeyKind(t *testing.T) {
	for kind, want := range map[string][2]string{
		sshtest.ED25519: {"ED25519", "/etc/ssh/ssh_host_ed25519_key.pub"},
		sshtest.ECDSA:   {"ECDSA", "/etc/ssh/ssh_host_ecdsa_key.pub"},
		sshtest.RSA:     {"RSA", "/etc/ssh/ssh_host_rsa_key.pub"},
	} {
		line := HostKeyLine(sshtest.NewHostKey(t, kind).PublicKey())
		if got := [2]string{HostKeyKind(line), HostKeyFile(line)}; got != want {
			t.Errorf("%s: %v", kind, got)
		}
	}
	for _, line := range []string{"", "not a key", "ssh-ed25519 AAAA"} {
		if HostKeyKind(line) != "" || HostKeyFile(line) != "" {
			t.Errorf("%q has a kind", line)
		}
	}
	// What a check reports for the server it reached.
	e := newEnv(t)
	rep, err := e.pool.Check(context.Background(), e.server)
	if err != nil {
		t.Fatal(err)
	}
	if rep.HostKeyKind != "ED25519" || rep.HostKeyFile != "/etc/ssh/ssh_host_ed25519_key.pub" || rep.Fingerprint != ssh.FingerprintSHA256(e.srv.HostKey) {
		t.Fatalf("%+v", rep)
	}
}

func TestDataDirProblem(t *testing.T) {
	for _, dir := range []string{
		"/var/lib/musdash", "/home/deploy/.musdash", "/root/.musdash", "/srv/musdash", "/opt/musdash", "/data", "/data/musdash",
		"/mnt/volume1/musdash", "/var/musdash", "/home/deploy/apps/musdash", "/etcetera", "/usrdata/musdash", "/var/lib/docker-data",
		DefaultDataDir("root"), DefaultDataDir("deploy"),
	} {
		if p := DataDirProblem(dir); p != "" {
			t.Errorf("%s: refused as %s", dir, p)
		}
	}
	for dir, want := range map[string]string{
		"":                          DataDirShape,
		"/":                         DataDirShape,
		"relative":                  DataDirShape,
		"/srv/musdash/":             DataDirShape,
		"/srv//musdash":             DataDirShape,
		"/srv/../etc":               DataDirShape,
		"/srv/./musdash":            DataDirShape,
		"/srv/my dir":               DataDirShape,
		"/srv/$(reboot)":            DataDirShape,
		"/srv/a;b":                  DataDirShape,
		"/srv/mus\ndash":            DataDirShape,
		"/etc":                      DataDirShared,
		"/etc/musdash":              DataDirShared,
		"/usr/local/data":           DataDirShared,
		"/proc/1":                   DataDirShared,
		"/sys":                      DataDirShared,
		"/dev/shm/x":                DataDirShared,
		"/boot":                     DataDirShared,
		"/bin":                      DataDirShared,
		"/sbin/x":                   DataDirShared,
		"/lib":                      DataDirShared,
		"/lib64/x":                  DataDirShared,
		"/run/musdash":              DataDirShared,
		"/var/run/x":                DataDirShared,
		"/tmp/musdash":              DataDirShared,
		"/var/tmp/x":                DataDirShared,
		"/var/lib/docker":           DataDirShared,
		"/var/lib/docker/volumes/x": DataDirShared,
		"/var/lib/containerd":       DataDirShared,
		"/var":                      DataDirShared,
		"/var/lib":                  DataDirShared,
		"/var/log":                  DataDirShared,
		"/home":                     DataDirShared,
		"/home/deploy":              DataDirShared,
		"/root":                     DataDirShared,
		"/opt":                      DataDirShared,
		"/srv":                      DataDirShared,
		"/mnt":                      DataDirShared,
		"/media":                    DataDirShared,
		"/root/.ssh":                DataDirShared,
		"/home/deploy/.ssh/musdash": DataDirShared,
	} {
		if got := DataDirProblem(dir); got != want {
			t.Errorf("%q: %q, want %q", dir, got, want)
		}
	}
	if DataDirProblem("/"+strings.Repeat("a", 200)) != "" || DataDirProblem("/"+strings.Repeat("a", 201)) != DataDirShape {
		t.Error("the length limit moved")
	}
}

// Whose a directory is cannot be told from names such as apps and work:
// other people have directories called that. A check leaves a mark, and
// both the look and the sweep go by it.
func TestADataDirectoryIsKnownByItsMark(t *testing.T) {
	ctx := context.Background()
	put := func(dir string, names ...string) {
		t.Helper()
		for _, name := range names {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("theirs"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	there := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	t.Run("a check leaves it", func(t *testing.T) {
		e := newEnv(t)
		if rep, err := e.pool.Check(ctx, e.server); err != nil || !itemOf(rep, "Data directory").OK {
			t.Fatalf("%+v %v", rep.Items, err)
		}
		info, err := os.Stat(filepath.Join(e.server.DataDir, ownedMark))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the mark: %v %v", info, err)
		}
		// With the mark and nothing else of musdash's, the directory is
		// still musdash's: a check that got no further than the mark.
		for _, sub := range subDirs[1:] {
			os.RemoveAll(filepath.Join(e.server.DataDir, sub))
		}
		checked, _ := e.db.ServerByID(ctx, e.server.ID)
		if rep, _ := e.pool.Check(ctx, checked); !itemOf(rep, "Data directory").OK || !there(filepath.Join(e.server.DataDir, "apps")) {
			t.Fatalf("a marked directory: %+v", rep.Items)
		}
	})

	t.Run("somebody's own apps and work", func(t *testing.T) {
		e := newEnv(t)
		put(e.server.DataDir, "apps/shop/index.php", "work/thesis.tex", "notes.txt")
		rep, err := e.pool.Check(ctx, e.server)
		if err != nil {
			t.Fatal(err)
		}
		if it := itemOf(rep, "Data directory"); it.OK || !strings.Contains(it.Detail, "other things") {
			t.Fatalf("%+v", it)
		}
		got, _ := e.db.ServerByID(ctx, e.server.ID)
		if got.HostKey != "" || !there(filepath.Join(e.server.DataDir, "work/thesis.tex")) || there(filepath.Join(e.server.DataDir, "proxy")) || there(filepath.Join(e.server.DataDir, ownedMark)) {
			t.Fatalf("key %q; the directory was changed", got.HostKey)
		}
	})

	t.Run("a directory that cannot be looked into", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root can look into anything")
		}
		e := newEnv(t)
		put(e.server.DataDir, "work/thesis.tex")
		os.Chmod(e.server.DataDir, 0o300)
		defer os.Chmod(e.server.DataDir, 0o700)
		rep, err := e.pool.Check(ctx, e.server)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := e.db.ServerByID(ctx, e.server.ID)
		if itemOf(rep, "Data directory").OK || got.HostKey != "" {
			t.Fatalf("%+v, key %q", rep.Items, got.HostKey)
		}
		os.Chmod(e.server.DataDir, 0o700)
		if !there(filepath.Join(e.server.DataDir, "work/thesis.tex")) || there(filepath.Join(e.server.DataDir, "apps")) {
			t.Fatal("the directory was changed")
		}
	})

	// A check is not the only way to a server: a row from before there
	// was that look, or a directory replaced since, is swept by the first
	// connection of the next process without anybody choosing Check.
	t.Run("the sweep leaves an unmarked directory alone", func(t *testing.T) {
		e := newEnv(t)
		put(e.server.DataDir, "work/thesis.tex", "apps/shop/.musdash-0123456789abcdef", "proxy/.musdash-0123456789abcdef")
		first, _, err := e.pool.FirstContact(ctx, e.server)
		if err != nil {
			t.Fatal(err)
		}
		first.Close()
		if _, err := e.runner().Output(ctx, runner.Cmd{Name: "true"}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"work/thesis.tex", "apps/shop/.musdash-0123456789abcdef", "proxy/.musdash-0123456789abcdef"} {
			if !there(filepath.Join(e.server.DataDir, name)) {
				t.Errorf("%s was removed from a directory that is not musdash's", name)
			}
		}
	})
	t.Run("and sweeps a marked one", func(t *testing.T) {
		e := newEnv(t)
		put(e.server.DataDir, "work/dep123/checkout/deploy-key", ownedMark)
		first, _, err := e.pool.FirstContact(ctx, e.server)
		if err != nil {
			t.Fatal(err)
		}
		first.Close()
		if _, err := e.runner().Output(ctx, runner.Cmd{Name: "true"}); err != nil {
			t.Fatal(err)
		}
		if there(filepath.Join(e.server.DataDir, "work/dep123")) || !there(filepath.Join(e.server.DataDir, ownedMark)) {
			t.Fatal("a build's leftovers are still there, or the mark is gone")
		}
	})
}

// A look that got no answer it understands is not a directory that may be
// used.
func TestALookThatFailsIsNotAnAnswer(t *testing.T) {
	ctx := context.Background()
	for answer, want := range map[string]struct {
		taken bool
		fails bool
	}{
		"new\n": {false, false}, "ours\n": {false, false}, "empty\n": {false, false}, "taken\n": {true, false},
		"": {false, true}, "yes\n": {false, true}, "taken and more\n": {false, true},
	} {
		fake := &runnertest.Fake{Handle: func(string, runner.Cmd) (string, error) { return answer, nil }}
		taken, err := dataDirTaken(ctx, fake, "/srv/musdash")
		if taken != want.taken || (err != nil) != want.fails {
			t.Errorf("answer %q: taken=%v err=%v", answer, taken, err)
		}
	}
	fake := &runnertest.Fake{Handle: func(string, runner.Cmd) (string, error) { return "", runnertest.Exit("sh", 127, "sh: not found") }}
	if _, err := dataDirTaken(ctx, fake, "/srv/musdash"); err == nil {
		t.Error("a command that failed was taken for an answer")
	}
	// The directory is an argument of the script and never part of it.
	var line string
	fake = &runnertest.Fake{Handle: func(l string, c runner.Cmd) (string, error) {
		line = c.Args[len(c.Args)-1]
		if strings.Contains(c.Args[1], "/srv/it's; reboot") {
			t.Error("the directory is in the script's text")
		}
		return "new\n", nil
	}}
	dataDirTaken(ctx, fake, "/srv/it's; reboot")
	if line != "/srv/it's; reboot" {
		t.Errorf("the directory arrived as %q", line)
	}
}
