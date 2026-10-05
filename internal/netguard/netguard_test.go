package netguard

import (
	"net/netip"
	"testing"
)

func TestCheck(t *testing.T) {
	p := Policy{Interfaces: func() []Interface {
		return []Interface{
			{Name: "lo", Prefix: netip.MustParsePrefix("127.0.0.1/8")},
			{Name: "eth0", Prefix: netip.MustParsePrefix("203.0.113.7/24")},
			{Name: "eth1", Prefix: netip.MustParsePrefix("10.0.0.5/16")},
			{Name: "docker0", Prefix: netip.MustParsePrefix("172.17.0.1/16")},
			{Name: "br-3f2a", Prefix: netip.MustParsePrefix("172.18.0.1/16")},
			{Name: "br-3f2a", Prefix: netip.MustParsePrefix("fd12:3456::1/64")},
		}
	}}
	for _, c := range []struct {
		addr string
		port int
		ok   bool
	}{
		{"93.184.216.34", 443, true},
		{"2606:2800:220:1::1", 443, true},
		// Another machine on the private network: a self-hosted chat.
		{"10.0.4.20", 8065, true},
		{"192.168.1.10", 25, true},
		// The server's own addresses: only the proxy's ports.
		{"203.0.113.7", 443, true},
		{"203.0.113.7", 80, true},
		{"203.0.113.7", 9100, false},
		{"10.0.0.5", 5432, false},
		{"172.17.0.1", 2375, false},
		// Containers' private addresses.
		{"172.17.0.5", 9200, false},
		{"172.18.255.254", 80, false},
		{"fd12:3456::42", 80, false},
		{"::ffff:172.18.0.9", 80, false},
		// Itself, nothing, everything, and the neighbours.
		{"127.0.0.1", 8000, false},
		{"::1", 8000, false},
		{"::ffff:127.0.0.1", 8000, false},
		{"0.0.0.0", 80, false},
		{"::", 80, false},
		{"224.0.0.1", 80, false},
		{"169.254.169.254", 80, false},
		{"fe80::1", 80, false},
		// Metadata services that are not link-local.
		{"100.100.100.200", 80, false},
		{"168.63.129.16", 80, false},
		{"fd00:ec2::254", 80, false},
	} {
		err := p.Check(netip.MustParseAddr(c.addr), c.port)
		if (err == nil) != c.ok {
			t.Errorf("%s port %d: %v, want allowed=%v", c.addr, c.port, err, c.ok)
		}
	}
	if err := (Policy{}).Check(netip.Addr{}, 80); err == nil {
		t.Error("an invalid address was allowed")
	}

	// A mail relay on the server itself, and the tests' own listeners.
	relay := Policy{LoopbackPorts: []int{25, 587}}
	if relay.Check(netip.MustParseAddr("127.0.0.1"), 25) != nil || relay.Check(netip.MustParseAddr("127.0.0.1"), 8000) == nil {
		t.Error("loopback ports")
	}
	if (Policy{AllowLoopback: true}).Check(netip.MustParseAddr("::1"), 8000) != nil {
		t.Error("AllowLoopback")
	}
	// The real interface list can be read.
	if err := (Policy{}).Check(netip.MustParseAddr("93.184.216.34"), 443); err != nil {
		t.Errorf("a public address with the real interfaces: %v", err)
	}
}
