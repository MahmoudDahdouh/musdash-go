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
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/servers"
)

// ErrProxyDown is returned by SyncRoutes when the routes file was written
// but no proxy process is running to load it.
var ErrProxyDown = errors.New("the proxy is not running")

// BuildRoutes turns a server's domains into the proxy's route file. A host
// the proxy would refuse is left out and returned in skipped: one bad name
// must not stop every other app's routes from being published.
func BuildRoutes(rows []db.RouteRow, email, instanceDomain, instanceTarget string) (file proxy.File, skipped []string) {
	file = proxy.File{Email: email, Routes: []proxy.Route{}}
	taken := make(map[string]bool, len(rows)+1)
	for _, r := range rows {
		taken[r.Host] = true
	}
	if instanceDomain != "" && !taken[instanceDomain] && proxy.ValidHost(instanceDomain) {
		taken[instanceDomain] = true
		file.Routes = append(file.Routes, proxy.Route{Host: instanceDomain, Target: instanceTarget, TLS: true})
	}
	for _, r := range rows {
		if !proxy.ValidHost(r.Host) || !proxy.ValidPath(r.Path) {
			skipped = append(skipped, r.Host+r.Path)
			continue
		}
		file.Routes = append(file.Routes, proxy.Route{
			Host: r.Host, Path: r.Path, StripPrefix: r.StripPrefix && r.Path != "",
			Target: "127.0.0.1:" + strconv.Itoa(r.HostPort), TLS: r.TLS,
			AuthUser: r.AuthUser, AuthHash: r.AuthHash,
		})
		if !r.RedirectWWW {
			continue
		}
		// The other form of the name redirects to the one that was entered.
		other := "www." + r.Host
		if strings.HasPrefix(r.Host, "www.") {
			other = strings.TrimPrefix(r.Host, "www.")
		}
		switch {
		case taken[other]:
		case !proxy.ValidHost(other):
			skipped = append(skipped, other)
		default:
			taken[other] = true
			file.Routes = append(file.Routes, proxy.Route{Host: other, RedirectTo: r.Host, TLS: r.TLS})
		}
	}
	return file, skipped
}

// SyncRoutes rewrites a server's routes file from the database and tells the
// proxy to load it. It returns ErrProxyDown when the file was written but no
// proxy is running to serve it.
func (d *Deployer) SyncRoutes(ctx context.Context, server db.Server) error {
	// One publication at a time: the database read, the file write and the
	// signal form one step, so an older snapshot is never written over a
	// newer one.
	// The lock is the server's own: one that does not answer must not hold
	// up the routes of the others.
	mu := d.routesLock(server.ID)
	mu.Lock()
	defer mu.Unlock()

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
	file, skipped := BuildRoutes(rows, email, instanceDomain, d.InstanceTarget)
	if len(skipped) > 0 {
		d.Log.Warn("domains left out of the routes: not valid host names", "hosts", skipped)
	}
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
	if err := r.MkdirAll(ctx, d.at(r).ProxyDir(), 0o700); err != nil {
		return err
	}
	// 0600: the file holds the password hashes of guarded routes. The
	// proxy runs as the account that owns the data directory, as this does.
	if err := r.WriteFile(ctx, d.at(r).RoutesPath(), 0o600, bytes.NewReader(raw)); err != nil {
		return err
	}
	return d.signalProxy(ctx, r)
}

func (d *Deployer) routesLock(serverID string) *sync.Mutex {
	d.routesMu.Lock()
	defer d.routesMu.Unlock()
	if d.routesOf == nil {
		d.routesOf = map[string]*sync.Mutex{}
	}
	mu := d.routesOf[serverID]
	if mu == nil {
		mu = &sync.Mutex{}
		d.routesOf[serverID] = mu
	}
	return mu
}

// signalProxy asks the proxy to reload now. The proxy also re-reads its
// routes file every few seconds on its own, so the signal only makes a
// change immediate; when it cannot be delivered this waits out one poll
// instead of failing.
func (d *Deployer) signalProxy(ctx context.Context, r runner.Runner) error {
	pidFile, err := r.ReadFile(ctx, d.at(r).ProxyPIDPath())
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
		return ErrProxyDown
	}
	// A pid file can outlive its process, and the number can be reused by
	// something else; SIGHUP would kill most programs. Where the system
	// says what the process is (Linux), only a musdash process is signalled.
	if comm, err := r.ReadFile(ctx, "/proc/"+strconv.Itoa(pid)+"/comm"); err == nil {
		name, _ := io.ReadAll(io.LimitReader(comm, 64))
		comm.Close()
		// The kernel truncates the name, and the binary may carry a suffix
		// such as musdash-linux-amd64.
		if !strings.HasPrefix(strings.TrimSpace(string(name)), "musdash") {
			return ErrProxyDown
		}
	}
	_, err = r.Output(ctx, runnerCmd("kill", "-HUP", strconv.Itoa(pid)))
	if err == nil {
		return nil
	}
	var ee *runner.ExitError
	if errors.As(err, &ee) && strings.Contains(strings.ToLower(ee.Stderr), "no such process") {
		return ErrProxyDown
	}
	// The proxy is alive but would not take the signal, for example because
	// it runs as another user. It will notice the new file by itself.
	d.Log.Warn("could not signal the proxy; waiting for it to re-read its routes", "err", err)
	select {
	case <-time.After(d.pollWait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
