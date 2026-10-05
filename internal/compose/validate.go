package compose

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
)

// A Compose file can ask for everything `docker run` can: a privileged
// container, the host's network, any directory of the server, the Docker
// socket. Validate holds a stack to the limits musdash sets for an app.
//
// It works from an allow-list. Compose keeps gaining keys, and some of them
// act on the server (one runs a program there), so a key this code does not
// know is refused rather than passed on.

// ValidateOptions says what a stack may refer to.
type ValidateOptions struct {
	// Dir is the stack's own directory on the server. It holds what musdash
	// itself starts the stack from, so nothing in it may be mounted: a
	// container that could write there could replace the checked file with
	// one that was never checked.
	Dir string
	// BuildDir, when set, is a checkout that services may be built from
	// and may mount files of. Empty refuses "build".
	BuildDir string
	// Protected are directories no container may mount: musdash's data.
	Protected []string
	// ValidPort reports whether a host port may be published.
	ValidPort func(port int) bool
}

// serviceKeys are the service options a stack may use. A value of nil means
// any value; otherwise the function checks it.
var serviceKeys = map[string]func(c *checker, v any){
	"image": nil, "command": nil, "entrypoint": nil, "environment": nil, "depends_on": nil,
	"healthcheck": nil, "restart": nil, "expose": nil, "hostname": nil, "domainname": nil,
	"user": nil, "working_dir": nil, "stop_signal": nil, "stop_grace_period": nil, "init": nil,
	"read_only": nil, "tmpfs": nil, "shm_size": nil, "ulimits": nil, "extra_hosts": nil,
	"dns": nil, "dns_search": nil, "dns_opt": nil, "mem_limit": nil, "mem_reservation": nil,
	"cpus": nil, "pids_limit": nil, "cap_drop": nil, "profiles": nil, "platform": nil,
	"tty": nil, "stdin_open": nil, "attach": nil, "secrets": nil, "configs": nil,
	"annotations": nil,

	"networks":     (*checker).serviceNetworks,
	"labels":       (*checker).labels,
	"volumes":      (*checker).mounts,
	"ports":        (*checker).ports,
	"cap_add":      (*checker).capAdd,
	"security_opt": (*checker).securityOpt,
	"sysctls":      (*checker).sysctls,
	"logging":      (*checker).logging,
	"deploy":       (*checker).deploy,
	"scale":        (*checker).one,
	"network_mode": (*checker).networkMode,
	"pull_policy":  (*checker).pullPolicy,
	"build":        (*checker).build,
	// Present in the normalised form even when unset.
	"privileged": (*checker).mustBeFalse,
}

// refusals explains the options that are refused for a reason worth
// stating. Any other unknown option gets a general message.
var refusals = map[string]string{
	"container_name":      "container names are assigned by musdash, so two stacks cannot claim the same one",
	"devices":             "it gives the container a device of the server",
	"device_cgroup_rules": "it gives the container devices of the server",
	"volumes_from":        "it mounts another container's data",
	"pid":                 "it shares the server's processes with the container",
	"ipc":                 "it shares memory with other containers or the server",
	"uts":                 "it shares the server's host name settings",
	"userns_mode":         "it turns off user namespace isolation",
	"cgroup":              "it shares the server's control groups",
	"cgroup_parent":       "it moves the container out of Docker's resource accounting",
	"runtime":             "it replaces the container runtime",
	"provider":            "it runs a program on the server itself",
	"use_api_socket":      "it hands the container control of Docker",
	"env_file":            "variables belong in the stack's Variables, not in a file on the server",
	"extends":             "the file must be self-contained",
	"links":               "services of one stack already reach each other by name",
	"external_links":      "it reaches into containers outside the stack",
	"oom_kill_disable":    "it exempts the container from the server's memory protection",
	"oom_score_adj":       "it changes which process the server stops when memory runs out",
	"memswap_limit":       "it changes how much swap the container may use",
	"cpu_shares":          "it changes the container's share of the processor against everything else on the server",
	"post_start":          "lifecycle hooks are not supported",
	"pre_stop":            "lifecycle hooks are not supported",
	"develop":             "it is for local development",
	"gpus":                "it gives the container devices of the server",
	"storage_opt":         "it changes Docker's storage limits",
	"mac_address":         "addresses are assigned by Docker",
	"group_add":           "it adds the container's user to groups of the server",
}

type checker struct {
	opt     ValidateOptions
	project string
	volumes map[string]bool
	service string // the service being checked
	key     string // the option being checked
	errs    []string
}

func (c *checker) fail(format string, args ...any) {
	where := ""
	if c.service != "" {
		where = "service " + c.service + ": "
	}
	c.errs = append(c.errs, where+fmt.Sprintf(format, args...))
}

// Validate reports everything in a normalised Compose document that a stack
// may not do. The error lists each problem on its own line.
func (p Project) Validate(opt ValidateOptions) error {
	c := &checker{opt: opt, project: p.Name(), volumes: map[string]bool{}}
	if c.project == "" {
		return fmt.Errorf("the Compose configuration has no project name")
	}
	for _, key := range sortedKeys(p.doc) {
		switch {
		case key == "name" || key == "services" || strings.HasPrefix(key, "x-"):
		case key == "networks":
			c.networks(asMap(p.doc[key]))
		case key == "volumes":
			c.topVolumes(asMap(p.doc[key]))
		case key == "secrets" || key == "configs":
			c.files(key, asMap(p.doc[key]))
		default:
			c.fail("the top-level key %q is not supported", key)
		}
	}
	services := asMap(p.doc["services"])
	if len(services) == 0 {
		c.fail("the file defines no services")
	}
	for _, name := range sortedKeys(services) {
		c.service = name
		if !docker.ValidName(name) {
			c.fail("the name is not a valid service name")
			continue
		}
		svc := asMap(services[name])
		if svc["image"] == nil && svc["build"] == nil {
			c.fail("it has no image")
		}
		for _, key := range sortedKeys(svc) {
			value := svc[key]
			if value == nil {
				continue // an option the normalised form lists but leaves unset
			}
			check, known := serviceKeys[key]
			switch {
			case !known:
				if why, ok := refusals[key]; ok {
					c.fail("%q is not allowed: %s", key, why)
				} else {
					c.fail("the option %q is not supported", key)
				}
			case check != nil:
				c.key = key
				check(c, value)
			}
		}
	}
	c.service = ""
	if len(c.errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(c.errs, "\n"))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// onlyKeys fails for any key of m outside the allowed ones.
func (c *checker) onlyKeys(what string, m map[string]any, allowed ...string) {
	for _, key := range sortedKeys(m) {
		ok := m[key] == nil
		for _, a := range allowed {
			ok = ok || key == a
		}
		if !ok {
			c.fail("%s: the option %q is not supported", what, key)
		}
	}
}

// inside reports whether p is dir or lies below it.
func inside(p, dir string) bool {
	if dir == "" || !path.IsAbs(p) {
		return false
	}
	p, dir = path.Clean(p), path.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func (c *checker) networks(networks map[string]any) {
	for _, key := range sortedKeys(networks) {
		n := asMap(networks[key])
		what := "network " + key
		c.onlyKeys(what, n, "name", "driver", "ipam", "internal", "enable_ipv4", "enable_ipv6", "labels")
		// Another name would join a network that belongs to something else:
		// another stack's, or an environment's.
		if name, _ := n["name"].(string); name != c.project+"_"+key {
			c.fail("%s: a stack's networks are its own; \"name\" and \"external\" are not allowed", what)
		}
		if driver, _ := n["driver"].(string); driver != "" && driver != "bridge" {
			c.fail("%s: only the bridge driver is allowed, not %q", what, driver)
		}
		// A chosen subnet becomes a route of the server. One that covers
		// addresses outside Docker would send the server's own traffic to
		// them into the stack.
		if ipam := asMap(n["ipam"]); len(ipam) > 0 {
			c.fail("%s: addresses are assigned by Docker; \"ipam\" is not allowed", what)
		}
	}
}

// serviceNetworks checks how a service joins the stack's networks.
func (c *checker) serviceNetworks(v any) {
	networks := asMap(v)
	for _, key := range sortedKeys(networks) {
		c.onlyKeys("network "+key, asMap(networks[key]), "aliases", "priority")
	}
}

func (c *checker) topVolumes(volumes map[string]any) {
	for _, key := range sortedKeys(volumes) {
		c.volumes[key] = true
		v := asMap(volumes[key])
		what := "volume " + key
		// driver_opts can turn a volume into a mount of any directory of
		// the server.
		c.onlyKeys(what, v, "name", "driver", "labels")
		if name, _ := v["name"].(string); name != c.project+"_"+key {
			c.fail("%s: a stack's volumes are its own; \"name\" and \"external\" are not allowed", what)
		}
		if driver, _ := v["driver"].(string); driver != "" && driver != "local" {
			c.fail("%s: only the local driver is allowed, not %q", what, driver)
		}
	}
}

// files checks top-level secrets and configs: what they contain must come
// from the file itself or from the checkout the stack is built from.
func (c *checker) files(kind string, entries map[string]any) {
	for _, key := range sortedKeys(entries) {
		e := asMap(entries[key])
		what := strings.TrimSuffix(kind, "s") + " " + key
		c.onlyKeys(what, e, "name", "file", "content")
		if file, ok := e["file"].(string); ok && !c.own(file) {
			c.fail("%s: it may not be read from a file on the server; write it under \"content\" instead", what)
		}
	}
}

// own reports whether a path belongs to the stack: it lies in the checkout
// the stack is built from. The stack's directory on the server does not
// count; see ValidateOptions.Dir.
func (c *checker) own(p string) bool {
	return inside(p, c.opt.BuildDir)
}

func (c *checker) mustBeFalse(v any) {
	if b, ok := v.(bool); !ok || b {
		c.fail("%q is not allowed: it gives the container the run of the server", c.key)
	}
}

func (c *checker) one(v any) {
	if toInt(v) != 1 {
		c.fail("%q must be 1: a stack runs one container per service", c.key)
	}
}

func (c *checker) labels(v any) {
	for key := range asMap(v) {
		if strings.HasPrefix(strings.ToLower(key), "musdash.") {
			c.fail("the label %q is not allowed: labels starting with \"musdash.\" are how musdash tells its containers apart", key)
		}
	}
}

func (c *checker) mounts(v any) {
	for _, entry := range asList(v) {
		m := asMap(entry)
		kind, _ := m["type"].(string)
		source, _ := m["source"].(string)
		target, _ := m["target"].(string)
		switch kind {
		case "volume":
			if source != "" && !c.volumes[source] {
				c.fail("the volume %q mounted at %s is not declared under \"volumes\"", source, target)
			}
		case "tmpfs":
		case "bind":
			if prop, _ := asMap(m["bind"])["propagation"].(string); prop != "" && prop != "rprivate" && prop != "private" {
				c.fail("the mount at %s may not use %q propagation", target, prop)
			}
			if c.own(source) {
				continue
			}
			if inside(source, c.opt.Dir) {
				c.fail("the mount at %s is a directory next to the Compose file, which a pasted stack does not have; use a named volume, or the full path of a directory on the server", target)
				continue
			}
			if err := docker.CheckBindSource(source, c.opt.Protected...); err != nil {
				c.fail("%v", err)
			}
		default:
			c.fail("the mount at %s has the type %q, which is not supported", target, kind)
		}
	}
}

func (c *checker) ports(v any) {
	for _, entry := range asList(v) {
		p := asMap(entry)
		published := fmt.Sprint(p["published"])
		if p["published"] == nil || published == "" {
			c.fail("the port %d is published without a fixed port of the server; name one, or give the service a domain with SERVICE_FQDN_<NAME>_%d instead", toInt(p["target"]), toInt(p["target"]))
			continue
		}
		port := toInt(p["published"])
		if port == 0 || c.opt.ValidPort == nil || !c.opt.ValidPort(port) {
			c.fail("the port %s of the server may not be published; use one from 1024 to 65535, outside 20000 to 29999", published)
		}
	}
}

func (c *checker) capAdd(v any) {
	for _, entry := range asList(v) {
		if name, _ := entry.(string); !docker.SafeCapability(name) {
			c.fail("the capability %v is not allowed", entry)
		}
	}
}

func (c *checker) securityOpt(v any) {
	for _, entry := range asList(v) {
		if opt, _ := entry.(string); opt != "no-new-privileges" && opt != "no-new-privileges:true" {
			c.fail("the security option %v is not allowed", entry)
		}
	}
}

func (c *checker) sysctls(v any) {
	names := sortedKeys(asMap(v))
	for _, entry := range asList(v) {
		name, _, _ := strings.Cut(fmt.Sprint(entry), "=")
		names = append(names, name)
	}
	for _, name := range names {
		// Only the network settings are the container's own; the rest are
		// the server's.
		if !strings.HasPrefix(name, "net.") {
			c.fail("the sysctl %q is not allowed", name)
		}
	}
}

func (c *checker) logging(v any) {
	switch driver, _ := asMap(v)["driver"].(string); driver {
	case "", "json-file", "local", "none":
	default:
		c.fail("the logging driver %q is not allowed: it would send the container's output off the server and leave the Logs page empty", driver)
	}
}

func (c *checker) deploy(v any) {
	d := asMap(v)
	c.onlyKeys("deploy", d, "resources", "replicas", "restart_policy", "labels")
	if d["replicas"] != nil && toInt(d["replicas"]) != 1 {
		c.fail("\"deploy.replicas\" must be 1: a stack runs one container per service")
	}
	c.labels(d["labels"])
	res := asMap(d["resources"])
	c.onlyKeys("deploy.resources", res, "limits", "reservations")
	for _, part := range []string{"limits", "reservations"} {
		c.onlyKeys("deploy.resources."+part, asMap(res[part]), "cpus", "memory", "pids")
	}
}

func (c *checker) networkMode(v any) {
	mode, _ := v.(string)
	if mode == "" || mode == "none" || mode == "bridge" {
		return
	}
	// "service:<name>" shares a sibling's network, which stays in the stack.
	if _, ok := strings.CutPrefix(mode, "service:"); ok {
		return
	}
	c.fail("the network mode %q is not allowed", mode)
}

func (c *checker) pullPolicy(v any) {
	if policy, _ := v.(string); policy == "build" && c.opt.BuildDir == "" {
		c.fail("\"pull_policy: build\" needs a stack that comes from a Git repository")
	}
}

func (c *checker) build(v any) {
	if c.opt.BuildDir == "" {
		c.fail("\"build\" is not allowed: a pasted Compose file has no source to build from. Use an image, or create the stack from a Git repository")
		return
	}
	b := asMap(v)
	// Left out on purpose: additional_contexts, secrets and ssh read from
	// the server; network could put the build on the server's own network;
	// tags could replace an image something else runs from.
	c.onlyKeys("build", b, "context", "dockerfile", "dockerfile_inline", "args", "target", "labels", "no_cache", "pull", "shm_size", "extra_hosts", "platforms")
	context, _ := b["context"].(string)
	if !inside(context, c.opt.BuildDir) {
		c.fail("the build context %q is outside the repository", context)
		return
	}
	if dockerfile, ok := b["dockerfile"].(string); ok && dockerfile != "" {
		if !path.IsAbs(dockerfile) {
			dockerfile = path.Join(context, dockerfile)
		}
		if !inside(dockerfile, c.opt.BuildDir) {
			c.fail("the Dockerfile %q is outside the repository", dockerfile)
		}
	}
	c.labels(b["labels"])
}
