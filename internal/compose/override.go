package compose

import (
	"fmt"
	"path"
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
	// ReadOnlyUnder, when set, is the checkout of a stack from a Git
	// repository: bind mounts from inside it are made read-only.
	ReadOnlyUnder string
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

		// A variable or build argument named without a value means "take
		// it from where Compose runs". The sandbox has already filled in
		// the ones the stack defines; any still empty would be read from
		// musdash's own environment when the stack is built or started.
		for _, vars := range []map[string]any{asMap(svc["environment"]), asMap(asMap(svc["build"])["args"])} {
			for key, value := range vars {
				if value == nil {
					delete(vars, key)
				}
			}
		}
		// Files of the repository are mounted read-only. A container that
		// could write to the checkout could leave a link there for another
		// mount, of this deployment or the next, to follow out of it.
		if o.ReadOnlyUnder != "" {
			for _, m := range asList(svc["volumes"]) {
				mount := asMap(m)
				if source, _ := mount["source"].(string); mount["type"] == "bind" && inside(source, o.ReadOnlyUnder) {
					mount["read_only"] = true
					// A path the repository does not have is an error, not
					// a directory for Docker to create, as root, inside the
					// checkout.
					bind := asMap(mount["bind"])
					if bind == nil {
						bind = map[string]any{}
						mount["bind"] = bind
					}
					bind["create_host_path"] = false
				}
			}
		}
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

// CheckoutPaths returns every path inside the checkout that the stack reads
// from the server: bind-mount sources, build contexts and Dockerfiles, and
// the files of secrets and configs. The caller makes sure none of them is,
// or passes through, a symbolic link of the repository.
func (p Project) CheckoutPaths(checkout string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v any) {
		s, _ := v.(string)
		if s = path.Clean(s); inside(s, checkout) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, raw := range asMap(p.doc["services"]) {
		svc := asMap(raw)
		for _, m := range asList(svc["volumes"]) {
			if mount := asMap(m); mount["type"] == "bind" {
				add(mount["source"])
			}
		}
		if b := asMap(svc["build"]); b != nil {
			context, _ := b["context"].(string)
			add(context)
			if dockerfile, _ := b["dockerfile"].(string); dockerfile != "" {
				if !path.IsAbs(dockerfile) {
					dockerfile = path.Join(context, dockerfile)
				}
				add(dockerfile)
			} else if b["dockerfile_inline"] == nil {
				add(path.Join(context, "Dockerfile"))
			}
		}
	}
	for _, kind := range []string{"secrets", "configs"} {
		for _, raw := range asMap(p.doc[kind]) {
			add(asMap(raw)["file"])
		}
	}
	sort.Strings(out)
	return out
}

// Builds reports whether any service of the stack is built from source.
func (p Project) Builds() bool {
	for _, raw := range asMap(p.doc["services"]) {
		if asMap(raw)["build"] != nil {
			return true
		}
	}
	return false
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

// Nested is a mount whose target lies inside another mount of the same
// service that comes from the checkout.
type Nested struct {
	Service string
	Target  string // where the mount is, in the container
	Under   string // the target of the mount from the checkout it lies in
	Path    string // the directory of the checkout it would be mounted on
}

// NestedInCheckout returns the mounts that need a directory of the checkout
// to be mounted on. Files of the checkout are mounted read-only (see
// Apply), so Docker cannot make that directory, as it would under a mount
// that can be written to: it has to be in the repository. The usual case
// is a development file with "./app:/app" and a volume at
// "/app/node_modules".
func (p Project) NestedInCheckout(checkout string) []Nested {
	var out []Nested
	services := asMap(p.doc["services"])
	for _, name := range sortedKeys(services) {
		mounts := asList(asMap(services[name])["volumes"])
		for _, m := range mounts {
			target, _ := asMap(m)["target"].(string)
			if !path.IsAbs(target) {
				continue
			}
			// The innermost mount from the checkout that the target is in
			// is the one whose directory it lands on.
			var under, source string
			for _, o := range mounts {
				other := asMap(o)
				t, _ := other["target"].(string)
				s, _ := other["source"].(string)
				if other["type"] == "bind" && inside(s, checkout) && path.IsAbs(t) &&
					path.Clean(t) != path.Clean(target) && inside(target, t) && len(path.Clean(t)) > len(under) {
					under, source = path.Clean(t), path.Clean(s)
				}
			}
			if under == "" {
				continue
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(path.Clean(target), under), "/")
			out = append(out, Nested{Service: name, Target: path.Clean(target), Under: under, Path: path.Join(source, rel)})
		}
	}
	return out
}

// EnvFile is a file a service takes variables from.
type EnvFile struct {
	Service string
	Path    string
}

// EnvFiles returns what the services name under "env_file". Only a
// document loaded raw has them: when Compose fills a document in, it reads
// the files into "environment" and drops the key, so that what a file
// held cannot be told from what the Compose file said.
func (p Project) EnvFiles() []EnvFile {
	var out []EnvFile
	services := asMap(p.doc["services"])
	for _, name := range sortedKeys(services) {
		for _, entry := range asList(asMap(services[name])["env_file"]) {
			file, ok := entry.(string)
			if !ok {
				file, _ = asMap(entry)["path"].(string)
			}
			out = append(out, EnvFile{Service: name, Path: file})
		}
	}
	return out
}
