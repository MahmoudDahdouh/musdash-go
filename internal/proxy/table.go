package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
)

// Route says what the proxy does for one host name.
type Route struct {
	Host string `json:"host"`
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
	hosts map[string]Route
}

// maxRoutesBytes bounds how much of routes.json is read.
const maxRoutesBytes = 8 << 20

// Load reads and validates a routes file. A missing file is an empty table:
// a fresh install has nothing deployed yet.
func Load(path string) (*Table, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Table{hosts: map[string]Route{}}, nil
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
	t := &Table{Email: file.Email, hosts: make(map[string]Route, len(file.Routes))}
	for i, rt := range file.Routes {
		host := NormalizeHost(rt.Host)
		if !ValidHost(host) {
			return nil, fmt.Errorf("route %d: %q is not a valid host name", i, rt.Host)
		}
		if _, dup := t.hosts[host]; dup {
			return nil, fmt.Errorf("route %d: host %q appears twice", i, host)
		}
		rt.Host = host
		switch {
		case rt.RedirectTo != "" && rt.Target != "":
			return nil, fmt.Errorf("route %d (%s): set target or redirect_to, not both", i, host)
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
		t.hosts[host] = rt
	}
	return t, nil
}

// Lookup finds the route for a request's Host header.
func (t *Table) Lookup(hostport string) (Route, bool) {
	rt, ok := t.hosts[NormalizeHost(hostport)]
	return rt, ok
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
