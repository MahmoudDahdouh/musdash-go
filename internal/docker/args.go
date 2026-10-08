package docker

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Mount kinds.
const (
	MountVolume = "volume"
	MountBind   = "bind"
)

// Mount attaches storage to a container.
type Mount struct {
	Kind     string // MountVolume or MountBind
	Source   string // volume name, or absolute host path
	Target   string // absolute path inside the container
	ReadOnly bool
	// Internal marks a bind source that musdash itself created (a file
	// mount it wrote), which is exempt from the host-path deny list.
	Internal bool
}

// Publish is one container port on a loopback port of the server.
type Publish struct {
	HostPort      int
	ContainerPort int
}

// RunSpec describes a container to start.
type RunSpec struct {
	Name  string
	Image string
	// Local says the image is on the server already and must not be
	// looked for anywhere else. Without it, an image that has gone
	// missing would be asked of a registry under the same name.
	Local         bool
	Network       string
	Alias         string // DNS name on the network
	HostPort      int    // published on 127.0.0.1; 0 publishes nothing
	ContainerPort int
	// Ports are further container ports published on 127.0.0.1 beside
	// ContainerPort: the ones an app's domains name.
	Ports []Publish
	// PublicPort publishes ContainerPort on every interface of the server.
	// Only databases whose public port a person switched on use it; apps
	// are reached through the proxy.
	PublicPort int
	EnvFile    string
	MemoryMB   int
	CPUs       float64
	Mounts     []Mount
	Labels     map[string]string
	// ExtraArgs are further `docker run` options, placed before the image.
	// They must already have been checked against the allow-list in
	// deploy.ParseRunOptions; nothing here re-validates them.
	ExtraArgs []string
	// Command replaces the image's default command.
	Command []string
}

// The parts of an image reference, by Docker's grammar. None of them can
// match anything that starts with "-", so a reference can never be read as
// a flag.
var (
	// registryRE is a registry's host: labels of letters, digits and
	// hyphens, with dots between. An IPv4 address is one too.
	registryRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)
	portRE     = regexp.MustCompile(`^[0-9]{1,8}$`)
	// repoPartRE is one component of a repository's path: lower-case, its
	// words joined by a dot, one or two underscores, or hyphens.
	repoPartRE = regexp.MustCompile(`^[a-z0-9]+((\.|__?|-+)[a-z0-9]+)*$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestRE   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

	nameRE  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// ValidImage reports whether ref is a well-formed image reference.
//
// It reads the reference as Docker does, part by part, rather than by one
// pattern: which part is the registry depends on what the first component
// looks like. A single pattern had to let the first component be upper-case
// in case it was a host, and so took NGINX:ALPINE, which Docker refuses at
// the pull, after the deployment has started.
//
// It is asked again at every deployment, so it must not refuse what an app
// may already be running from. Three things that Docker's own parser takes
// are refused all the same. Two always were here: a registry written as an
// IPv6 address in brackets, and an underscore in a registry's name. The
// third is new: a port that is no port (0, 99999), which Docker reads and
// then cannot pull from, so nothing runs from such a name.
func ValidImage(ref string) bool {
	if ref == "" || len(ref) > 255 {
		return false
	}
	name := ref
	if i := strings.IndexByte(name, '@'); i >= 0 {
		if !digestRE.MatchString(name[i+1:]) {
			return false
		}
		name = name[:i]
	}
	// A colon after the last slash starts the tag; one before it belongs
	// to a registry's port.
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		if !tagRE.MatchString(name[i+1:]) {
			return false
		}
		name = name[:i]
	}
	// The first component is a registry when it could not be anything
	// else: it has a dot or a port, is localhost, or has an upper-case
	// letter, which no repository has. Without a slash there is no
	// registry at all.
	repo := name
	if i := strings.IndexByte(name, '/'); i >= 0 {
		if first := name[:i]; strings.ContainsAny(first, ".:") || first == "localhost" || strings.ToLower(first) != first {
			if !validRegistry(first) {
				return false
			}
			repo = name[i+1:]
		}
	}
	for _, part := range strings.Split(repo, "/") {
		if !repoPartRE.MatchString(part) {
			return false
		}
	}
	return true
}

// validRegistry reports whether s is a registry's host, with or without a
// port.
func validRegistry(s string) bool {
	host, port, hasPort := strings.Cut(s, ":")
	if hasPort {
		if !portRE.MatchString(port) {
			return false
		}
		if n, _ := strconv.Atoi(port); n < 1 || n > 65535 {
			return false
		}
	}
	return registryRE.MatchString(host)
}

// ValidName reports whether s can name a container, network or volume.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidMountPath reports whether p is an absolute path that is safe inside a
// --mount option, whose fields are separated by commas.
func ValidMountPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 4096 {
		return false
	}
	for _, c := range p {
		if c == ',' || c == '\n' || c == '\r' || c == 0 || c == '=' {
			return false
		}
	}
	return !strings.Contains(p, "/../") && !strings.HasSuffix(p, "/..")
}

// CheckMountTarget reports why nothing can be mounted at a path of the
// container, or nil. Besides a path that is not one, these are the two
// places Docker itself refuses, which it does only when the container is
// started and in words about its own configuration.
func CheckMountTarget(target string) error {
	if !ValidMountPath(target) {
		return fmt.Errorf("mount target %q must be an absolute path without commas", target)
	}
	switch clean := path.Clean(target); {
	case clean == "/":
		return fmt.Errorf("nothing can be mounted at /, which is the container's whole file system: choose a directory in it, such as /data")
	case clean == "/proc" || strings.HasPrefix(clean, "/proc/"):
		return fmt.Errorf("nothing can be mounted at %s: /proc is where the system shows the container its processes, and Docker refuses a mount there", target)
	}
	return nil
}

// deniedBinds are host paths a container must never be given. Mounting any
// of them, or a directory that contains them, hands the container control of
// the host: the Docker socket is root, and the system directories hold
// credentials and devices.
var deniedBinds = []string{
	"/var/run", "/run", "/var/lib", "/var/spool", "/var/backups", "/var/log",
	"/etc", "/root", "/proc", "/sys", "/dev", "/boot", "/usr", "/bin", "/sbin", "/lib", "/lib64",
}

// safeCaps are capabilities that do not let a container act on the host.
// SYS_NICE is left out although it cannot reach the host: it would let one
// container take the processor from the proxy and the dashboard.
var safeCaps = map[string]bool{
	"NET_BIND_SERVICE": true, "CHOWN": true, "SETUID": true, "SETGID": true,
	"DAC_OVERRIDE": true, "FOWNER": true, "KILL": true, "IPC_LOCK": true,
}

// SafeCapability reports whether a container may be given a capability,
// written with or without its CAP_ prefix.
func SafeCapability(name string) bool {
	return safeCaps[strings.TrimPrefix(name, "CAP_")]
}

// CheckBindSource reports why a host path may not be bind-mounted, or nil.
// protected lists further directories to keep out of containers; musdash
// passes its own data directory, which holds the master key and database.
//
// A path is refused when it is a denied directory, lies inside one, or
// contains one (mounting /var would expose /var/run/docker.sock). The check
// is on the path as written: it cannot follow symlinks on a remote server,
// so bind mounts remain a power reserved for people trusted with the host.
func CheckBindSource(source string, protected ...string) error {
	if !ValidMountPath(source) {
		return fmt.Errorf("bind source %q must be an absolute path without commas", source)
	}
	clean := path.Clean(source)
	if clean == "/" {
		return fmt.Errorf("the whole filesystem cannot be mounted into a container")
	}
	for _, denied := range append(append([]string{}, deniedBinds...), protected...) {
		if denied == "" {
			continue
		}
		denied = path.Clean(denied)
		if clean == denied || strings.HasPrefix(clean, denied+"/") || strings.HasPrefix(denied, clean+"/") {
			return fmt.Errorf("%s cannot be mounted into a container: it would expose %s", source, denied)
		}
	}
	return nil
}

// Args builds the argument vector for `docker run`. Every value is validated
// and placed as its own argument or after its flag, so nothing a person
// typed can change which flags Docker sees.
func (s RunSpec) Args() ([]string, error) {
	if !ValidName(s.Name) {
		return nil, fmt.Errorf("bad container name %q", s.Name)
	}
	if !ValidImage(s.Image) {
		return nil, fmt.Errorf("%q is not a valid image name", s.Image)
	}
	args := []string{"run", "--detach"}
	if s.Local {
		args = append(args, "--pull", "never")
	}
	args = append(args,
		"--name", s.Name,
		"--restart", "unless-stopped",
		// Bound the container's log on disk; small servers fill up otherwise.
		"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
	)
	if s.Network != "" {
		if !ValidName(s.Network) {
			return nil, fmt.Errorf("bad network name %q", s.Network)
		}
		args = append(args, "--network", s.Network)
		if s.Alias != "" {
			if !ValidName(s.Alias) {
				return nil, fmt.Errorf("bad network alias %q", s.Alias)
			}
			args = append(args, "--network-alias", s.Alias)
		}
	}
	if s.HostPort != 0 {
		if !validPort(s.HostPort) || !validPort(s.ContainerPort) {
			return nil, fmt.Errorf("bad port mapping %d:%d", s.HostPort, s.ContainerPort)
		}
		// Loopback only: the proxy is the single way in from outside.
		args = append(args, "--publish", "127.0.0.1:"+strconv.Itoa(s.HostPort)+":"+strconv.Itoa(s.ContainerPort))
	}
	for _, p := range s.Ports {
		if !validPort(p.HostPort) || !validPort(p.ContainerPort) {
			return nil, fmt.Errorf("bad port mapping %d:%d", p.HostPort, p.ContainerPort)
		}
		args = append(args, "--publish", "127.0.0.1:"+strconv.Itoa(p.HostPort)+":"+strconv.Itoa(p.ContainerPort))
	}
	if s.PublicPort != 0 {
		if !validPort(s.PublicPort) || !validPort(s.ContainerPort) {
			return nil, fmt.Errorf("bad public port mapping %d:%d", s.PublicPort, s.ContainerPort)
		}
		args = append(args, "--publish", "0.0.0.0:"+strconv.Itoa(s.PublicPort)+":"+strconv.Itoa(s.ContainerPort))
	}
	if s.EnvFile != "" {
		args = append(args, "--env-file", s.EnvFile)
	}
	if s.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(s.MemoryMB)+"m")
	}
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.CPUs, 'f', -1, 64))
	}
	for _, m := range s.Mounts {
		opt, err := m.option()
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", opt)
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := s.Labels[k]
		if !labelRE.MatchString(k) || strings.ContainsAny(v, "\n\r") {
			return nil, fmt.Errorf("bad label %q", k)
		}
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, s.ExtraArgs...)
	args = append(args, s.Image)
	return append(args, s.Command...), nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func (m Mount) option() (string, error) {
	if err := CheckMountTarget(m.Target); err != nil {
		return "", err
	}
	switch m.Kind {
	case MountVolume:
		if !ValidName(m.Source) {
			return "", fmt.Errorf("bad volume name %q", m.Source)
		}
	case MountBind:
		if !m.Internal {
			if err := CheckBindSource(m.Source); err != nil {
				return "", err
			}
		} else if !ValidMountPath(m.Source) {
			return "", fmt.Errorf("bind source %q must be an absolute path without commas", m.Source)
		}
	default:
		return "", fmt.Errorf("unknown mount kind %q", m.Kind)
	}
	opt := "type=" + m.Kind + ",source=" + m.Source + ",target=" + m.Target
	if m.ReadOnly {
		opt += ",readonly"
	}
	return opt, nil
}
