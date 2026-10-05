package servers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// Item is one thing a check looked at.
type Item struct {
	Name   string
	OK     bool
	Detail string
	// Needed says the server cannot be deployed to while this is not OK.
	Needed bool
}

// Report is what a check of a server found.
type Report struct {
	Items []Item
	// Fingerprint is the host key's, for the person to compare with what
	// the server itself reports.
	Fingerprint string
	// NewHostKey says this check was the first to see the key.
	NewHostKey bool
}

// OK reports whether everything a deployment needs is there.
func (r Report) OK() bool {
	for _, it := range r.Items {
		if it.Needed && !it.OK {
			return false
		}
	}
	return true
}

// Problem is the first thing that is missing, in words.
func (r Report) Problem() string {
	for _, it := range r.Items {
		if it.Needed && !it.OK {
			return it.Name + ": " + it.Detail
		}
	}
	return ""
}

// archOf maps what `uname -m` prints to Go's names, and anything it does
// not know to "". The answer comes from the server, and the name it gives
// becomes part of a file name on this machine: it is one of these words or
// it is nothing.
func archOf(machine string) string {
	switch strings.TrimSpace(machine) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv6l":
		return "arm"
	case "riscv64":
		return "riscv64"
	}
	return ""
}

// knownArch are the architectures a proxy binary is looked up for.
var knownArch = map[string]bool{"amd64": true, "arm64": true, "arm": true, "riscv64": true}

// subDirs are the directories musdash uses under a server's data directory.
var subDirs = []string{"", "apps", "work", "backups", "proxy", "bin"}

// Check connects to a server and looks at what a deployment needs there:
// Docker and its Compose plugin, git, and a data directory. On the first
// check it records the server's host key. What it finds is stored with the
// server and returned.
func (p *Pool) Check(ctx context.Context, s db.Server) (Report, error) {
	var rep Report
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	fail := func(detail string) (Report, error) {
		s.Status, s.StatusDetail = db.ServerUnreachable, detail
		if err := p.DB.SetServerChecked(ctx, s); err != nil {
			return rep, err
		}
		return rep, nil
	}
	first := s.HostKey == ""
	r, hostKey, err := p.FirstContact(ctx, s)
	if errors.Is(err, runner.ErrHostKeyChanged) {
		rep.Items = append(rep.Items, Item{Name: "Connection", Needed: true,
			Detail: "the server's host key is not the one recorded for it. If the server was reinstalled, choose Forget host key and check again; otherwise something else is answering at this address"})
		return fail(rep.Items[0].Detail)
	}
	if err != nil {
		rep.Items = append(rep.Items, Item{Name: "Connection", Needed: true, Detail: connectProblem(err)})
		return fail(rep.Items[0].Detail)
	}
	defer r.Close()
	// A connection made under the old settings is of no use any more.
	p.Forget(s.ID)
	rep.Fingerprint, rep.NewHostKey = Fingerprint(hostKey), first
	rep.Items = append(rep.Items, Item{Name: "Connection", OK: true, Needed: true, Detail: "signed in as " + s.SSHUser})

	say := func(name string, args ...string) (string, error) {
		out, err := r.Output(ctx, runner.Cmd{Name: name, Args: args})
		return strings.TrimSpace(string(out)), err
	}

	if system, _ := say("uname", "-s"); system != "Linux" {
		rep.Items = append(rep.Items, Item{Name: "System", Needed: true, Detail: "musdash deploys to Linux servers; this one reports " + orUnknown(system)})
	}
	machine, _ := say("uname", "-m")
	if s.Arch = archOf(machine); s.Arch == "" {
		rep.Items = append(rep.Items, Item{Name: "Architecture", Detail: "the server reports an architecture musdash has no proxy for; apps can be deployed, but the proxy cannot be installed"})
	}

	// Docker: there, and usable by this account.
	version, err := say("docker", "version", "--format", "{{.Server.Version}}")
	switch {
	case err == nil && version != "":
		s.DockerVersion = version
		rep.Items = append(rep.Items, Item{Name: "Docker", OK: true, Needed: true, Detail: "version " + version})
	case isMissing(err):
		rep.Items = append(rep.Items, Item{Name: "Docker", Needed: true, Detail: "not installed. Install it on the server with: curl -fsSL https://get.docker.com | sh"})
	default:
		rep.Items = append(rep.Items, Item{Name: "Docker", Needed: true,
			Detail: "installed, but " + s.SSHUser + " may not use it. Add the account to the docker group (usermod -aG docker " + s.SSHUser + ") and sign in again, or connect as root"})
	}
	if out, err := say("docker", "compose", "version", "--short"); err == nil && out != "" {
		rep.Items = append(rep.Items, Item{Name: "Docker Compose", OK: true, Detail: "version " + out})
	} else {
		rep.Items = append(rep.Items, Item{Name: "Docker Compose", Detail: "the Compose plugin is missing; services cannot be deployed here until it is installed (package docker-compose-plugin)"})
	}
	if out, err := say("git", "--version"); err == nil {
		rep.Items = append(rep.Items, Item{Name: "git", OK: true, Detail: strings.TrimPrefix(out, "git version ")})
	} else {
		rep.Items = append(rep.Items, Item{Name: "git", Detail: "not installed; apps and services cannot be deployed from a repository here until it is"})
	}

	// The data directory, private to the account.
	dirOK := true
	for _, sub := range subDirs {
		if err := r.MkdirAll(ctx, path.Join(s.DataDir, sub), 0o700); err != nil {
			dirOK = false
			rep.Items = append(rep.Items, Item{Name: "Data directory", Needed: true, Detail: s.DataDir + " cannot be created by " + s.SSHUser + ". Create it on the server and give it to that account, or choose another directory"})
			break
		}
	}
	if dirOK {
		rep.Items = append(rep.Items, Item{Name: "Data directory", OK: true, Needed: true, Detail: s.DataDir})
	}

	if mem, err := say("sh", "-c", "awk '/^MemTotal:/ {print $2}' /proc/meminfo"); err == nil {
		if kb, err := strconv.Atoi(mem); err == nil {
			item := Item{Name: "Memory", OK: true, Detail: fmt.Sprintf("%d MB", kb/1024)}
			if kb < 900*1024 {
				item.Detail += ". Builds need more than this: build on another server, or deploy ready-made images"
			}
			rep.Items = append(rep.Items, item)
		}
	}
	if uid, _ := say("id", "-u"); uid == "0" {
		rep.Items = append(rep.Items, Item{Name: "Proxy install", OK: true, Detail: "connected as root"})
	} else if _, err := say("sudo", "-n", "true"); err == nil {
		rep.Items = append(rep.Items, Item{Name: "Proxy install", OK: true, Detail: s.SSHUser + " can use sudo without a password"})
	} else {
		rep.Items = append(rep.Items, Item{Name: "Proxy install", Detail: "installing the proxy needs root: " + s.SSHUser + " cannot use sudo without a password. Apps can be deployed, but their domains are not served until the proxy is installed"})
	}

	// The address generated domains are built from.
	if s.IP == "" {
		s.IP = publicAddress(ctx, s.Host)
	}

	s.Status, s.StatusDetail = db.ServerOK, ""
	if !rep.OK() {
		s.Status, s.StatusDetail = db.ServerProblem, rep.Problem()
	}
	return rep, p.DB.SetServerChecked(ctx, s)
}

func orUnknown(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// isMissing reports whether a command failed because it is not installed.
func isMissing(err error) bool {
	var exit *runner.ExitError
	return errors.As(err, &exit) && exit.Code == 127
}

// connectProblem words a failed connection for the person who typed the
// server's address.
func connectProblem(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return "the server refused the key. Add the public key shown for this server to ~/.ssh/authorized_keys of the account on the server"
	case strings.Contains(msg, "connection refused"):
		return "nothing answers on that port. Is the SSH port right, and does a firewall let this dashboard in?"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return "no answer in time. Is the address right, and does a firewall let this dashboard in?"
	case strings.Contains(msg, "no such host"):
		return "the address cannot be looked up"
	}
	return msg
}

// publicAddress is the address of a host as other machines reach it: the
// host itself when it is an address, otherwise what its name resolves to.
func publicAddress(ctx context.Context, host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP.String()
		}
	}
	return addrs[0].IP.String()
}

// proxyUnit is the systemd unit of the proxy on a remote server.
func proxyUnit(user, dataDir string) string {
	var b strings.Builder
	b.WriteString(`[Unit]
Description=musdash edge proxy (ports 80 and 443)
After=network-online.target
Wants=network-online.target

[Service]
# The account the dashboard signs in as, which writes the routes and
# signals this process to reload them.
User=` + user + `
Environment=MUSDASH_DATA=` + dataDir + `
ExecStart=` + path.Join(dataDir, "bin", "musdash") + ` proxy
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=1

# Bind ports 80 and 443 without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
`)
	if user != "root" {
		b.WriteString("CapabilityBoundingSet=CAP_NET_BIND_SERVICE\n")
	}
	b.WriteString(`
[Install]
WantedBy=multi-user.target
`)
	return b.String()
}

// safeForUnit reports whether a data directory or account name can be
// written into a unit file as it is: any other character could end the
// line and add one of its own.
func safeForUnit(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '/' || c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// ProxyBinary finds the musdash binary for a server of the given
// architecture: this process's own when it matches, otherwise one the
// operator put in the dist directory.
func ProxyBinary(distDir, arch string) (string, error) {
	// The value was stored from a server's answer. Only a known word may
	// become part of a path here: anything else could name another file
	// of this machine, which would then be copied to that server.
	if !knownArch[arch] {
		return "", fmt.Errorf("musdash has no proxy for the architecture %q", arch)
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		if self, err := os.Executable(); err == nil {
			return self, nil
		}
	}
	name := filepath.Join(distDir, "musdash-linux-"+arch)
	if info, err := os.Stat(name); err == nil && info.Mode().IsRegular() {
		return name, nil
	}
	return "", fmt.Errorf("the server is linux/%s and this dashboard runs on %s/%s, so its own binary cannot be used there. Put the musdash binary for linux/%s at %s and try again",
		arch, runtime.GOOS, runtime.GOARCH, arch, name)
}

// InstallProxy puts the proxy on a server and starts it as a service: the
// binary into the data directory, a systemd unit, and an empty routes file
// if there is none. It can be run again to replace the binary.
func (p *Pool) InstallProxy(ctx context.Context, s db.Server, distDir string) error {
	if s.Kind != db.ServerSSH {
		return errors.New("the proxy of this machine is installed with musdash itself")
	}
	if s.Arch == "" {
		return errors.New("check the server first, so that its architecture is known")
	}
	if !safeForUnit(s.SSHUser) || !safeForUnit(s.DataDir) || !path.IsAbs(s.DataDir) {
		return fmt.Errorf("the account %q or data directory %q has characters that cannot be written into a service file", s.SSHUser, s.DataDir)
	}
	binary, err := ProxyBinary(distDir, s.Arch)
	if err != nil {
		return err
	}
	r, err := p.Runner(ctx, s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := installProxy(ctx, r, s, binary); err != nil {
		return err
	}
	return p.DB.SetServerProxy(ctx, s.ID, db.ProxyInstalled)
}

// installProxy does the work of InstallProxy on the server r reaches.
func installProxy(ctx context.Context, r runner.Runner, s db.Server, binary string) error {

	for _, sub := range []string{"bin", "proxy"} {
		if err := r.MkdirAll(ctx, path.Join(s.DataDir, sub), 0o700); err != nil {
			return err
		}
	}
	src, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := r.WriteFile(ctx, path.Join(s.DataDir, "bin", "musdash"), 0o755, src); err != nil {
		return fmt.Errorf("copy the proxy to the server: %w", err)
	}
	// The proxy starts with whatever routes are there; none is a valid set.
	routes := path.Join(s.DataDir, "proxy", "routes.json")
	if f, err := r.ReadFile(ctx, routes); err == nil {
		f.Close()
	} else if err := r.WriteFile(ctx, routes, 0o644, strings.NewReader(`{"routes":[]}`+"\n")); err != nil {
		return err
	}
	unit := path.Join(s.DataDir, "musdash-proxy.service")
	if err := r.WriteFile(ctx, unit, 0o644, strings.NewReader(proxyUnit(s.SSHUser, s.DataDir))); err != nil {
		return err
	}

	// The rest needs root: directly, or through sudo that asks nothing.
	sudo := []string{}
	if out, _ := r.Output(ctx, runner.Cmd{Name: "id", Args: []string{"-u"}}); strings.TrimSpace(string(out)) != "0" {
		sudo = []string{"sudo", "-n"}
	}
	root := func(args ...string) error {
		full := append(append([]string{}, sudo...), args...)
		var stderr bytes.Buffer
		if _, err := r.Output(ctx, runner.Cmd{Name: full[0], Args: full[1:], Stderr: &stderr}); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	steps := [][]string{
		{"install", "-m", "0644", unit, "/etc/systemd/system/musdash-proxy.service"},
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "musdash-proxy.service"},
		// Restart rather than start: a second install replaces the binary.
		{"systemctl", "restart", "musdash-proxy.service"},
	}
	for _, step := range steps {
		if err := root(step...); err != nil {
			if len(sudo) > 0 && strings.Contains(err.Error(), "sudo") {
				return fmt.Errorf("installing the service needs root, and %s cannot use sudo without a password: %w", s.SSHUser, err)
			}
			return err
		}
	}
	return nil
}
