package notify

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/netguard"
)

// A notification channel's address is typed by a team member. It must not
// become a way to make the server call itself, a container's private port,
// or a cloud provider's metadata service: netguard holds the rules. The
// check is made on the address a connection is actually opened to, after
// DNS has answered and for every redirect, so a public name that resolves
// to a forbidden address is refused too.

// ErrForbiddenAddress is returned for a destination notifications may not
// be sent to.
var ErrForbiddenAddress = errors.New("notifications cannot be sent to this address: it is the server itself, a container's private address or a link-local address")

// Dialer opens outgoing connections for notifications.
type Dialer struct {
	// AllowLoopback permits connections to the server itself. Tests set it.
	AllowLoopback bool
	// LoopbackPorts are ports of the server itself that are permitted even
	// so: a mail relay on the same machine is a common, harmless setup.
	LoopbackPorts []int
	// Interfaces replaces the server's interface list. Tests set it.
	Interfaces func() []netguard.Interface
}

func (d Dialer) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrForbiddenAddress
	}
	policy := netguard.Policy{AllowLoopback: d.AllowLoopback, LoopbackPorts: d.LoopbackPorts, Interfaces: d.Interfaces}
	if policy.Check(ap.Addr(), int(ap.Port())) != nil {
		return ErrForbiddenAddress
	}
	return nil
}

// DialContext connects to address unless it is a forbidden one.
func (d Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	nd := net.Dialer{Timeout: 10 * time.Second, Control: d.control}
	return nd.DialContext(ctx, network, address)
}

// HTTPClient returns a client for webhook calls: short timeouts, few
// redirects, no proxy from the environment, every connection checked.
func (d Dialer) HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
			// A notification is one small request; nothing is gained by
			// keeping connections, and idle ones cost memory.
			DisableKeepAlives: true,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}
