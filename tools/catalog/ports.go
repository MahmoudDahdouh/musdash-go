package main

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A template keeps the ports its source published with a port of the
// server named: they are the protocols the proxy cannot carry (SSH, MQTT,
// a game's UDP port). What musdash's own rule for a published port refuses
// is moved where it can be, and dropped where it cannot.

// The ports a stack may publish (deploy.ValidPublicPort): not the
// privileged ones, and not the range musdash gives out on loopback.
const (
	lowestPort  = 1024
	highestPort = 65535
	ownFirst    = 20000
	ownLast     = 29999
	// moved is how far a port in musdash's own range is moved up.
	moved = 10000
)

// srcPort is one entry of a service's "ports", as its source wrote it.
type srcPort struct {
	host   string // "22222", "5060-5063", "${PORT}", "${DHT_PORT:-6881}"; empty when none is named
	target string // "22", "5060-5063"
	udp    bool
	bound  bool // an address of the server in front
}

// readPort reads a port in either of Compose's two forms.
func readPort(n *yaml.Node) (srcPort, bool) {
	if isMap(n) {
		p := srcPort{}
		if v := mapGet(n, "published"); isStr(v) {
			p.host = v.Value
		}
		if v := mapGet(n, "target"); isStr(v) {
			p.target = v.Value
		}
		if v := mapGet(n, "protocol"); isStr(v) {
			p.udp = v.Value == "udp"
		}
		if v := mapGet(n, "host_ip"); isStr(v) && v.Value != "" {
			p.bound = true
		}
		return p, p.target != ""
	}
	if !isStr(n) {
		return srcPort{}, false
	}
	spec, proto, _ := strings.Cut(n.Value, "/")
	parts := splitOutsideBraces(spec)
	p := srcPort{udp: proto == "udp"}
	if proto != "" && proto != "udp" && proto != "tcp" {
		return p, false
	}
	switch len(parts) {
	case 1:
		p.target = parts[0]
	case 2:
		p.host, p.target = parts[0], parts[1]
	case 3:
		p.bound, p.host, p.target = true, parts[1], parts[2]
	default:
		return p, false
	}
	return p, p.target != ""
}

// splitOutsideBraces cuts at the colons that are not inside "${…}", where
// one is part of a default ("${PORT:-25565}").
func splitOutsideBraces(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// portRange reads "22" or "5060-5063".
func portRange(s string) (lo, hi int, ok bool) {
	a, b, ranged := strings.Cut(s, "-")
	lo, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	hi = lo
	if ranged {
		if hi, err = strconv.Atoi(b); err != nil {
			return 0, 0, false
		}
	}
	return lo, hi, lo >= 1 && hi >= lo && hi <= highestPort
}

// place is where ports of the server from lo to hi may be published: as
// they are, moved out of musdash's own range, or not at all.
func place(lo, hi int) (int, int, bool) {
	if lo < lowestPort {
		return 0, 0, false
	}
	if hi >= ownFirst && lo <= ownLast {
		lo, hi = lo+moved, hi+moved
	}
	if hi > highestPort || (hi >= ownFirst && lo <= ownLast) {
		return 0, 0, false
	}
	return lo, hi, true
}

func rangeText(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
}

// kept is the port as a template publishes it, and whether it does. web
// are the ports of the service that its address goes to: the proxy has
// those.
func (p srcPort) kept(web map[int]bool) (string, bool) {
	if p.host == "" || p.bound {
		// With no port of the server named, Compose takes any, and no
		// two starts would agree on it.
		return "", false
	}
	tlo, thi, ok := portRange(p.target)
	if !ok {
		return "", false
	}
	if !p.udp && tlo == thi && web[tlo] {
		return "", false
	}
	var host string
	if lo, hi, literal := portRange(p.host); literal {
		if hi-lo != thi-tlo {
			return "", false
		}
		lo, hi, ok = place(lo, hi)
		if !ok {
			return "", false
		}
		host = rangeText(lo, hi)
	} else {
		// A variable: its default is the port it had, or the container's
		// own when its source left that to the person.
		name, def, isVar := variablePort(p.host)
		if !isVar || tlo != thi {
			return "", false
		}
		if def == 0 {
			def = tlo
		}
		at, _, ok := place(def, def)
		if !ok {
			return "", false
		}
		host = "${" + name + ":-" + strconv.Itoa(at) + "}"
		// A port that had to move is written as the number: the variable
		// may be read elsewhere in the file, where its default is the
		// port its source meant, and one name with two defaults says
		// nothing a person can follow.
		if at != def {
			host = strconv.Itoa(at)
		}
	}
	out := host + ":" + p.target
	if p.udp {
		out += "/udp"
	}
	return out, true
}

// variablePort reads "${PORT}", "$PORT" or "${PORT:-25565}".
func variablePort(s string) (name string, def int, ok bool) {
	rest, braced := strings.CutPrefix(s, "${")
	if !braced {
		rest, ok = strings.CutPrefix(s, "$")
		if !ok || rest == "" {
			return "", 0, false
		}
		for i := 0; i < len(rest); i++ {
			if !nameByte(rest[i], i == 0) {
				return "", 0, false
			}
		}
		return rest, 0, true
	}
	rest, ok = strings.CutSuffix(rest, "}")
	if !ok {
		return "", 0, false
	}
	end := 0
	for end < len(rest) && nameByte(rest[end], end == 0) {
		end++
	}
	name, rest = rest[:end], rest[end:]
	if name == "" {
		return "", 0, false
	}
	switch {
	case rest == "":
		return name, 0, true
	case strings.HasPrefix(rest, ":-"):
		rest = rest[2:]
	case strings.HasPrefix(rest, "-"):
		rest = rest[1:]
	default:
		// "${PORT:?…}" asks for it: the default answers.
		return name, 0, true
	}
	def, err := strconv.Atoi(rest)
	if err != nil {
		return name, 0, true
	}
	return name, def, true
}

// publish gives the services back the ports of their source that a
// template keeps. It runs when the addresses are placed, since a port an
// address goes to is the proxy's.
func (c *converter) publish() {
	taken := map[string]bool{}
	for _, name := range mapKeys(c.services) {
		ports := c.srcPorts[name]
		if ports == nil {
			continue
		}
		svc := mapGet(c.services, name)
		web := c.webPorts(name)
		seq := newSeq()
		// A service that needs a port under 1024 (mail, DNS) is not
		// reached without it: its other ports alone would open something
		// on the server for nothing.
		if needsLowPort(ports) {
			continue
		}
		for _, n := range ports.Content {
			p, ok := readPort(n)
			if !ok {
				continue
			}
			out, ok := p.kept(web)
			if !ok {
				continue
			}
			// Two services of a template cannot have one port of the
			// server: the first keeps it.
			at := out[:strings.LastIndexByte(out, ':')]
			if p.udp {
				at += "/udp"
			}
			if taken[at] {
				continue
			}
			taken[at] = true
			quoted := str(out)
			quoted.Style = yaml.DoubleQuotedStyle
			seq.Content = append(seq.Content, quoted)
		}
		if len(seq.Content) > 0 {
			mapSet(svc, "ports", seq)
			c.kept += len(seq.Content)
		}
	}
}

// webPorts are the ports of a service that an address of the template goes
// to: one named after the service, wherever it is written, and one that
// only the service itself names.
func (c *converter) webPorts(service string) map[int]bool {
	others := map[string]bool{}
	for _, name := range mapKeys(c.services) {
		if name != service {
			others[normalName(name)] = true
		}
	}
	web := map[int]bool{}
	read := func(own bool) func(n *yaml.Node) {
		return func(n *yaml.Node) {
			for _, m := range addressRE.FindAllStringSubmatch(n.Value, -1) {
				id := m[2]
				i := strings.LastIndexByte(id, '_')
				if i <= 0 {
					continue
				}
				port, err := strconv.Atoi(id[i+1:])
				if err != nil {
					continue
				}
				if id[:i] == normalName(service) || (own && !others[id[:i]]) {
					web[port] = true
				}
			}
		}
	}
	scalars(c.services, read(false))
	scalars(mapGet(c.services, service), read(true))
	return web
}

// needsLowPort reports whether a service published a port of the server
// under 1024, which a stack may not.
func needsLowPort(ports *yaml.Node) bool {
	for _, n := range ports.Content {
		if p, ok := readPort(n); ok && !p.bound {
			if lo, _, literal := portRange(p.host); literal && lo < lowestPort {
				return true
			}
		}
	}
	return false
}
