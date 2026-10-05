// Package netguard decides which addresses musdash may connect to on a
// team member's say-so: a notification channel's URL, a storage bucket's
// endpoint.
//
// Such an address must not become a way to reach what only the server can
// reach: the server's own services, the private ports of other people's
// containers, or a cloud provider's metadata service. Other private
// addresses are allowed, because a self-hosted chat server, mail relay or
// object store usually has one.
package netguard

import (
	"errors"
	"net"
	"net/netip"
	"strings"
)

// ErrForbidden is returned for an address that may not be connected to.
var ErrForbidden = errors.New("this address cannot be used: it is the server itself, a container's private address or a link-local address")

// metadata are the cloud metadata services that are not link-local.
var metadata = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"),   // Azure
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
}

// Interface is one network interface of the server with one of its
// addresses.
type Interface struct {
	Name   string
	Prefix netip.Prefix // the interface's address and its network
}

// Policy is the rule set. The zero value is the one to use.
type Policy struct {
	// AllowLoopback permits the server's loopback addresses. Tests set it.
	AllowLoopback bool
	// LoopbackPorts are loopback ports that are permitted even so: a mail
	// relay on the same machine is a common, harmless setup.
	LoopbackPorts []int
	// Interfaces lists the server's interfaces. Tests replace it.
	Interfaces func() []Interface
}

// containerBridge reports whether an interface carries containers'
// private networks.
func containerBridge(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "cni", "podman", "virbr", "lxc", "lxd", "flannel", "cali"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// local returns the server's interfaces with their addresses.
func local() []Interface {
	var out []Interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if prefix, err := netip.ParsePrefix(a.String()); err == nil {
				out = append(out, Interface{Name: iface.Name, Prefix: netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits())})
			}
		}
	}
	return out
}

// Check reports whether a connection to ip on port is allowed.
func (p Policy) Check(ip netip.Addr, port int) error {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return ErrForbidden
	}
	if ip.IsLoopback() {
		if p.AllowLoopback {
			return nil
		}
		for _, allowed := range p.LoopbackPorts {
			if port == allowed {
				return nil
			}
		}
		return ErrForbidden
	}
	for _, m := range metadata {
		if ip == m {
			return ErrForbidden
		}
	}
	interfaces := p.Interfaces
	if interfaces == nil {
		interfaces = local
	}
	for _, iface := range interfaces() {
		if iface.Prefix.Addr() == ip {
			// One of the server's own addresses. Its web ports are the
			// proxy, which anyone on the internet reaches too; anything
			// else there is a service of the server itself.
			if port == 80 || port == 443 {
				return nil
			}
			return ErrForbidden
		}
		if containerBridge(iface.Name) && iface.Prefix.Masked().Contains(ip) {
			return ErrForbidden
		}
	}
	return nil
}
