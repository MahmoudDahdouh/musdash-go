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
	gone := []string{write("work/dep123/checkout/deploy-key"), write("work/.env-abcdef"), write("backups/db1/.rclone-123.env")}
	kept := []string{write("backups/db1/2026-01-01.sql.gz"), write("apps/app1/.env"), write("proxy/routes.json")}

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
