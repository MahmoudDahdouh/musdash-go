package compose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
)

// Endpoint is a port of one Compose service that gets a domain.
type Endpoint struct {
	// Name is the endpoint's name in the magic variables: the <NAME> of
	// SERVICE_FQDN_<NAME>_<PORT>.
	Name    string
	Service string // the Compose service that listens
	Port    int    // inside the container
}

// normalName is how a Compose service name appears in a variable name.
func normalName(service string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service))
}

// Endpoints works out, from the address variables a Compose file uses,
// which service and port each one stands for. raw must be the document
// loaded without interpolation, which still shows where variables are used.
//
// An endpoint belongs to the service it is named after; SERVICE_FQDN_N8N is
// the service "n8n". A name that matches no service belongs to the one
// service that mentions it.
func Endpoints(raw Project, vars []catalog.MagicVar) ([]Endpoint, error) {
	type group struct {
		port  int
		names []string // the variables of this endpoint
	}
	groups := map[string]*group{}
	for _, v := range vars {
		if !v.Address() {
			continue
		}
		g := groups[v.ID]
		if g == nil {
			g = &group{}
			groups[v.ID] = g
		}
		g.names = append(g.names, v.Name)
		if v.Port > 0 {
			if g.port > 0 && g.port != v.Port {
				return nil, fmt.Errorf("the variables for %s name two ports, %d and %d; an address goes to one port", v.ID, g.port, v.Port)
			}
			g.port = v.Port
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	services := raw.Services()
	var out []Endpoint
	for _, id := range ids {
		g := groups[id]
		owner := ""
		for _, s := range services {
			if normalName(s) == id {
				owner = s
			}
		}
		if owner == "" {
			var users []string
			for _, s := range services {
				for _, name := range g.names {
					if raw.Mentions(s, name) {
						users = append(users, s)
						break
					}
				}
			}
			if len(users) != 1 {
				return nil, fmt.Errorf("%s does not say which service it is the address of: name it after the service, as in SERVICE_FQDN_%s", g.names[0], normalName(firstOr(services, "app")))
			}
			owner = users[0]
		}
		port := g.port
		if port == 0 {
			port = raw.FirstPort(owner)
		}
		if port == 0 {
			return nil, fmt.Errorf("%s does not say which port %s listens on: add it to the name, as in %s_3000", g.names[0], owner, g.names[0])
		}
		out = append(out, Endpoint{Name: id, Service: owner, Port: port})
	}
	// Two names for one service and port would publish it twice.
	seen := map[string]string{}
	for _, e := range out {
		key := e.Service + ":" + strconv.Itoa(e.Port)
		if other, dup := seen[key]; dup {
			return nil, fmt.Errorf("%s and %s are both the address of %s port %d; use one name", other, e.Name, e.Service, e.Port)
		}
		seen[key] = e.Name
	}
	return out, nil
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}

// Published is an endpoint with the loopback port of the server it is
// published on.
type Published struct {
	Service  string
	Port     int
	HostPort int
}

// Override is what musdash adds to a stack before starting it.
type Override struct {
	// ServiceID is the musdash service the stack belongs to.
	ServiceID string
	// Publish are the endpoints, each on a loopback port for the proxy.
	Publish []Published
	// EnvNetwork, when set, is the environment's Docker network, which
	// every service of the stack joins.
	EnvNetwork string
}

// envNetworkKey is the name the environment's network has inside the stack.
const envNetworkKey = "musdash-environment"

// Apply amends a validated document with what musdash needs: labels to
// find the containers by, the endpoints' ports, a restart policy, and
// optionally the environment's network.
func (p Project) Apply(o Override) {
	services := asMap(p.doc["services"])
	for name, raw := range services {
		svc := asMap(raw)
		if svc == nil {
			continue
		}
		labels := asMap(svc["labels"])
		if labels == nil {
			labels = map[string]any{}
			svc["labels"] = labels
		}
		labels[docker.ManagedLabel] = "true"
		labels[docker.LabelKind] = "service"
		labels[docker.LabelResource] = o.ServiceID

		// A stack comes back after a reboot unless its file says otherwise.
		if svc["restart"] == nil {
			svc["restart"] = "unless-stopped"
		}
		// A built image is named by musdash. The file's own name could be
		// that of an image something else on the server runs from.
		if svc["build"] != nil {
			svc["image"] = p.Name() + "-" + name
		}
		for _, pub := range o.Publish {
			if pub.Service != name {
				continue
			}
			svc["ports"] = append(asList(svc["ports"]), map[string]any{
				"mode": "ingress", "host_ip": "127.0.0.1", "protocol": "tcp",
				"target": pub.Port, "published": strconv.Itoa(pub.HostPort),
			})
		}
		// A service that shares another's network, or has none, cannot
		// join one of its own.
		if mode, _ := svc["network_mode"].(string); o.EnvNetwork != "" && (mode == "" || mode == "bridge") {
			networks := asMap(svc["networks"])
			if networks == nil {
				networks = map[string]any{"default": nil}
				svc["networks"] = networks
			}
			networks[envNetworkKey] = nil
		}
	}
	if o.EnvNetwork != "" {
		networks := asMap(p.doc["networks"])
		if networks == nil {
			networks = map[string]any{}
			p.doc["networks"] = networks
		}
		networks[envNetworkKey] = map[string]any{"name": o.EnvNetwork, "external": true}
	}
}

// Volumes returns the Docker names of the stack's named volumes.
func (p Project) Volumes() []string {
	var names []string
	for _, v := range asMap(p.doc["volumes"]) {
		if name, _ := asMap(v)["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
