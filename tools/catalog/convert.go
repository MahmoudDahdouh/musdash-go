package main

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// What a catalogue's own platform reads and Compose does not, and options
// that only tune how the server shares itself out, which musdash's rules
// leave to the server.
var dropKeys = []string{
	"container_name", "cpu_shares", "cpu_count", "cpu_percent", "cpuset", "cpu_quota", "cpu_period",
	"memswap_limit", "mem_swappiness", "oom_score_adj", "blkio_config", "isolation",
}

var dropLabels = regexp.MustCompile(`^(traefik|caddy|coolify|dokploy|com\.centurylinklabs\.watchtower|diun|homepage)\b`)

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// fileNameRE is a path that names a file: mounting a volume there would
// put a directory where the program expects to read.
var fileNameRE = regexp.MustCompile(`(?i)(\.(ya?ml|json|conf|cnf|cfg|toml|ini|env|sql|sh|js|ts|xml|txt|properties|html?|php|py|rb|lua|key|pem|crt|cert|db|sqlite3?|htpasswd|list|rules|hcl|exs|vcl|caddy)|/Caddyfile|/Corefile|/Dockerfile)$`)

// serverOwn are the paths that are the server itself, not a place a
// template keeps its data in.
var serverOwn = regexp.MustCompile(`^/($|(root|home)/?$|(bin|boot|dev|etc|lib|lib64|proc|run|sbin|sys|usr|var/run|var/log|var/lib|tmp|host|hostfs|rootfs)(/|$))`)

type converter struct {
	t        *tmpl
	services *yaml.Node
	volumes  map[string]bool // named volumes the services mount
	configs  *yaml.Node
	ports    map[string]int // the first TCP port of its container a service published
	// published says a service published a port of any kind.
	published bool
	// srcPorts are the "ports" of each service as its source wrote them,
	// and kept is how many of them the template publishes (ports.go).
	srcPorts map[string]*yaml.Node
	kept     int
}

// convert changes a template into one musdash's rules accept. An error is
// the reason it cannot be.
func (t *tmpl) convert() error {
	root := t.Root
	for _, k := range mapKeys(root) {
		if k == "version" || k == "name" || strings.HasPrefix(k, "x-") {
			mapDel(root, k)
		}
	}
	c := &converter{t: t, services: mapGet(root, "services"), volumes: map[string]bool{}, ports: map[string]int{}, srcPorts: map[string]*yaml.Node{}}
	if !isMap(c.services) || len(c.services.Content) == 0 {
		return fmt.Errorf("its Compose file has no services")
	}
	if c.configs = mapGet(root, "configs"); !isMap(c.configs) {
		c.configs = newMap()
	}
	t.notWaited = map[string]bool{}
	c.networks()
	c.links()
	for _, name := range mapKeys(c.services) {
		svc := mapGet(c.services, name)
		if !isMap(svc) {
			return fmt.Errorf("its service %s is empty", name)
		}
		if mapGet(svc, "build") != nil {
			return fmt.Errorf("it builds an image from source, which a template has none of")
		}
		if mapGet(svc, "env_file") != nil {
			return fmt.Errorf("it reads its variables from a file next to the Compose file")
		}
		for _, k := range dropKeys {
			mapDel(svc, k)
		}
		// A policy that is a variable cannot be read before the variable
		// is filled in; the default policy does.
		if p := mapGet(svc, "pull_policy"); isStr(p) && strings.Contains(p.Value, "$") {
			mapDel(svc, "pull_policy")
		}
		if truthy(mapDel(svc, "exclude_from_hc")) {
			t.notWaited[name] = true
		}
		if ports := mapDel(svc, "ports"); isSeq(ports) {
			// UDP counts: it is how a game is reached.
			c.published = c.published || len(ports.Content) > 0
			c.srcPorts[name] = ports
			for _, p := range ports.Content {
				if n := containerPort(p); n > 0 && c.ports[name] == 0 {
					c.ports[name] = n
				}
			}
		}
		c.labels(svc)
		if image := mapGet(svc, "image"); isStr(image) && !strings.Contains(image.Value, "$") {
			last := image.Value[strings.LastIndex(image.Value, "/")+1:]
			if !strings.ContainsAny(last, ":@") {
				setValue(image, image.Value+":latest")
			}
		}
		c.deploy(svc)
		if err := c.mounts(name, svc); err != nil {
			return err
		}
	}
	c.declareVolumes()
	if len(c.configs.Content) > 0 {
		mapSet(root, "configs", c.configs)
	}
	c.defaults()
	if err := c.endpoints(); err != nil {
		return err
	}
	c.publish()
	if err := c.oneShots(); err != nil {
		return err
	}
	t.Published = c.published
	t.Ports = c.kept
	t.Addressed = addressRE.MatchString(c.text())
	c.passwords()
	return nil
}

// text is every scalar of the file, for a search.
func (c *converter) text() string {
	var b strings.Builder
	scalars(c.t.Root, func(n *yaml.Node) {
		b.WriteString(n.Value)
		b.WriteByte('\n')
	})
	return b.String()
}

func containerPort(p *yaml.Node) int {
	if isMap(p) {
		if target := mapGet(p, "target"); isStr(target) {
			n, _ := strconv.Atoi(target.Value)
			return n
		}
		return 0
	}
	if !isStr(p) {
		return 0
	}
	spec, proto, _ := strings.Cut(p.Value, "/")
	if proto != "" && proto != "tcp" {
		return 0
	}
	n, _ := strconv.Atoi(spec[strings.LastIndexByte(spec, ':')+1:])
	return n
}

// networks makes every network the stack's own. One that the source's
// platform provides (an external one, as Dokploy's proxy network is)
// becomes a plain network of the stack: the services that shared it still
// do.
func (c *converter) networks() {
	top := mapGet(c.t.Root, "networks")
	if isMap(top) {
		for i := 1; i < len(top.Content); i += 2 {
			def := top.Content[i]
			if !isMap(def) {
				continue
			}
			for _, k := range mapKeys(def) {
				if k != "internal" && k != "labels" && k != "enable_ipv6" {
					mapDel(def, k)
				}
			}
			if len(def.Content) == 0 {
				top.Content[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
			}
		}
	}
	for _, name := range mapKeys(c.services) {
		nets := mapGet(mapGet(c.services, name), "networks")
		if !isMap(nets) {
			continue
		}
		for i := 1; i < len(nets.Content); i += 2 {
			// A fixed address needs a subnet, which a stack may not choose.
			if def := nets.Content[i]; isMap(def) {
				for _, k := range mapKeys(def) {
					if k != "aliases" && k != "priority" {
						mapDel(def, k)
					}
				}
				if len(def.Content) == 0 {
					nets.Content[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
				}
			}
		}
	}
}

// links removes "links", which a stack may not use: its services reach each
// other by name as it is. A link that gives a service a second name
// ("api:backend") is kept as what it is, a name on the network.
func (c *converter) links() {
	for _, name := range mapKeys(c.services) {
		list := mapDel(mapGet(c.services, name), "links")
		if !isSeq(list) {
			continue
		}
		for _, link := range list.Content {
			target, alias, ok := strings.Cut(link.Value, ":")
			svc := mapGet(c.services, target)
			if !ok || alias == target || !isMap(svc) {
				continue
			}
			nets := mapGet(svc, "networks")
			switch {
			case isSeq(nets):
				// The short form has no room for a name: written out.
				long := newMap()
				for _, n := range nets.Content {
					long.Content = append(long.Content, str(n.Value), newMap())
				}
				nets = long
				mapSet(svc, "networks", nets)
			case !isMap(nets):
				nets = newMap()
				nets.Content = append(nets.Content, str("default"), newMap())
				mapSet(svc, "networks", nets)
			}
			for i := 1; i < len(nets.Content); i += 2 {
				if !isMap(nets.Content[i]) {
					nets.Content[i] = newMap()
				}
				aliases := mapGet(nets.Content[i], "aliases")
				if !isSeq(aliases) {
					aliases = newSeq()
					mapSet(nets.Content[i], "aliases", aliases)
				}
				has := false
				for _, a := range aliases.Content {
					has = has || a.Value == alias
				}
				if !has {
					aliases.Content = append(aliases.Content, str(alias))
				}
			}
		}
	}
}

// labels drops what configures another platform's proxy or tools.
func (c *converter) labels(svc *yaml.Node) {
	labels := mapGet(svc, "labels")
	switch {
	case isMap(labels):
		for _, k := range mapKeys(labels) {
			if dropLabels.MatchString(k) {
				mapDel(labels, k)
			}
		}
	case isSeq(labels):
		kept := labels.Content[:0]
		for _, item := range labels.Content {
			if !dropLabels.MatchString(item.Value) {
				kept = append(kept, item)
			}
		}
		labels.Content = kept
	default:
		return
	}
	if len(labels.Content) == 0 {
		mapDel(svc, "labels")
	}
}

// deploy keeps the limits and drops what is for a swarm, or hands the
// container a device of the server (a GPU).
func (c *converter) deploy(svc *yaml.Node) {
	deploy := mapGet(svc, "deploy")
	if !isMap(deploy) {
		return
	}
	for _, k := range mapKeys(deploy) {
		if k != "resources" && k != "replicas" && k != "restart_policy" && k != "labels" {
			mapDel(deploy, k)
		}
	}
	// Several copies of one service is a swarm's doing; a stack runs one.
	mapDel(deploy, "replicas")
	if res := mapGet(deploy, "resources"); isMap(res) {
		if reserved := mapGet(res, "reservations"); isMap(reserved) {
			mapDel(reserved, "devices")
			mapDel(reserved, "generic_resources")
			if len(reserved.Content) == 0 {
				mapDel(res, "reservations")
			}
		}
		if len(res.Content) == 0 {
			mapDel(deploy, "resources")
		}
	}
	if len(deploy.Content) == 0 {
		mapDel(svc, "deploy")
	}
}

// mounts rewrites a service's volumes: a directory next to the Compose
// file becomes a named volume, a file with content a config.
func (c *converter) mounts(service string, svc *yaml.Node) error {
	list := mapGet(svc, "volumes")
	if !isSeq(list) {
		return nil
	}
	kept := list.Content[:0:0]
	for _, item := range list.Content {
		var source, target, mode string
		var content *yaml.Node
		directory := false
		long := isMap(item)
		switch {
		case long:
			kind := "volume"
			if k := mapGet(item, "type"); isStr(k) {
				kind = k.Value
			}
			if s := mapGet(item, "source"); isStr(s) {
				source = s.Value
			}
			if s := mapGet(item, "target"); isStr(s) {
				target = s.Value
			}
			content = mapDel(item, "content")
			directory = truthy(mapDel(item, "is_directory")) || truthy(mapDel(item, "isDirectory"))
			if truthy(mapGet(item, "read_only")) {
				mode = "ro"
			}
			if kind == "tmpfs" {
				kept = append(kept, item)
				continue
			}
			if kind == "volume" {
				if source != "" {
					c.volumes[source] = true
				}
				kept = append(kept, item)
				continue
			}
		case isStr(item):
			parts := strings.Split(item.Value, ":")
			// A variable's default can hold a colon: ${DATA:-./data}:/data.
			if strings.HasPrefix(item.Value, "${") {
				if end := strings.Index(item.Value, "}:"); end > 0 {
					parts = append([]string{item.Value[:end+1]}, strings.Split(item.Value[end+2:], ":")...)
				}
			}
			if len(parts) == 1 {
				kept = append(kept, item) // an anonymous volume
				continue
			}
			source, target = parts[0], parts[1]
			if len(parts) > 2 {
				mode = parts[2]
			}
		default:
			kept = append(kept, item)
			continue
		}

		if target == "/etc/localtime" || target == "/etc/timezone" {
			continue // the server's clock setting: the container keeps its own
		}
		switch {
		case strings.Contains(source, "docker.sock") || strings.Contains(source, "podman.sock") || strings.Contains(source, "/var/lib/docker"):
			return fmt.Errorf("it mounts the Docker socket or Docker's own files, which hands a container the server")
		case source == "/dev/urandom" || source == "/dev/random":
			continue // a container has the kernel's random numbers already
		case strings.HasPrefix(source, "~") || strings.HasPrefix(source, "/") && serverOwn.MatchString(source):
			return fmt.Errorf("it mounts a directory of the server (%s)", source)
		case strings.HasPrefix(source, "/"):
			// A directory the template picked for its data (/opt/app,
			// /srv/data): a volume of the stack holds the same, and a
			// second copy of the template does not share it.
			if fileNameRE.MatchString(target) {
				return fmt.Errorf("it mounts a file of the server (%s)", source)
			}
			name := slug(service + "-" + target)
			c.volumes[name] = true
			entry := name + ":" + target
			if mode != "" {
				entry += ":" + mode
			}
			kept = append(kept, str(entry))
			continue
		case strings.HasPrefix(source, "."), strings.HasPrefix(source, "$"), strings.Contains(source, "/"), content != nil:
			// Next to the Compose file, below.
		default:
			c.volumes[source] = true
			kept = append(kept, item)
			continue
		}

		rel := cleanRel(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(source, "../files/"), "./files/"), "./"))
		if strings.HasPrefix(source, "$") {
			rel = slug(target)
		}
		text, isFile := "", false
		if content != nil {
			// Coolify writes the file as it stands; Compose fills
			// variables into a config's content, so only the magic ones
			// may stay as references.
			text, isFile = escapeExceptMagic(content.Value), true
		} else if f, ok := c.t.files[rel]; ok {
			text, isFile = f, true
		}
		if isFile {
			c.addConfig(svc, service, rel, target, text)
			continue
		}
		// A directory that holds files of the blueprint: each is mounted
		// where it would have been.
		inside := false
		for _, p := range sortedKeys(c.t.files) {
			if strings.HasPrefix(p, rel+"/") {
				c.addConfig(svc, service, p, path.Join(target, strings.TrimPrefix(p, rel+"/")), c.t.files[p])
				inside = true
			}
		}
		if inside {
			continue
		}
		if !directory && fileNameRE.MatchString(target) {
			return fmt.Errorf("it mounts a file next to the Compose file that the template does not carry (%s)", source)
		}
		name := slug(service + "-" + rel)
		if rel == "" || rel == "." {
			name = slug(service + "-" + target)
		}
		c.volumes[name] = true
		entry := name + ":" + target
		if mode != "" {
			entry += ":" + mode
		}
		kept = append(kept, str(entry))
	}
	if len(kept) == 0 {
		mapDel(svc, "volumes")
		return nil
	}
	list.Content = kept
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var magicRefRE = regexp.MustCompile(`^\$\{?SERVICE_(FQDN|URL|HTTPS|USER|PASSWORD|BASE64|HEX)_[A-Z0-9_]*[A-Z0-9]\}?`)

// escapeExceptMagic doubles the dollar signs of a file's content, but for
// those of magic variables.
func escapeExceptMagic(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			if m := magicRefRE.FindString(s[i:]); m != "" && strings.HasPrefix(m, "${") == strings.HasSuffix(m, "}") {
				b.WriteString(m)
				i += len(m) - 1
				continue
			}
			b.WriteString("$$")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// addConfig puts a file with content into the stack as a config and mounts
// it in a service.
func (c *converter) addConfig(svc *yaml.Node, service, rel, target, content string) {
	name := slug(service + "-" + rel)
	// A file with nothing in it is what some templates start from (a list
	// of users to be filled in later). Compose reads an empty content as
	// no content and refuses the config, and only when the stack is
	// started: one line end is as near to empty as a config can be.
	if content == "" {
		content = "\n"
	}
	def := newMap()
	text := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: content, Style: yaml.LiteralStyle}
	if strings.TrimSpace(content) == "" {
		text.Style = yaml.DoubleQuotedStyle // a block of nothing is hard to read
	}
	def.Content = append(def.Content, str("content"), text)
	mapSet(c.configs, name, def)
	use := newMap()
	use.Content = append(use.Content, str("source"), str(name), str("target"), str(target))
	if strings.HasSuffix(rel, ".sh") || strings.HasPrefix(content, "#!") {
		// A script is run, and a config is otherwise only readable.
		use.Content = append(use.Content, str("mode"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "0555"})
	}
	list := mapGet(svc, "configs")
	if !isSeq(list) {
		list = newSeq()
		mapSet(svc, "configs", list)
	}
	list.Content = append(list.Content, use)
}

// declareVolumes lists every named volume at the top of the file, as
// Compose wants, each the stack's own.
func (c *converter) declareVolumes() {
	top := mapGet(c.t.Root, "volumes")
	if !isMap(top) {
		top = newMap()
	}
	for i := 1; i < len(top.Content); i += 2 {
		if def := top.Content[i]; isMap(def) {
			for _, k := range mapKeys(def) {
				if k != "labels" {
					mapDel(def, k)
				}
			}
			if len(def.Content) == 0 {
				top.Content[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
			}
		}
	}
	// One nobody mounts any more is not kept.
	for _, name := range mapKeys(top) {
		if !c.volumes[name] {
			mapDel(top, name)
		}
	}
	names := make([]string, 0, len(c.volumes))
	for name := range c.volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if mapGet(top, name) == nil {
			top.Content = append(top.Content, str(name), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"})
		}
	}
	mapDel(c.t.Root, "volumes")
	if len(top.Content) > 0 {
		mapSet(c.t.Root, "volumes", top)
	}
}

func isMagic(name string) bool {
	return magicRefRE.MatchString("$" + name)
}

// defaults gives every variable a person may set a value to fall back on.
// Coolify and Dokploy start a stack with a variable left empty; musdash's
// catalogue asks for every one that has no default, so a variable that was
// optional there must be optional here. Where the file gives a default in
// one place, that is the variable's default everywhere.
func (c *converter) defaults() {
	known := map[string]string{}
	values(c.t.Root, func(n *yaml.Node) {
		rewriteRefs(n.Value, func(r ref) (string, bool) {
			if _, seen := known[r.Name]; r.hasDefault() && !seen {
				known[r.Name] = r.Arg
			}
			return "", false
		})
	})
	values(c.t.Root, func(n *yaml.Node) {
		setValue(n, rewriteRefs(n.Value, func(r ref) (string, bool) {
			if r.Op != "" || isMagic(r.Name) {
				return "", false
			}
			return "${" + r.Name + ":-" + known[r.Name] + "}", true
		}))
	})
}

func normalName(service string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service))
}

var addressRE = regexp.MustCompile(`SERVICE_(FQDN|URL|HTTPS)_([A-Z0-9_]*[A-Z0-9])`)

// endpoints makes sure every address the file names says which service
// and which port it goes to, the way musdash reads it: an address belongs
// to the service it is named after, or to the one service that mentions it.
// Coolify reads more loosely (the address belongs to the service that names
// it with a port), so a name that only that rule can place is changed to
// the service's own.
func (c *converter) endpoints() error {
	// A Coolify template with no address variable and a port in its header
	// gets its domain from Coolify all the same, at that port. Here that
	// is said with a variable.
	if names := mapKeys(c.services); c.t.Source == "coolify" && c.t.Port > 0 && !c.published && !addressRE.MatchString(c.text()) {
		addEnv(mapGet(c.services, names[0]), "SERVICE_FQDN_"+normalName(names[0])+"_"+strconv.Itoa(c.t.Port), "")
	}
	for range 4 {
		again, err := c.placeEndpoints()
		if err != nil || !again {
			return err
		}
	}
	return fmt.Errorf("its addresses could not be given to its services")
}

func (c *converter) renameEndpoint(from, to string) {
	re := regexp.MustCompile(`(SERVICE_(?:FQDN|URL|HTTPS)_)` + regexp.QuoteMeta(from) + `((?:_[0-9]+)?)($|[^A-Z0-9_])`)
	scalars(c.t.Root, func(n *yaml.Node) {
		if strings.Contains(n.Value, "_"+from) {
			// Twice: two names side by side share the character between.
			setValue(n, re.ReplaceAllString(re.ReplaceAllString(n.Value, "${1}"+to+"${2}${3}"), "${1}"+to+"${2}${3}"))
		}
	})
}

func (c *converter) placeEndpoints() (again bool, err error) {
	type endpoint struct {
		port     int
		mentions map[string]bool // the services that name it
		ported   map[string]bool // those that name it with its port
		declared map[string]bool // those that list it alone in their environment
	}
	found := map[string]*endpoint{}
	for _, service := range mapKeys(c.services) {
		scalars(mapGet(c.services, service), func(n *yaml.Node) {
			for _, m := range addressRE.FindAllStringSubmatch(n.Value, -1) {
				id, port := m[2], 0
				if i := strings.LastIndexByte(id, '_'); i > 0 {
					if p, perr := strconv.Atoi(id[i+1:]); perr == nil && p >= 1 && p <= 65535 {
						id, port = id[:i], p
					}
				}
				e := found[id]
				if e == nil {
					e = &endpoint{mentions: map[string]bool{}, ported: map[string]bool{}, declared: map[string]bool{}}
					found[id] = e
				}
				if declares(n, m[0]) {
					e.declared[service] = true
				}
				e.mentions[service] = true
				if port > 0 {
					e.ported[service] = true
					if e.port > 0 && e.port != port && err == nil {
						err = fmt.Errorf("its address %s names two ports, %d and %d", id, e.port, port)
					}
					e.port = port
				}
			}
		})
	}
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bare := 0
	for _, id := range ids {
		if found[id].port == 0 {
			bare++
		}
	}
	owners := map[string]string{}
	for _, id := range ids {
		e := found[id]
		owner := ""
		for _, service := range mapKeys(c.services) {
			if normalName(service) == id {
				owner = service
			}
		}
		if owner == "" && len(e.mentions) == 1 {
			for service := range e.mentions {
				owner = service
			}
		}
		// Coolify gives an address to the service that lists its variable
		// alone in its environment; the others only read it. Without such
		// a line, to the one that names it with a port.
		placed := e.declared
		if len(placed) != 1 {
			placed = e.ported
		}
		if owner == "" && len(placed) == 1 {
			for service := range placed {
				owner = service
			}
			// Only Coolify's rule places it: it takes the service's name.
			if _, taken := found[normalName(owner)]; taken {
				return false, fmt.Errorf("its address %s is of the service %s, which has another address under its own name", id, owner)
			}
			c.renameEndpoint(id, normalName(owner))
			return true, nil
		}
		if owner == "" {
			return false, fmt.Errorf("its address %s does not say which service it is for", id)
		}
		owners[id] = owner
		if e.port > 0 {
			continue
		}
		// The header's port is the template's own word for where its one
		// address goes. What the service published first may be something
		// else (SSH before the web port).
		// Where a service's port is written down by hand, that is it.
		port := servicePorts[c.t.Key][owner]
		if port == 0 && bare == 1 {
			port = c.t.Port
			// Unless another address names that port already: then the
			// header spoke of that one, and of this one nothing is known.
			for _, other := range ids {
				if other != id && found[other].port == port && c.plainOwner(other, found[other].mentions) != owner {
					port = 0
				}
			}
		}
		if port == 0 {
			port = c.ports[owner]
		}
		if expose := mapGet(mapGet(c.services, owner), "expose"); port == 0 && isSeq(expose) && len(expose.Content) > 0 {
			port, _ = strconv.Atoi(expose.Content[0].Value)
		}
		if port == 0 {
			return false, fmt.Errorf("its address %s does not say which port it goes to", id)
		}
		addEnv(mapGet(c.services, owner), "SERVICE_FQDN_"+id+"_"+strconv.Itoa(port), "")
		e.port = port
	}
	// Two names for one service and port are one address.
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if owners[a] != owners[b] || found[a].port != found[b].port {
				continue
			}
			keep, drop := a, b
			if normalName(owners[b]) == b {
				keep, drop = b, a
			}
			c.renameEndpoint(drop, keep)
			return true, nil
		}
	}
	return false, nil
}

var oneShotName = regexp.MustCompile(`(^|[-_])(init|initializer|migrat[a-z]*|setup|seed[a-z]*|bootstrap|create[a-z]*|provision[a-z]*|job|install[a-z]*|prepare|permissions?|chown|fix-perms|mc)([-_]|$)`)

// oneShots deals with the services that do a job and end. `docker compose
// up --wait`, which is how musdash starts a stack, fails for a container
// that exits, also with 0, unless another service waits for it to complete.
// And a service with no restart policy gets musdash's, which would run the
// job again for ever.
func (c *converter) oneShots() error {
	names := mapKeys(c.services)
	// dependsOn[a][b] is the condition a waits for b with.
	dependsOn := map[string]map[string]string{}
	for _, name := range names {
		dependsOn[name] = map[string]string{}
		deps := mapGet(mapGet(c.services, name), "depends_on")
		if isSeq(deps) {
			for _, d := range deps.Content {
				dependsOn[name][d.Value] = "service_started"
			}
		} else if isMap(deps) {
			for _, d := range mapKeys(deps) {
				cond := "service_started"
				if v := mapGet(mapGet(deps, d), "condition"); isStr(v) {
					cond = v.Value
				}
				dependsOn[name][d] = cond
			}
		}
	}
	awaited := map[string]bool{}
	for _, deps := range dependsOn {
		for d, cond := range deps {
			if cond == "service_completed_successfully" {
				awaited[d] = true
			}
		}
	}
	var reaches func(from, to string, seen map[string]bool) bool
	reaches = func(from, to string, seen map[string]bool) bool {
		if from == to {
			return true
		}
		if seen[from] {
			return false
		}
		seen[from] = true
		for d := range dependsOn[from] {
			if reaches(d, to, seen) {
				return true
			}
		}
		return false
	}
	for _, name := range names {
		svc := mapGet(c.services, name)
		restart := ""
		if r := mapGet(svc, "restart"); isStr(r) {
			restart = r.Value
		}
		ends := restart == "no" || strings.HasPrefix(restart, "on-failure")
		if awaited[name] {
			// Something waits for its end already. It must not be started
			// again once it has ended.
			if restart == "" {
				mapSet(svc, "restart", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "no", Style: yaml.DoubleQuotedStyle})
			}
			continue
		}
		hinted := c.t.notWaited[name] || oneShotName.MatchString(strings.ToLower(name))
		if !ends || !hinted {
			continue
		}
		// Nothing waits for it: from now on a service that does not
		// depend on it the other way round does. The one with the web
		// address first: it is what the job prepares things for, where a
		// database listed before it may be what the job itself needs.
		candidates := make([]string, 0, len(names))
		for _, other := range names {
			if c.addressed(other) {
				candidates = append(candidates, other)
			}
		}
		for _, other := range names {
			if !c.addressed(other) {
				candidates = append(candidates, other)
			}
		}
		waiter := ""
		for _, other := range candidates {
			if other == name || awaited[other] || reaches(name, other, map[string]bool{}) {
				continue
			}
			if r := mapGet(mapGet(c.services, other), "restart"); isStr(r) && r.Value == "no" {
				continue
			}
			waiter = other
			break
		}
		if waiter == "" {
			return fmt.Errorf("its service %s ends by itself and nothing can wait for it, which starting a stack reads as a failure", name)
		}
		if os.Getenv("CATALOG_NOTES") != "" {
			fmt.Fprintf(os.Stderr, "%s: %s now waits for %s to complete\n", c.t.Key, waiter, name)
		}
		c.await(waiter, name)
		dependsOn[waiter][name] = "service_completed_successfully"
		awaited[name] = true
	}
	return nil
}

// addressed reports whether a service names a web address.
func (c *converter) addressed(service string) bool {
	found := false
	scalars(mapGet(c.services, service), func(n *yaml.Node) {
		found = found || addressRE.MatchString(n.Value)
	})
	return found
}

// await makes one service wait for another to complete.
func (c *converter) await(waiter, job string) {
	svc := mapGet(c.services, waiter)
	deps := mapGet(svc, "depends_on")
	if isSeq(deps) {
		// The short form has no conditions: written out, with what it
		// meant for the others.
		long := newMap()
		for _, d := range deps.Content {
			cond := newMap()
			cond.Content = append(cond.Content, str("condition"), str("service_started"))
			long.Content = append(long.Content, str(d.Value), cond)
		}
		deps = long
		mapSet(svc, "depends_on", deps)
	}
	if !isMap(deps) {
		deps = newMap()
		mapSet(svc, "depends_on", deps)
	}
	cond := newMap()
	cond.Content = append(cond.Content, str("condition"), str("service_completed_successfully"))
	mapSet(deps, job, cond)
}

var addressRE2 = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']*`)

// appLogin is a variable that sets the password a person signs in to the
// app with. Written out, it is the same on every install and on the
// internet. Its other end is that person, so the template asks for it: the
// form of a catalogue template has a field for every variable written
// "${NAME:?…}". (Not generated: an app may have rules for a password that a
// generated one does not meet, and the person has to know it anyway.)
var appLogin = regexp.MustCompile(`(?i)(ADMIN|DASHBOARD|BASIC_?AUTH|LOGIN|INITIAL|DEFAULT|WEBUI|PANEL|SUPERUSER|OWNER)`)

const askPassword = ":?Set a password to sign in with"

// withDefault is a variable whose default is all of a value:
// "${ADMIN_PASSWORD:-changeme}".
var withDefault = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*:?-([^${}]*)\}$`)

var passwordKey = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|_PASS|^PASS)$`)

func boolish(v string) bool {
	switch strings.ToLower(v) {
	case "", "true", "false", "yes", "no", "on", "off", "0", "1", "null", "none":
		return true
	}
	return false
}

// passwords replaces a password that is written out in the file by a
// generated one, where the file itself shows both ends of it: the same text
// as the database's password and as the app's, or in a connection address.
// Every place is rewritten, or none.
//
// A password that stands in one place only is left as its makers wrote it.
// Its other end is not in the file (the image's own default, as with an app
// that connects as postgres:postgres unless told otherwise), and a
// generated value on one side would lock the app out of its database. So is
// one that also stands where this cannot tell it from another word. Such a
// password is reachable only on the stack's own network.
//
// The exception is the app's own sign-in (appLogin): that one is generated
// from a single place too.
func (c *converter) passwords() {
	type place struct {
		node *yaml.Node
		key  string // "KEY=" in front of the value, for an entry of a list
	}
	places := map[string][]place{} // what was written → where it stands by itself
	keys := map[string]string{}    // what was written → the first variable that held it
	var order []string
	note := func(key, value string, p place) {
		value = strings.Trim(value, `"'`)
		// For the app's own sign-in, a default is what the password is
		// until somebody sets another. For anything else a variable is the
		// person's to fill in (a mail server's password), default or not.
		alone := slices.Contains(generatedAlone[c.t.Key], key)
		if m := withDefault.FindStringSubmatch(value); m != nil && (appLogin.MatchString(key) || alone) {
			value = m[1]
		}
		if !passwordKey.MatchString(key) || boolish(value) || strings.Contains(value, "$") || len(value) < 3 {
			return
		}
		if _, ok := places[value]; !ok {
			order = append(order, value)
			keys[value] = key
		}
		places[value] = append(places[value], p)
	}
	for _, service := range mapKeys(c.services) {
		environment := mapGet(mapGet(c.services, service), "environment")
		if isMap(environment) {
			for i := 0; i+1 < len(environment.Content); i += 2 {
				if v := environment.Content[i+1]; isStr(v) {
					note(environment.Content[i].Value, v.Value, place{node: v})
				}
			}
		} else if isSeq(environment) {
			for _, item := range environment.Content {
				if k, v, ok := strings.Cut(item.Value, "="); ok {
					note(k, v, place{node: item, key: k + "="})
				}
			}
		}
	}
	if len(order) == 0 {
		return
	}
	// Longest first, so that one password inside another is not cut.
	sort.SliceStable(order, func(i, j int) bool { return len(order[i]) > len(order[j]) })
	word := func(literal string) *regexp.Regexp {
		return regexp.MustCompile(`(^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(literal) + `($|[^A-Za-z0-9_])`)
	}
	// The places a password stands inside something else and can be told:
	// after the user in an address, and as the argument of an option that
	// takes one.
	inside := func(literal string) []*regexp.Regexp {
		q := regexp.QuoteMeta(literal)
		return []*regexp.Regexp{
			regexp.MustCompile(`(://[^:/@\s]*:)` + q + `(@)`),
			regexp.MustCompile(`(--requirepass[ =]|--password[ =]|--pass[ =]|PGPASSWORD=|--masterauth[ =])` + q + `($|[\s"'])`),
		}
	}
	// And in a file the template carries: quoted, as what a line says the
	// password is (pwd: "…" in the script that makes a database's user).
	inFile := func(literal string) *regexp.Regexp {
		return regexp.MustCompile(`((?i:pwd|password|passwd|pass)["']?\s*[:=]\s*["'])` + regexp.QuoteMeta(literal) + `(["'])`)
	}
	taken := map[string]bool{}
	scalars(c.t.Root, func(n *yaml.Node) {
		for _, m := range regexp.MustCompile(`SERVICE_PASSWORD_[A-Z0-9_]*[A-Z0-9]`).FindAllString(n.Value, -1) {
			taken[m] = true
		}
	})
	for _, literal := range order {
		uses := len(places[literal])
		values(c.services, func(n *yaml.Node) {
			for _, re := range inside(literal) {
				uses += len(re.FindAllStringIndex(n.Value, -1))
			}
		})
		values(c.configs, func(n *yaml.Node) {
			uses += len(inFile(literal).FindAllStringIndex(n.Value, -1))
		})
		login := appLogin.MatchString(keys[literal])
		// One place, and not a sign-in of the app's own: left alone,
		// unless it is known by hand that nothing else holds it.
		if uses < 2 && !login && !slices.Contains(generatedAlone[c.t.Key], keys[literal]) {
			continue
		}
		base := regexp.MustCompile(`(?i)_*(PASSWORD|PASSWD|PASS)$`).ReplaceAllString(keys[literal], "")
		name := "SERVICE_PASSWORD_" + magicID(base)
		if base == "" {
			name = "SERVICE_PASSWORD_APP"
		}
		for n := 2; taken[name]; n++ {
			name = strings.TrimRight(name, "0123456789") + strconv.Itoa(n)
		}
		taken[name] = true
		reference := "${" + name + "}"
		if login {
			reference = "${" + magicID(keys[literal]) + askPassword + "}"
		}
		// What cannot be placed is left as its makers wrote it, all of it:
		// see above. So everything is looked at before anything is changed.
		placed := true
		for _, file := range sortedKeys(c.t.files) {
			placed = placed && !word(literal).MatchString(inFile(literal).ReplaceAllString(c.t.files[file], "${1}${2}"))
		}
		scalars(c.configs, func(n *yaml.Node) {
			placed = placed && !word(literal).MatchString(inFile(literal).ReplaceAllString(n.Value, "${1}${2}"))
		})
		rewrite := func(apply bool) {
			for _, service := range mapKeys(c.services) {
				svc := mapGet(c.services, service)
				for _, key := range mapKeys(svc) {
					values(mapGet(svc, key), func(n *yaml.Node) {
						if !strings.Contains(n.Value, literal) {
							return
						}
						text := n.Value
						for _, re := range inside(literal) {
							text = re.ReplaceAllString(text, "${1}"+strings.ReplaceAll(reference, "$", "$$")+"${2}")
						}
						if apply {
							setValue(n, text)
							return
						}
						for _, p := range places[literal] {
							if p.node == n {
								return // the password by itself
							}
						}
						// In an address the password has one place, which
						// was just dealt with: the scheme, the user, the
						// host and the path are not it.
						if !word(literal).MatchString(addressRE2.ReplaceAllString(text, " ")) {
							return
						}
						// What is left of it. By itself as another
						// variable's whole value it is a name that happens
						// to be the same word (a user "postgres" beside a
						// password "postgres"), and so it is in an image's
						// name or a volume's. Inside a longer value, or in a
						// command, it could be the password.
						whole := strings.Trim(text, `"'`) == literal || strings.HasSuffix(text, "="+literal)
						if key == "environment" && !whole || key == "command" || key == "entrypoint" || key == "healthcheck" {
							placed = false
						}
					})
				}
			}
		}
		rewrite(false)
		if !placed {
			taken[name] = false
			continue
		}
		rewrite(true)
		// Compose fills variables in a file's content too.
		values(c.configs, func(n *yaml.Node) {
			if strings.Contains(n.Value, literal) {
				setValue(n, inFile(literal).ReplaceAllString(n.Value, "${1}"+strings.ReplaceAll(reference, "$", "$$")+"${2}"))
			}
		})
		for _, p := range places[literal] {
			setValue(p.node, p.key+reference)
			p.node.Style = 0
		}
	}
}

// declares reports whether a scalar is a line of an environment that names
// the variable and nothing else: "- SERVICE_URL_API", which is how a
// Coolify template says whose address it is.
func declares(n *yaml.Node, variable string) bool {
	return n.Value == variable
}

// plainOwner is the service an address belongs to by musdash's own
// reading: the one it is named after, or the only one that mentions it.
func (c *converter) plainOwner(id string, mentions map[string]bool) string {
	for _, service := range mapKeys(c.services) {
		if normalName(service) == id {
			return service
		}
	}
	if len(mentions) == 1 {
		for service := range mentions {
			return service
		}
	}
	return ""
}
