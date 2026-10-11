package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/sshtest"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
)

// remoteServer adds a server through the form and starts an SSH server
// that accepts the key musdash made for it, then points the row at it.
func (a *app) remoteServer(name string) (db.Server, *sshtest.Server) {
	a.t.Helper()
	ctx := context.Background()
	res, body := a.post("/servers", "/servers", url.Values{"name": {name}, "host": {"198.51.100.20"}, "port": {"22"}, "ssh_user": {"deploy"}, "key": {"new"}, "data_dir": {"/srv/musdash"}})
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("add server: %d\n%s", res.StatusCode, body)
	}
	// The next step, installing the key, is on the new server's page.
	if to := res.Header.Get("Location"); !regexp.MustCompile(`^/servers/[a-z][a-z2-7]{11}$`).MatchString(to) {
		a.t.Fatalf("adding a server leads to %q", to)
	}
	list, _ := a.db.ListServers(ctx, firstTeam(a.t, a))
	var server db.Server
	for _, s := range list {
		if s.Name == name {
			server = s
		}
	}
	if server.ID == "" {
		a.t.Fatal("the server was not stored")
	}
	key, err := a.db.SSHKeyByID(ctx, server.SSHKeyID)
	if err != nil {
		a.t.Fatal(err)
	}
	private, _ := a.server.Box.Open(key.PrivateKey)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		a.t.Fatal(err)
	}
	srv := sshtest.StartWithKey(a.t, signer)
	// The "server" is this machine, so its data directory is put where a
	// test may write: a place the form would refuse on a real server.
	a.db.Exec(`UPDATE servers SET host = ?, port = ?, data_dir = ? WHERE id = ?`, srv.Host, srv.Port, a.t.TempDir()+"/remote", server.ID)
	a.t.Cleanup(func() { a.server.Pool.Forget(server.ID) })
	server, _ = a.db.ServerByID(ctx, server.ID)
	return server, srv
}

func TestServersPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)

	res, page := a.get("/servers")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "This machine") || !strings.Contains(page, "Add a server") {
		t.Fatal("the page without remote servers")
	}

	// What will be put on a command line or in a service file is checked.
	good := url.Values{"name": {"Frankfurt"}, "host": {"203.0.113.20"}, "port": {"22"}, "ssh_user": {"root"}, "key": {"new"}}
	for key, bad := range map[string]string{
		"name": "", "host": "203.0.113.20:22", "port": "70000", "ssh_user": "root; reboot", "data_dir": "relative", "key": "nosuchkey",
	} {
		form := url.Values{}
		for k, v := range good {
			form[k] = v
		}
		form.Set(key, bad)
		res, body := a.post("/servers", "/servers", form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="`+key+`-error"`) {
			t.Errorf("%s=%q: %d, error on the field: %v", key, bad, res.StatusCode, strings.Contains(body, `id="`+key+`-error"`))
		}
	}
	// A data directory must be musdash's own: it makes directories there
	// and empties one of them.
	for dir, want := range map[string]string{
		"/etc": "only for musdash", "/etc/musdash": "only for musdash", "/home/deploy": "only for musdash", "/var/lib/docker/musdash": "only for musdash",
		"/root/.ssh": "only for musdash", "/tmp/musdash": "only for musdash", "/": "Enter a full path", "/srv/my dir": "Enter a full path", "/srv/../etc": "Enter a full path",
	} {
		form := url.Values{}
		for k, v := range good {
			form[k] = v
		}
		form.Set("data_dir", dir)
		res, body := a.post("/servers", "/servers", form)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="data_dir-error"`) || !strings.Contains(body, want) {
			t.Errorf("data_dir=%q: %d, says %q: %v", dir, res.StatusCode, want, strings.Contains(body, want))
		}
	}
	for _, host := range []string{"-oProxyCommand=evil", "a b", "http://x", "x/y", ""} {
		form := url.Values{}
		for k, v := range good {
			form[k] = v
		}
		form.Set("host", host)
		if res, _ := a.post("/servers", "/servers", form); res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("host %q was accepted", host)
		}
	}
	if list, _ := a.db.ListServers(ctx, team); len(list) != 1 {
		t.Fatalf("%d servers after only refused forms", len(list))
	}

	server, srv := a.remoteServer("Frankfurt")
	if server.DataDir == "" || server.Status != db.ServerUnknown || server.HostKey != "" {
		t.Fatalf("%+v", server)
	}
	key, _ := a.db.SSHKeyByID(ctx, server.SSHKeyID)
	// The list is a tile a server, the whole of it a link to the server's
	// page; what acts on a server is on that page and not in the list.
	own := "/servers/" + server.ID
	_, page = a.get("/servers")
	for _, want := range []string{"Frankfurt", "Not checked yet", `href="` + own + `"`, "deploy@" + server.Host + ":" + strconv.Itoa(server.Port)} {
		if !strings.Contains(page, want) {
			t.Errorf("the list is missing %q", want)
		}
	}
	for _, absent := range []string{key.PublicKey, own + "/check", own + "/delete", "PRIVATE KEY"} {
		if strings.Contains(page, absent) {
			t.Errorf("the list holds %q", absent)
		}
	}
	res, page = a.get(own)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"Frankfurt", "Not checked yet", key.PublicKey, "authorized_keys", server.DataDir, `action="` + own + `/check"`,
		`href="` + own + `/metrics"`, `href="` + own + `/variables"`, "Remove server"} {
		if !strings.Contains(page, want) {
			t.Errorf("the server's page is missing %q", want)
		}
	}
	if strings.Contains(page, "PRIVATE KEY") {
		t.Fatal("the page shows a private key")
	}
	// Its variables are a page of their own, which leads back.
	if _, vars := a.get(own + "/variables"); !strings.Contains(vars, `href="`+own+`"`) {
		t.Error("a server's variables do not lead back to its page")
	}
	if res, _ := a.get("/servers/nosuchserver"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a server that is not there: %d", res.StatusCode)
	}
	// Nothing is deployed to a server that was never checked.
	projectID, env := a.project("Shop")
	form := url.Values{"name": {"api"}, "image": {"nginx:alpine"}, "port": {"80"}, "server": {server.ID}}
	res, body := a.post("/projects/"+projectID+"/env/"+env.ID+"/app/new", "/projects/"+projectID+"/env/"+env.ID+"/app", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "has not been checked yet") {
		t.Fatalf("an app on an unchecked server: %d", res.StatusCode)
	}

	// Check: the first contact records the host key and what was found.
	res, page = a.post("/servers", "/servers/"+server.ID+"/check", url.Values{})
	wantStatus(t, res, http.StatusOK)
	checked, _ := a.db.ServerByID(ctx, server.ID)
	if checked.HostKey != servers.HostKeyLine(srv.HostKey) || checked.CheckedAt == 0 {
		t.Fatalf("after the check: %+v", checked)
	}
	// The fingerprint comes with the kind of key and the file to compare it
	// with: a server has three host keys.
	// The answer is the server's own page, under the address a Refresh
	// may fetch: a second check is not what Refresh is for.
	for _, want := range []string{"What the check found", "signed in as deploy", "host key was recorded", "ED25519 " + servers.Fingerprint(checked.HostKey),
		"ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub", "Forwarding", `data-address="` + own + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the report is missing %q", want)
		}
	}
	// What the header says of the server is what the check just stored.
	if strings.Contains(page, "Not checked yet") {
		t.Error("the page after a check shows the server as it was before it")
	}

	// With two servers the forms ask which one, and an app goes where it
	// was sent, with an address built from that server's.
	a.db.Exec(`UPDATE servers SET ip = '198.51.100.20' WHERE id = ?`, server.ID)
	_, newForm := a.get("/projects/" + projectID + "/env/" + env.ID + "/app/new")
	if !strings.Contains(newForm, `name="server"`) || !strings.Contains(newForm, "Frankfurt") || !strings.Contains(newForm, "(this machine)") {
		t.Fatal("the New app form does not ask which server")
	}
	suggested := regexp.MustCompile(`[a-z2-7]{8}\.[0-9.]+\.sslip\.io`).FindString(newForm)
	if suggested == "" {
		t.Fatal("the form suggests no generated address")
	}
	form.Set("domain", suggested)
	res, body = a.post("/projects/"+projectID+"/env/"+env.ID+"/app/new", "/projects/"+projectID+"/env/"+env.ID+"/app", form)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("an app on the remote server: %d\n%s", res.StatusCode, body)
	}
	appID := createdID(res, db.KindApp)
	created, _ := a.db.AppByID(ctx, appID)
	domains, _ := a.db.ListDomains(ctx, db.KindApp, appID)
	if created.ServerID != server.ID || len(domains) != 1 || !strings.HasSuffix(domains[0].Host, ".198.51.100.20.sslip.io") {
		t.Fatalf("server %s (want %s), domains %+v", created.ServerID, server.ID, domains)
	}
	for _, path := range []string{"/projects/" + projectID + "/env/" + env.ID + "/database/new?engine=postgres", "/projects/" + projectID + "/env/" + env.ID + "/service/new?template=custom"} {
		if _, f := a.get(path); !strings.Contains(f, `name="server"`) {
			t.Errorf("%s does not ask which server", path)
		}
	}

	// The proxy cannot be installed without a binary for the server; the
	// message says which file is missing.
	a.db.Exec(`UPDATE servers SET arch = 'riscv64', status = 'ok' WHERE id = ?`, server.ID)
	res, _ = a.post("/servers", "/servers/"+server.ID+"/proxy", url.Values{})
	wantRedirect(t, res, own)
	if _, page = a.get(own); !strings.Contains(page, "musdash-linux-riscv64") {
		t.Fatal("the missing proxy binary was not explained")
	}

	// A server with something on it is not removed, and its page says why.
	res, _ = a.post("/servers", "/servers/"+server.ID+"/delete", url.Values{})
	wantRedirect(t, res, own)
	if _, err := a.db.ServerByID(ctx, server.ID); err != nil {
		t.Fatal("a server with an app on it was removed")
	}
	// Forgetting the host key means the next check is a first contact again.
	res, _ = a.post("/servers", "/servers/"+server.ID+"/forget-host-key", url.Values{})
	wantRedirect(t, res, own)
	if got, _ := a.db.ServerByID(ctx, server.ID); got.HostKey != "" || got.Status != db.ServerUnknown {
		t.Fatalf("after forgetting: %+v", got)
	}
	// Once empty, it goes; the local server never does.
	a.db.Exec(`DELETE FROM apps WHERE id = ?`, appID)
	res, _ = a.post("/servers", "/servers/"+server.ID+"/delete", url.Values{})
	wantRedirect(t, res, "/servers")
	if _, err := a.db.ServerByID(ctx, server.ID); err != db.ErrNotFound {
		t.Fatalf("the empty server: %v", err)
	}
	local, _ := a.db.EnsureLocalServer(ctx, team, "")
	for _, path := range []string{"/check", "/proxy", "/forget-host-key", "/delete"} {
		res, _ := a.post("/servers", "/servers/"+local.ID+path, url.Values{})
		wantStatus(t, res, http.StatusNotFound)
	}
	if _, err := a.db.ServerByID(ctx, local.ID); err != nil {
		t.Fatal("the local server was removed")
	}
}

// A tile shows what its server used when it was last sampled, and says so
// in a line when there is nothing recent.
func TestServersPageShowsLastReading(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	local, err := a.db.EnsureLocalServer(ctx, team, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, page := a.get("/servers"); !strings.Contains(page, "Usage is not recorded.") || strings.Contains(page, "meter-bar") {
		t.Fatal("a server that is not sampled does not say so")
	}
	if err := a.db.SetServerSampled(ctx, team, local.ID, true); err != nil {
		t.Fatal(err)
	}
	const gib = 1 << 30
	reading := db.Sample{CPU: 4250, Mem: 1 * gib, MemTotal: 4 * gib, DiskUsed: 19 * gib, DiskTotal: 20 * gib}
	// One from an hour ago is not what the server uses now.
	if err := a.db.AddSamples(ctx, local.ID, time.Now().Add(-time.Hour).Unix(), map[string]db.Sample{"": reading}); err != nil {
		t.Fatal(err)
	}
	if _, page := a.get("/servers"); !strings.Contains(page, "No recent reading of what it uses.") || strings.Contains(page, "meter-bar") {
		t.Fatal("an old reading is shown as the present, or its absence is not said")
	}
	if err := a.db.AddSamples(ctx, local.ID, time.Now().Unix(), map[string]db.Sample{"": reading}); err != nil {
		t.Fatal(err)
	}
	_, page := a.get("/servers")
	for _, want := range []string{"42.5%", "1.0 GiB of 4.0 GiB", "19.0 GiB of 20.0 GiB", "Nearly full", "meter-bar"} {
		if !strings.Contains(page, want) {
			t.Errorf("the tile lacks %q", want)
		}
	}
	server, _ := a.remoteServer("Frankfurt")
	a.db.Exec(`UPDATE servers SET status = 'ok', docker_version = '28.3.2', proxy = 'installed' WHERE id = ?`, server.ID)
	_, page = a.get("/servers")
	for _, want := range []string{"Frankfurt", "Ready", "Docker 28.3.2", "Proxy installed"} {
		if !strings.Contains(page, want) {
			t.Errorf("a remote server's tile lacks %q", want)
		}
	}
	// What the list says of a remote server is what was stored: the
	// connection the pool would open for a question is not there.
	if a.server.Pool.Connections() != 0 {
		t.Fatal("the list reached for a remote server")
	}
}

func TestOtherTeamsServerIsNotReachable(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	theirKey, err := a.db.CreateSSHKey(ctx, "otherteam", "theirs", "ssh-ed25519 AAAA", a.seal("private"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := a.db.CreateServer(ctx, db.Server{TeamID: "otherteam", Name: "their-server", Host: "198.51.100.99", Port: 22, SSHUser: "root", SSHKeyID: theirKey.ID, DataDir: "/var/lib/musdash"})
	if err != nil {
		t.Fatal(err)
	}
	a.db.Exec(`UPDATE servers SET host_key = 'ssh-ed25519 AAAA', status = 'ok' WHERE id = ?`, theirs.ID)

	if _, page := a.get("/servers"); strings.Contains(page, "their-server") || strings.Contains(page, "198.51.100.99") {
		t.Fatal("another team's server is listed")
	}
	if res, page := a.get("/servers/" + theirs.ID); res.StatusCode != http.StatusNotFound || strings.Contains(page, "their-server") {
		t.Fatalf("another team's server has a page here: %d", res.StatusCode)
	}
	token := a.csrf("/servers")
	for _, path := range []string{"", "/check", "/proxy", "/forget-host-key", "/delete"} {
		res, _ := a.postRaw(a.client, "/servers/"+theirs.ID+path, url.Values{"_csrf": {token}, "ip": {"203.0.113.1"}}, nil)
		wantStatus(t, res, http.StatusNotFound)
	}
	// Nor can a server be added with their key, or an app sent to their server.
	res, _ := a.post("/servers", "/servers", url.Values{"name": {"x"}, "host": {"203.0.113.20"}, "port": {"22"}, "ssh_user": {"root"}, "key": {theirKey.ID}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	projectID, env := a.project("Shop")
	res, body := a.post("/projects/"+projectID+"/env/"+env.ID+"/app/new", "/projects/"+projectID+"/env/"+env.ID+"/app",
		url.Values{"name": {"api"}, "image": {"nginx:alpine"}, "port": {"80"}, "server": {theirs.ID}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose one of the servers listed") {
		t.Fatalf("an app on another team's server: %d", res.StatusCode)
	}
	if got, _ := a.db.ServerByID(ctx, theirs.ID); got.HostKey == "" || got.IP != "" {
		t.Fatalf("their server was changed: %+v", got)
	}
	if n, _ := a.db.ServerUse(ctx, theirs.ID); n != 0 {
		t.Fatal("something was put on their server")
	}
}

func TestAppBuildServerSetting(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings := a.appPath(app.ID) + "/settings"

	// With one server there is nothing to choose.
	if _, page := a.get(settings); strings.Contains(page, "Build server") {
		t.Fatal("the build server is offered with only one server")
	}
	server, _ := a.remoteServer("Builder")
	_, page := a.get(settings)
	if !strings.Contains(page, "Build server") || !strings.Contains(page, "Builder") || !strings.Contains(page, "The server the app runs on") {
		t.Fatal("the build server choice is missing")
	}
	res, _ := a.post(settings, a.appPath(app.ID)+"/build-server", url.Values{"build_server": {server.ID}})
	wantRedirect(t, res, settings)
	if got, _ := a.db.AppByID(ctx, app.ID); got.BuildServerID != server.ID {
		t.Fatalf("build server %q", got.BuildServerID)
	}
	// A server that builds for an app is in use.
	res, _ = a.post("/servers", "/servers/"+server.ID+"/delete", url.Values{})
	wantRedirect(t, res, "/servers/"+server.ID)
	if _, err := a.db.ServerByID(ctx, server.ID); err != nil {
		t.Fatal("a server an app is built on was removed")
	}
	// Back to its own server.
	res, _ = a.post(settings, a.appPath(app.ID)+"/build-server", url.Values{"build_server": {""}})
	wantRedirect(t, res, settings)
	if got, _ := a.db.AppByID(ctx, app.ID); got.BuildServerID != "" {
		t.Fatalf("build server %q", got.BuildServerID)
	}

	// Not another team's server, and not for an app that is not built.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('theirs', 'otherteam', 'theirs', 'ssh', 1)`)
	res, _ = a.post(settings, a.appPath(app.ID)+"/build-server", url.Values{"build_server": {"theirs"}})
	wantStatus(t, res, http.StatusNotFound)
	imageApp := a.newApp(projectID, env, "img", false, nil)
	res, _ = a.post(a.appPath(imageApp)+"/settings", a.appPath(imageApp)+"/build-server", url.Values{"build_server": {server.ID}})
	wantStatus(t, res, http.StatusNotFound)
}
