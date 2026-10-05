package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Route says what the proxy does for one host name, or for the part of it
// under one path.
type Route struct {
	Host string `json:"host"`
	// Path, when set, limits the route to that path and what is below it:
	// "/api" takes "/api" and "/api/users", not "/apix". Of a host's routes
	// the one with the longest matching path is used.
	Path string `json:"path,omitempty"`
	// StripPrefix removes Path from the request before it is passed on, so
	// the app behind "/api" sees "/users" for "/api/users".
	StripPrefix bool `json:"strip_prefix,omitempty"`
	// AuthUser and AuthHash (bcrypt) put a user name and password in front
	// of the route.
	AuthUser string `json:"auth_user,omitempty"`
	AuthHash string `json:"auth_hash,omitempty"`
	// Target is the loopback address of the container's published port,
	// such as "127.0.0.1:20417".
	Target string `json:"target,omitempty"`
	// TLS requests a certificate for the host and redirects HTTP to HTTPS.
	TLS bool `json:"tls"`
	// RedirectTo sends every request to the same path on another host. It is
	// how "www.example.com" forwards to "example.com" or the reverse.
	RedirectTo string `json:"redirect_to,omitempty"`
}

// File is the content of routes.json, written by the control plane.
type File struct {
	// Email is given to the certificate authority for expiry notices.
	Email  string  `json:"email,omitempty"`
	Routes []Route `json:"routes"`
}

// Table is an immutable, validated route set. The proxy swaps whole tables
// atomically, so a request never sees a half-updated one.
type Table struct {
	Email string
	// hosts holds each host's routes, the longest path first.
	hosts map[string][]Route
}

// maxAuthCost bounds the work one password check may cost the proxy.
const maxAuthCost = 12

// maxRoutesBytes bounds how much of routes.json is read.
const maxRoutesBytes = 8 << 20

// Load reads and validates a routes file. A missing file is an empty table:
// a fresh install has nothing deployed yet.
func Load(path string) (*Table, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Table{hosts: map[string][]Route{}}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse validates a routes document.
func Parse(r io.Reader) (*Table, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxRoutesBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRoutesBytes {
		return nil, fmt.Errorf("routes file is larger than %d bytes", maxRoutesBytes)
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("routes file: %w", err)
	}
	t := &Table{Email: file.Email, hosts: make(map[string][]Route, len(file.Routes))}
	for i, rt := range file.Routes {
		host := NormalizeHost(rt.Host)
		if !ValidHost(host) {
			return nil, fmt.Errorf("route %d: %q is not a valid host name", i, rt.Host)
		}
		if !ValidPath(rt.Path) {
			return nil, fmt.Errorf("route %d (%s): %q is not a valid path prefix", i, host, rt.Path)
		}
		for _, other := range t.hosts[host] {
			if other.Path == rt.Path {
				return nil, fmt.Errorf("route %d: %s%s appears twice", i, host, rt.Path)
			}
		}
		if (rt.AuthUser == "") != (rt.AuthHash == "") {
			return nil, fmt.Errorf("route %d (%s): a user name and a password hash go together", i, host)
		}
		if rt.AuthHash != "" {
			cost, err := bcrypt.Cost([]byte(rt.AuthHash))
			if err != nil || cost > maxAuthCost || strings.ContainsAny(rt.AuthUser, ":\r\n") {
				return nil, fmt.Errorf("route %d (%s): bad user name or password hash", i, host)
			}
		}
		rt.Host = host
		switch {
		case rt.RedirectTo != "" && rt.Target != "":
			return nil, fmt.Errorf("route %d (%s): set target or redirect_to, not both", i, host)
		case rt.RedirectTo != "" && rt.Path != "":
			return nil, fmt.Errorf("route %d (%s): a redirect is for a whole host, not a path", i, host)
		case rt.RedirectTo != "":
			rt.RedirectTo = NormalizeHost(rt.RedirectTo)
			if !ValidHost(rt.RedirectTo) || rt.RedirectTo == host {
				return nil, fmt.Errorf("route %d (%s): bad redirect_to %q", i, host, rt.RedirectTo)
			}
		default:
			if err := validTarget(rt.Target); err != nil {
				return nil, fmt.Errorf("route %d (%s): %w", i, host, err)
			}
		}
		t.hosts[host] = append(t.hosts[host], rt)
	}
	for host, routes := range t.hosts {
		// Longest path first, so the first match is the most specific.
		sort.SliceStable(routes, func(i, j int) bool { return len(routes[i].Path) > len(routes[j].Path) })
		// One certificate serves a whole host: if any of its paths wants
		// HTTPS, all of them are served over it.
		tls := false
		for _, rt := range routes {
			tls = tls || rt.TLS
		}
		for i := range routes {
			routes[i].TLS = tls
		}
		t.hosts[host] = routes
	}
	return t, nil
}

// ValidPath reports whether p can be a route's path prefix: empty for the
// whole host, or an absolute path in its simplest form without a trailing
// slash.
func ValidPath(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 200 || !strings.HasPrefix(p, "/") || p == "/" || path.Clean(p) != p {
		return false
	}
	for _, c := range p {
		if c <= ' ' || c == 0x7f || c == '?' || c == '#' || c == '%' || c == '\\' {
			return false
		}
	}
	return true
}

// under reports whether a request path is the prefix or lies below it.
func under(requestPath, prefix string) bool {
	return prefix == "" || requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

// Lookup finds the route for a request's Host header and path. The path
// must be in its simplest form (see Plain); the proxy sends a client that
// asked for anything else to that form first.
func (t *Table) Lookup(hostport, requestPath string) (Route, bool) {
	for _, rt := range t.hosts[NormalizeHost(hostport)] {
		if under(requestPath, rt.Path) {
			return rt, true
		}
	}
	return Route{}, false
}

// Host reports whether a host is routed at all, whether it wants HTTPS,
// and whether which of its routes serves a request depends on the path or
// is guarded by a password.
func (t *Table) Host(hostport string) (routed, tls, strict bool) {
	routes := t.hosts[NormalizeHost(hostport)]
	for _, rt := range routes {
		tls = tls || rt.TLS
		strict = strict || rt.Path != "" || rt.AuthUser != ""
	}
	return len(routes) > 0, tls, strict
}

// Plain returns a request path in its simplest form: no "." or ".."
// segments and no doubled slashes. A trailing slash is kept, because it
// means something to the app behind.
func Plain(p string) string {
	if p == "" {
		return "/"
	}
	clean := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	return clean
}

// Len reports how many hosts are routed.
func (t *Table) Len() int { return len(t.hosts) }

// validTarget accepts only loopback ip:port. Containers publish to loopback,
// and refusing anything else means a tampered routes file cannot turn the
// proxy into a relay to other machines.
func validTarget(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("target %q is not ip:port", target)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("target %q must be a loopback address", target)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("target %q has a bad port", target)
	}
	return nil
}

// NormalizeHost lower-cases a Host header value and removes the port and any
// trailing dot.
func NormalizeHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// ValidHost reports whether host is a syntactically valid DNS name: labels
// of letters, digits and hyphens, not starting or ending with a hyphen. It
// must already be normalised. Bare IP addresses are rejected: a route needs
// a name, and certificates are not issued for addresses.
func ValidHost(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
