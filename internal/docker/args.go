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

// RunSpec describes a container to start.
type RunSpec struct {
	Name          string
	Image         string
	Network       string
	Alias         string // DNS name on the network
	HostPort      int    // published on 127.0.0.1; 0 publishes nothing
	ContainerPort int
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

var (
	// imageRE follows Docker's reference grammar: optional registry host and
	// port, lower-case path components, optional tag, optional digest. It
	// cannot match anything starting with "-", so an image name can never be
	// read as a flag.
	imageRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(:[0-9]+)?(/[a-z0-9]+([._-]+[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)
	nameRE  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// ValidImage reports whether ref is a well-formed image reference.
func ValidImage(ref string) bool {
	return len(ref) <= 255 && imageRE.MatchString(ref)
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

// deniedBinds are host paths a container must never be given. Mounting any
// of them, or a directory that contains them, hands the container control of
// the host: the Docker socket is root, and the system directories hold
// credentials and devices.
var deniedBinds = []string{
	"/var/run", "/run", "/var/lib/docker", "/var/lib/containerd",
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
	args := []string{
		"run", "--detach",
		"--name", s.Name,
		"--restart", "unless-stopped",
		// Bound the container's log on disk; small servers fill up otherwise.
		"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
	}
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
	if !ValidMountPath(m.Target) {
		return "", fmt.Errorf("mount target %q must be an absolute path without commas", m.Target)
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
