package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
)

// ErrProxyDown is returned by SyncRoutes when the routes file was written
// but no proxy process is running to load it.
var ErrProxyDown = errors.New("the proxy is not running")

// BuildRoutes turns a server's domains into the proxy's route file.
func BuildRoutes(rows []db.RouteRow, email, instanceDomain, instanceTarget string) proxy.File {
	file := proxy.File{Email: email, Routes: []proxy.Route{}}
	taken := make(map[string]bool, len(rows)+1)
	for _, r := range rows {
		taken[r.Host] = true
	}
	if instanceDomain != "" && !taken[instanceDomain] {
		taken[instanceDomain] = true
		file.Routes = append(file.Routes, proxy.Route{Host: instanceDomain, Target: instanceTarget, TLS: true})
	}
	for _, r := range rows {
		file.Routes = append(file.Routes, proxy.Route{Host: r.Host, Target: "127.0.0.1:" + strconv.Itoa(r.HostPort), TLS: r.TLS})
		if !r.RedirectWWW {
			continue
		}
		// The other form of the name redirects to the one that was entered.
		other := "www." + r.Host
		if strings.HasPrefix(r.Host, "www.") {
			other = strings.TrimPrefix(r.Host, "www.")
		}
		if !taken[other] {
			taken[other] = true
			file.Routes = append(file.Routes, proxy.Route{Host: other, RedirectTo: r.Host, TLS: r.TLS})
		}
	}
	return file
}

// SyncRoutes rewrites a server's routes file from the database and tells the
// proxy to load it.
func (d *Deployer) SyncRoutes(ctx context.Context, server db.Server) error {
	rows, err := d.DB.RoutesForServer(ctx, server.ID)
	if err != nil {
		return err
	}
	email, err := d.DB.Setting(ctx, db.SettingACMEEmail)
	if err != nil {
		return err
	}
	// The dashboard itself is routed only on the server it runs on.
	instanceDomain := ""
	if servers.IsLocal(server) {
		if instanceDomain, err = d.DB.Setting(ctx, db.SettingInstanceDomain); err != nil {
			return err
		}
	}
	file := BuildRoutes(rows, email, instanceDomain, d.InstanceTarget)
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	// Refuse to publish a file the proxy would reject: it would keep the old
	// table and the deploy would look done while serving stale routes.
	if _, err := proxy.Parse(bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("generated routes are invalid: %w", err)
	}

	r, err := d.Runners.Runner(ctx, server)
	if err != nil {
		return err
	}
	if err := r.MkdirAll(ctx, d.Cfg.ProxyDir(), 0o700); err != nil {
		return err
	}
	// 0644: the proxy may run as a different user than the control plane.
	if err := r.WriteFile(ctx, d.Cfg.RoutesPath(), 0o644, bytes.NewReader(raw)); err != nil {
		return err
	}

	pidFile, err := r.ReadFile(ctx, d.Cfg.ProxyPIDPath())
	if errors.Is(err, fs.ErrNotExist) {
		return ErrProxyDown
	}
	if err != nil {
		return err
	}
	rawPID, err := io.ReadAll(io.LimitReader(pidFile, 32))
	pidFile.Close()
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || pid < 2 {
		return fmt.Errorf("proxy pid file is malformed")
	}
	if err := r.Run(ctx, runnerCmd("kill", "-HUP", strconv.Itoa(pid))); err != nil {
		// A stale pid file: the proxy exited without removing it.
		return ErrProxyDown
	}
	return nil
}
