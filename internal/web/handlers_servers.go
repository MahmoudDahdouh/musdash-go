package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// serverInfo asks a server about Docker and the proxy. Each question has a
// short deadline so an unreachable server cannot stall the page.
func (s *Server) serverInfo(ctx context.Context, server db.Server) pages.ServerInfo {
	info := pages.ServerInfo{Server: server}
	if n, err := s.DB.ServerUse(ctx, server.ID); err == nil {
		info.Apps = n
	}
	if server.Kind == db.ServerSSH {
		// What the last check found is shown as stored. Asking every
		// remote server on every page view would make the page as slow as
		// the slowest of them.
		if key, err := s.DB.SSHKeyByID(ctx, server.SSHKeyID); err == nil {
			info.PublicKey = key.PublicKey
		}
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dk, err := s.Pool.Docker(ctx, server)
	if err != nil {
		info.DockerError = err.Error()
		return info
	}
	if info.DockerVersion, err = dk.Version(ctx); err != nil {
		info.DockerVersion = ""
		info.DockerError = "Docker did not answer. Is the daemon running, and may the musdash user use it?"
	}
	running, format := s.Deploy.ProxyState(ctx, dk.R)
	info.ProxyRunning, info.ProxyBehind = running, running && format < proxy.RoutesFormat
	return info
}

func (s *Server) renderServers(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	s.renderServersWith(w, r, status, f, "", nil)
}

// renderServersWith draws the Servers page with the report of a check
// that was just made of one of them.
func (s *Server) renderServersWith(w http.ResponseWriter, r *http.Request, status int, f ui.Form, checked string, report *servers.Report) {
	teamID := sessionFrom(r).TeamID
	if _, err := s.DB.EnsureLocalServer(r.Context(), teamID, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.DB.ListServers(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	infos := make([]pages.ServerInfo, 0, len(list))
	for _, server := range list {
		info := s.serverInfo(r.Context(), server)
		if server.ID == checked {
			info.Report = report
		}
		infos = append(infos, info)
	}
	keys, err := s.DB.ListSSHKeys(r.Context(), teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.Servers(s.shell(w, r, "Servers", "servers"), infos, keys, f))
}

var (
	hostNameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	sshUserRE  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// validHost reports whether host is an IP address or a host name.
func validHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	return hostNameRE.MatchString(host) && !strings.Contains(host, "..")
}

// serverCreate adds a server reached over SSH. Nothing is connected yet:
// the page then shows the key to install, and Check makes the first
// connection.
func (s *Server) serverCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	var f ui.Form
	f.Set("_submitted", "1")
	form := func(key string) string {
		v := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, v)
		return v
	}
	server := db.Server{TeamID: teamID, Name: form("name"), Host: form("host"), SSHUser: form("ssh_user"), DataDir: form("data_dir")}
	if server.Name == "" || len(server.Name) > 60 || !plainText(server.Name) {
		f.Fail("name", labelProblem(server.Name, "Enter a name of up to 60 characters."))
	}
	if !validHost(server.Host) {
		f.Fail("host", "Enter an IP address or a host name, without a port or a scheme.")
	}
	port := form("port")
	if port == "" {
		port = "22"
	}
	var err error
	if server.Port, err = strconv.Atoi(port); err != nil || server.Port < 1 || server.Port > 65535 {
		f.Fail("port", "Enter a port from 1 to 65535.")
	}
	if !sshUserRE.MatchString(server.SSHUser) {
		f.Fail("ssh_user", "Enter the account's name: lowercase letters, numbers, hyphens and underscores.")
	}
	if server.DataDir == "" {
		server.DataDir = servers.DefaultDataDir(server.SSHUser)
	}
	switch servers.DataDirProblem(server.DataDir) {
	case servers.DataDirShape:
		f.Fail("data_dir", "Enter a full path such as /srv/musdash, using letters, numbers, dots, hyphens and underscores.")
	case servers.DataDirShared:
		// musdash makes its directories there and empties one of them.
		f.Fail("data_dir", "That directory is the system's, or is shared with other things. Enter one that is only for musdash, such as /srv/musdash.")
	}
	if net.ParseIP(server.Host) != nil {
		server.IP = server.Host
	}
	keyChoice := form("key")
	if !f.OK() {
		s.renderServers(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if keyChoice == "new" || keyChoice == "" {
		public, private, err := source.GenerateDeployKey("musdash")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sealed, err := s.Box.Seal(private)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		key, err := s.DB.CreateSSHKey(ctx, teamID, "Server "+server.Name, public, sealed)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		keyChoice = key.ID
	}
	server.SSHKeyID = keyChoice
	if _, err := s.DB.CreateServer(ctx, server); errors.Is(err, db.ErrNotFound) {
		f.Fail("key", "Choose one of the keys listed.")
		s.renderServers(w, r, http.StatusUnprocessableEntity, f)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Server added. Install its key on the server, then choose Check.")
	redirect(w, r, "/servers")
}

// loadRemoteServer fetches the remote server in the path for the signed-in
// team. It answers 404 itself, also for the local server.
func (s *Server) loadRemoteServer(w http.ResponseWriter, r *http.Request) (db.Server, bool) {
	server, err := s.DB.Server(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && server.Kind != db.ServerSSH) {
		s.notFound(w, r)
		return server, false
	}
	if err != nil {
		s.fail(w, r, err)
		return server, false
	}
	return server, true
}

func (s *Server) serverCheck(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadRemoteServer(w, r)
	if !ok {
		return
	}
	report, err := s.Pool.Check(r.Context(), server)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Rendered directly: the report is for this one view.
	s.renderServersWith(w, r, http.StatusOK, ui.Form{}, server.ID, &report)
}

func (s *Server) serverProxy(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadRemoteServer(w, r)
	if !ok {
		return
	}
	if err := s.Pool.InstallProxy(r.Context(), server, filepath.Join(s.Cfg.DataDir, "dist")); err != nil {
		s.Log.Warn("install proxy", "server", server.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The proxy was not installed: "+err.Error())
		redirect(w, r, "/servers")
		return
	}
	// The proxy starts with the routes of what already runs there.
	if err := s.Deploy.SyncRoutes(r.Context(), server); err != nil && !errors.Is(err, deploy.ErrProxyDown) {
		s.Log.Warn("publish routes after a proxy install", "server", server.ID, "err", err)
	}
	setFlash(w, r, ui.ToneOK, "The proxy is installed on "+server.Name+" and running.")
	redirect(w, r, "/servers")
}

func (s *Server) serverForgetHostKey(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadRemoteServer(w, r)
	if !ok {
		return
	}
	if err := s.DB.ForgetServerHostKey(r.Context(), server.TeamID, server.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Pool.Forget(server.ID)
	setFlash(w, r, ui.ToneOK, "Host key forgotten. Check the server to record the key it presents now.")
	redirect(w, r, "/servers")
}

func (s *Server) serverDelete(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadRemoteServer(w, r)
	if !ok {
		return
	}
	err := s.DB.DeleteServer(r.Context(), server.TeamID, server.ID)
	switch {
	case errors.Is(err, db.ErrInUse):
		setFlash(w, r, ui.ToneWarn, "Something still runs on "+server.Name+". Delete or move it first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		s.Pool.Forget(server.ID)
		setFlash(w, r, ui.ToneOK, server.Name+" was removed from musdash. Nothing was changed on the machine itself: its proxy service and data directory are still there.")
	}
	redirect(w, r, "/servers")
}

// serverChoices lists the servers a new app, database or service can run
// on, this machine first.
func (s *Server) serverChoices(ctx context.Context, teamID string) ([]db.Server, error) {
	if _, err := s.DB.EnsureLocalServer(ctx, teamID, ""); err != nil {
		return nil, err
	}
	return s.DB.ListServers(ctx, teamID)
}

// targetServer returns the server a creation form chose from the list. A
// form without the field, which is every form while there is one server,
// means the first.
func targetServer(r *http.Request, f *ui.Form, list []db.Server) db.Server {
	want := r.PostFormValue("server")
	f.Set("server", want)
	if want == "" {
		return list[0]
	}
	for _, server := range list {
		if server.ID != want {
			continue
		}
		if server.Kind == db.ServerSSH && server.HostKey == "" {
			f.Fail("server", server.Name+" has not been checked yet. Open Servers and choose Check first.")
		}
		return server
	}
	f.Fail("server", "Choose one of the servers listed.")
	return list[0]
}

func (s *Server) serverList(w http.ResponseWriter, r *http.Request) {
	s.renderServers(w, r, http.StatusOK, ui.Form{})
}

func (s *Server) serverUpdate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	ip := strings.TrimSpace(r.PostFormValue("ip"))
	f.Set("ip", ip)
	if ip != "" && net.ParseIP(ip) == nil {
		f.Fail("ip", "Enter an IP address such as 203.0.113.7.")
		s.renderServers(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	err := s.DB.UpdateServerIP(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"), ip)
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Address saved.")
	redirect(w, r, "/servers")
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderInstanceSettings(w, r, http.StatusOK, ui.Form{})
}

// renderInstanceSettings draws the dashboard's settings as they are stored; what
// was typed into a refused form is in f.
func (s *Server) renderInstanceSettings(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	domain, err := s.DB.Setting(r.Context(), db.SettingInstanceDomain)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	email, err := s.DB.Setting(r.Context(), db.SettingACMEEmail)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.InstanceSettings(s.shell(w, r, "Settings", "settings"), f, domain, email))
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var f ui.Form
	rawDomain := strings.TrimSpace(r.PostFormValue("instance_domain"))
	rawEmail := strings.TrimSpace(r.PostFormValue("acme_email"))
	f.Set("instance_domain", rawDomain)
	f.Set("acme_email", rawEmail)

	domain := ""
	if rawDomain != "" {
		domain = proxy.NormalizeHost(rawDomain)
		if !proxy.ValidHost(domain) || !strings.Contains(domain, ".") {
			f.Fail("instance_domain", "Enter a domain such as musdash.example.com, without http:// or a path.")
		} else if used, err := s.DB.HostInUse(ctx, domain); err == nil && used {
			f.Fail("instance_domain", "This domain is already routed to an app.")
		}
	}
	email := ""
	if rawEmail != "" {
		var ok bool
		if email, ok = normalEmail(rawEmail); !ok {
			f.Fail("acme_email", "Enter an email address like you@example.com.")
		}
	}
	if !f.OK() {
		s.renderInstanceSettings(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if err := s.DB.SetSetting(ctx, db.SettingInstanceDomain, domain); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetSetting(ctx, db.SettingACMEEmail, email); err != nil {
		s.fail(w, r, err)
		return
	}

	message, tone := "Settings saved.", ui.ToneOK
	server, err := s.DB.EnsureLocalServer(ctx, sessionFrom(r).TeamID, "")
	if err == nil {
		err = s.Deploy.SyncRoutes(ctx, server)
	}
	switch {
	case errors.Is(err, deploy.ErrProxyDown):
		message, tone = "Settings saved. The proxy is not running, so the dashboard domain is not served yet.", ui.ToneWarn
	case err != nil:
		s.Log.Error("sync routes", "err", err)
		message, tone = "Settings saved, but the proxy's routes could not be updated: "+err.Error(), ui.ToneWarn
	}
	setFlash(w, r, tone, message)
	redirect(w, r, "/settings")
}
