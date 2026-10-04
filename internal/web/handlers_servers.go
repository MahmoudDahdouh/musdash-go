package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// serverInfo asks a server about Docker and the proxy. Each question has a
// short deadline so an unreachable server cannot stall the page.
func (s *Server) serverInfo(ctx context.Context, server db.Server) pages.ServerInfo {
	info := pages.ServerInfo{Server: server}
	if apps, err := s.DB.AppsOnServer(ctx, server.ID); err == nil {
		info.Apps = len(apps)
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
	info.ProxyRunning = s.proxyRunning(ctx, dk.R)
	return info
}

// proxyRunning reports whether the proxy's pid file names a live process.
func (s *Server) proxyRunning(ctx context.Context, r runner.Runner) bool {
	f, err := r.ReadFile(ctx, s.Cfg.ProxyPIDPath())
	if err != nil {
		return false
	}
	raw, _ := io.ReadAll(io.LimitReader(f, 32))
	f.Close()
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid < 2 {
		return false
	}
	// Signal 0 checks that the process exists without touching it.
	return r.Run(ctx, runner.Cmd{Name: "kill", Args: []string{"-0", strconv.Itoa(pid)}}) == nil
}

func (s *Server) renderServers(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
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
		infos = append(infos, s.serverInfo(r.Context(), server))
	}
	s.render(w, r, status, pages.Servers(s.shell(w, r, "Servers", "servers"), infos, f))
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
	s.render(w, r, http.StatusOK, pages.InstanceSettings(s.shell(w, r, "Settings", "settings"), ui.Form{}, domain, email))
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
		s.render(w, r, http.StatusUnprocessableEntity, pages.InstanceSettings(s.shell(w, r, "Settings", "settings"), f, rawDomain, rawEmail))
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
