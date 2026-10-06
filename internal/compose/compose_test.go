package compose

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
)

const (
	testDir     = "/var/lib/musdash/apps/svc1"
	testProject = "musdash-svc1"
)

func testOptions() ValidateOptions {
	return ValidateOptions{
		Dir:       testDir,
		Protected: []string{"/var/lib/musdash"},
		ValidPort: func(p int) bool { return p >= 1024 && p <= 65535 && (p < 20000 || p > 29999) },
	}
}

// allowed is a normalised document that uses what a stack may use.
const allowed = `{
  "name": "musdash-svc1",
  "x-note": "extension fields are ignored",
  "networks": {
    "default": {"name": "musdash-svc1_default", "ipam": {}},
    "back": {"name": "musdash-svc1_back", "driver": "bridge", "internal": true}
  },
  "volumes": {
    "data": {"name": "musdash-svc1_data"},
    "cache": {"name": "musdash-svc1_cache", "driver": "local", "labels": {"a": "b"}}
  },
  "configs": {"site": {"name": "musdash-svc1_site", "content": "server {}"}},
  "secrets": {"key": {"name": "musdash-svc1_key", "content": "s3cret"}},
  "services": {
    "web": {
      "image": "nginx:alpine",
      "command": null,
      "entrypoint": null,
      "privileged": false,
      "environment": {"A": "1"},
      "labels": {"com.example.role": "web", "traefik.enable": "true"},
      "depends_on": {"db": {"condition": "service_healthy", "required": true}},
      "healthcheck": {"test": ["CMD", "true"]},
      "restart": "always",
      "expose": ["80"],
      "user": "1000:1000",
      "working_dir": "/app",
      "init": true,
      "read_only": true,
      "tmpfs": ["/tmp"],
      "shm_size": "67108864",
      "ulimits": {"nofile": 65536},
      "extra_hosts": ["host.docker.internal=host-gateway"],
      "dns": ["1.1.1.1"],
      "mem_limit": "536870912",
      "cpus": 0.5,
      "pids_limit": 200,
      "cap_drop": ["ALL"],
      "cap_add": ["NET_BIND_SERVICE", "CAP_CHOWN"],
      "security_opt": ["no-new-privileges:true"],
      "sysctls": {"net.core.somaxconn": "1024"},
      "logging": {"driver": "json-file", "options": {"max-size": "10m"}},
      "deploy": {"replicas": 1, "resources": {"limits": {"cpus": 0.5, "memory": "536870912"}}},
      "network_mode": "",
      "networks": {"default": null, "back": {"aliases": ["site"]}},
      "configs": [{"source": "site", "target": "/etc/nginx/conf.d/default.conf"}],
      "secrets": [{"source": "key"}],
      "ports": [{"mode": "ingress", "target": 25, "published": "2525", "protocol": "tcp"}],
      "volumes": [
        {"type": "volume", "source": "data", "target": "/data", "volume": {}},
        {"type": "volume", "target": "/anonymous", "volume": {}},
        {"type": "tmpfs", "target": "/run"},
        {"type": "bind", "source": "/srv/site/conf", "target": "/conf", "bind": {"create_host_path": true}},
        {"type": "bind", "source": "/srv/shared", "target": "/shared", "read_only": true, "bind": {}}
      ]
    },
    "db": {
      "image": "postgres:17-alpine",
      "network_mode": "service:web"
    }
  }
}`

func parse(t *testing.T, raw string) Project {
	t.Helper()
	p, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateAllows(t *testing.T) {
	if err := parse(t, allowed).Validate(testOptions()); err != nil {
		t.Fatalf("a stack within the limits was refused:\n%v", err)
	}
}

// with returns the allowed document with one value of the "web" service
// replaced, or with a top-level section replaced when key has a "/" prefix.
func with(t *testing.T, key, value string) Project {
	t.Helper()
	var doc map[string]any
	json.Unmarshal([]byte(allowed), &doc)
	var v any
	if err := json.Unmarshal([]byte(value), &v); err != nil {
		t.Fatalf("%s: %v", value, err)
	}
	if top, ok := strings.CutPrefix(key, "/"); ok {
		doc[top] = v
	} else {
		doc["services"].(map[string]any)["web"].(map[string]any)[key] = v
	}
	raw, _ := json.Marshal(doc)
	return parse(t, string(raw))
}

func TestValidateRefuses(t *testing.T) {
	cases := []struct{ key, value, want string }{
		// The run of the server.
		{"privileged", `true`, `"privileged" is not allowed`},
		{"network_mode", `"host"`, `network mode "host"`},
		{"network_mode", `"container:musdash-db-x"`, `network mode "container:musdash-db-x"`},
		{"pid", `"host"`, `"pid" is not allowed`},
		{"ipc", `"host"`, `"ipc" is not allowed`},
		{"uts", `"host"`, `"uts" is not allowed`},
		{"userns_mode", `"host"`, `"userns_mode" is not allowed`},
		{"cgroup", `"host"`, `"cgroup" is not allowed`},
		{"cgroup_parent", `"/"`, `"cgroup_parent" is not allowed`},
		{"devices", `[{"source":"/dev/sda","target":"/dev/sda"}]`, `"devices" is not allowed`},
		{"device_cgroup_rules", `["c 1:3 mr"]`, `"device_cgroup_rules" is not allowed`},
		{"cap_add", `["SYS_ADMIN"]`, `capability SYS_ADMIN`},
		{"cap_add", `["ALL"]`, `capability ALL`},
		{"cap_add", `["NET_ADMIN"]`, `capability NET_ADMIN`},
		{"security_opt", `["seccomp=unconfined"]`, `security option seccomp=unconfined`},
		{"security_opt", `["apparmor=unconfined"]`, `security option`},
		{"sysctls", `{"kernel.shmmax": "1"}`, `sysctl "kernel.shmmax"`},
		{"sysctls", `["fs.file-max=1"]`, `sysctl "fs.file-max"`},
		{"runtime", `"sysbox"`, `"runtime" is not allowed`},
		{"provider", `{"type":"/bin/sh"}`, `"provider" is not allowed: it runs a program on the server`},
		{"use_api_socket", `true`, `"use_api_socket" is not allowed`},
		{"gpus", `"all"`, `"gpus" is not allowed`},
		{"post_start", `[{"command":["id"],"privileged":true}]`, `"post_start" is not allowed`},
		// Other containers' data, and musdash's own.
		{"image", `"musdash/app1:d-abc"`, `musdash's own namespace`},
		{"image", `"musdash/nixpacks:1.41.0"`, `musdash's own namespace`},
		{"image", `"docker.io/Musdash/railpack:0.40.1"`, `musdash's own namespace`},
		{"volumes_from", `["other"]`, `"volumes_from" is not allowed`},
		{"external_links", `["musdash-db-x:db"]`, `"external_links" is not allowed`},
		{"container_name", `"musdash-db-x"`, `"container_name" is not allowed`},
		{"labels", `{"musdash.managed": "false"}`, `label "musdash.managed"`},
		{"labels", `{"Musdash.Resource": "other"}`, `label "Musdash.Resource"`},
		{"volumes", `[{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock","bind":{}}]`, `/var/run/docker.sock cannot be mounted`},
		{"volumes", `[{"type":"bind","source":"/","target":"/host","bind":{}}]`, `whole filesystem`},
		{"volumes", `[{"type":"volume","source":"data","target":"/"}]`, `nothing can be mounted at /,`},
		{"volumes", `[{"type":"tmpfs","target":"/proc/sys"}]`, `nothing can be mounted at /proc/sys`},
		{"volumes", `[{"type":"bind","source":"/etc","target":"/host-etc","bind":{}}]`, `/etc cannot be mounted`},
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/master.key","target":"/k","bind":{}}]`, `/var/lib/musdash`},
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/apps/other/env","target":"/k","bind":{}}]`, `/var/lib/musdash`},
		// A sibling directory whose name merely starts like the stack's own.
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/apps/svc1-other","target":"/k","bind":{}}]`, `/var/lib/musdash`},
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/apps/svc1/../other","target":"/k","bind":{}}]`, `/var/lib/musdash`},
		{"volumes", `[{"type":"bind","source":"/srv/x","target":"/x","bind":{"propagation":"rshared"}}]`, `"rshared" propagation`},
		{"volumes", `[{"type":"volume","source":"musdash-db-x-data","target":"/x"}]`, `not declared under "volumes"`},
		{"volumes", `[{"type":"npipe","source":"x","target":"/x"}]`, `type "npipe"`},
		{"/volumes", `{"data":{"name":"musdash-db-x-data","external":true}}`, `volume data`},
		{"/volumes", `{"data":{"name":"musdash-db-x-data"}}`, `a stack's volumes are its own`},
		{"/volumes", `{"data":{"name":"musdash-svc1_data","driver":"local","driver_opts":{"type":"none","o":"bind","device":"/"}}}`, `"driver_opts" is not supported`},
		{"/networks", `{"default":{"name":"musdash-env1","external":true}}`, `network default`},
		{"/networks", `{"default":{"name":"musdash-svc1_default","driver":"host"}}`, `only the bridge driver`},
		{"/networks", `{"default":{"name":"musdash-svc1_default","driver":"macvlan","driver_opts":{"parent":"eth0"}}}`, `network default`},
		{"/secrets", `{"key":{"name":"musdash-svc1_key","file":"/var/lib/musdash/master.key"}}`, `may not be read from a file on the server`},
		// Also not from the stack's own directory: a container that could
		// write there could make the name a link to any file.
		{"/secrets", `{"key":{"name":"musdash-svc1_key","file":"/var/lib/musdash/apps/svc1/key.txt"}}`, `may not be read from a file on the server`},
		{"/secrets", `{"key":{"name":"musdash-svc1_key","environment":"HOME"}}`, `"environment" is not supported`},
		{"/configs", `{"site":{"name":"musdash-svc1_site","file":"/etc/shadow"}}`, `may not be read from a file on the server`},
		{"/configs", `{"site":{"name":"x","external":true}}`, `"external" is not supported`},
		{"env_file", `[{"path":"/var/lib/musdash/apps/other/env"}]`, `"env_file" is not allowed`},
		// The limits musdash and the server set.
		{"oom_kill_disable", `true`, `"oom_kill_disable" is not allowed`},
		{"oom_score_adj", `-1000`, `"oom_score_adj" is not allowed`},
		{"memswap_limit", `"-1"`, `"memswap_limit" is not allowed`},
		{"cpu_shares", `262144`, `"cpu_shares" is not allowed`},
		{"deploy", `{"replicas": 5}`, `"deploy.replicas" must be 1`},
		{"deploy", `{"placement": {"constraints": ["node.role==manager"]}}`, `deploy: the option "placement"`},
		{"deploy", `{"resources": {"reservations": {"devices": [{"capabilities": ["gpu"]}]}}}`, `the option "devices"`},
		{"scale", `3`, `"scale" must be 1`},
		// Ports.
		{"ports", `[{"mode":"ingress","target":80,"published":"80","protocol":"tcp"}]`, `port 80 of the server`},
		{"ports", `[{"mode":"ingress","target":80,"published":"20001","protocol":"tcp"}]`, `port 20001 of the server`},
		{"ports", `[{"mode":"ingress","target":80,"protocol":"tcp"}]`, `published without a fixed port`},
		{"ports", `[{"mode":"host","target":80,"published":"8000-8010","protocol":"tcp"}]`, `port 8000-8010`},
		// The stack's directory holds the file musdash starts it from. A
		// container that could write there could swap in one never checked.
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/apps/svc1","target":"/s","bind":{}}]`, `next to the Compose file`},
		{"volumes", `[{"type":"bind","source":"/var/lib/musdash/apps/svc1/conf","target":"/s","bind":{}}]`, `next to the Compose file`},
		// Directories whose content the server acts on as root.
		{"volumes", `[{"type":"bind","source":"/var/spool/cron","target":"/c","bind":{}}]`, `/var/spool`},
		{"volumes", `[{"type":"bind","source":"/var/lib/cloud","target":"/c","bind":{}}]`, `/var/lib`},
		// A mount goes to a full path. The first is what Compose makes of
		// "v:/x" with a volume called v: the name is taken for a drive.
		{"volumes", `[{"type":"volume","target":"v:/x","volume":{}}]`, `the mount at "v:/x" must be a full path in the container, such as /data. A volume with a one-letter name`},
		{"volumes", `[{"type":"volume","source":"data","target":"relative/path","volume":{}}]`, `the mount at "relative/path" must be a full path`},
		{"volumes", `[{"type":"tmpfs","target":"cache"}]`, `the mount at "cache" must be a full path`},
		{"volumes", `[{"type":"bind","source":"/srv/site/conf","target":"conf","bind":{}}]`, `the mount at "conf" must be a full path`},
		{"volumes", `[{"type":"volume","source":"data","volume":{}}]`, `the mount at "" must be a full path`},
		// A subnet of the stack's choosing becomes a route of the server.
		{"/networks", `{"default":{"name":"musdash-svc1_default","ipam":{"config":[{"subnet":"8.8.8.0/24"}]}}}`, `"ipam" is not allowed`},
		{"networks", `{"default":{"ipv4_address":"8.8.8.8"}}`, `the option "ipv4_address" is not supported`},
		{"networks", `{"default":{"driver_opts":{"a":"b"}}}`, `the option "driver_opts" is not supported`},
		// Building needs a repository.
		{"build", `{"context":"/var/lib/musdash/apps/svc1","dockerfile":"Dockerfile"}`, `"build" is not allowed`},
		{"pull_policy", `"build"`, `needs a stack that comes from a Git repository`},
		{"logging", `{"driver":"syslog","options":{"syslog-address":"udp://evil.test:514"}}`, `logging driver "syslog"`},
		// Whatever Compose adds next.
		{"a_key_from_the_future", `true`, `the option "a_key_from_the_future" is not supported`},
		{"/include", `[{"path":"/etc/hostname"}]`, `top-level key "include"`},
		{"/models", `{"m":{"model":"x"}}`, `top-level key "models"`},
	}
	for _, c := range cases {
		err := with(t, c.key, c.value).Validate(testOptions())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %s\n  got  %v\n  want it to mention %q", c.key, c.value, err, c.want)
		}
	}
}

func TestValidateReportsEveryProblemAndNamesTheService(t *testing.T) {
	p := parse(t, `{"name":"musdash-svc1","services":{
		"a":{"image":"x","privileged":true,"pid":"host"},
		"b":{"command":["sleep","1"]},
		"Bad Name":{"image":"x"}}}`)
	err := p.Validate(testOptions())
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`service a: "pid" is not allowed`, `service a: "privileged" is not allowed`, `service b: it has no image`, `service Bad Name: the name is not a valid service name`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	for _, empty := range []string{`{"name":"musdash-svc1","services":{}}`, `{"name":"musdash-svc1"}`, `{"services":{"a":{"image":"x"}}}`} {
		if err := parse(t, empty).Validate(testOptions()); err == nil {
			t.Errorf("accepted %s", empty)
		}
	}
}

func TestValidateBuild(t *testing.T) {
	opt := testOptions()
	opt.BuildDir = testDir + "/src"
	ok := `{"context":"/var/lib/musdash/apps/svc1/src/api","dockerfile":"docker/Dockerfile","args":{"A":"1"},"target":"prod"}`
	if err := with(t, "build", ok).Validate(opt); err != nil {
		t.Fatalf("a build inside the repository was refused: %v", err)
	}
	// A stack from a repository may mount files of its checkout, but still
	// not the directory musdash keeps its own files in.
	mount := func(source string) string {
		return `[{"type":"bind","source":"` + source + `","target":"/m","bind":{}}]`
	}
	if err := with(t, "volumes", mount(testDir+"/src/nginx.conf")).Validate(opt); err != nil {
		t.Errorf("a file of the checkout was refused: %v", err)
	}
	if err := with(t, "volumes", mount(testDir)).Validate(opt); err == nil {
		t.Error("the stack's directory could be mounted by a stack from a repository")
	}
	if err := with(t, "volumes", mount(testDir+"/compose.resolved.json")).Validate(opt); err == nil {
		t.Error("the resolved file could be mounted by a stack from a repository")
	}
	for value, want := range map[string]string{
		`{"context":"/var/lib/musdash/apps/svc1"}`: `outside the repository`,
		`{"context":"/etc"}`:                       `outside the repository`,
		`{"context":"/var/lib/musdash/apps/svc1/src","dockerfile":"../../other/env"}`:             `Dockerfile`,
		`{"context":"/var/lib/musdash/apps/svc1/src","dockerfile":"/var/lib/musdash/master.key"}`: `Dockerfile`,
		`{"context":"/var/lib/musdash/apps/svc1/src","additional_contexts":{"host":"/"}}`:         `"additional_contexts" is not supported`,
		`{"context":"/var/lib/musdash/apps/svc1/src","secrets":[{"source":"k"}]}`:                 `"secrets" is not supported`,
		`{"context":"/var/lib/musdash/apps/svc1/src","ssh":["default"]}`:                          `"ssh" is not supported`,
		`{"context":"/var/lib/musdash/apps/svc1/src","network":"host"}`:                           `"network" is not supported`,
		`{"context":"/var/lib/musdash/apps/svc1/src","tags":["postgres:17-alpine"]}`:              `"tags" is not supported`,
		`{"context":"/var/lib/musdash/apps/svc1/src","labels":{"musdash.kind":"app"}}`:            `label "musdash.kind"`,
	} {
		if err := with(t, "build", value).Validate(opt); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("build = %s\n  got %v\n  want it to mention %q", value, err, want)
		}
	}
}

func TestEndpoints(t *testing.T) {
	raw := parse(t, `{"name":"p","services":{
		"n8n":{"image":"n8n","environment":["SERVICE_FQDN_N8N_5678","N8N_HOST=${SERVICE_FQDN_N8N}","URL=${SERVICE_URL_N8N}"]},
		"uptime-kuma":{"image":"kuma","environment":{"SERVICE_FQDN_UPTIME_KUMA_3001":null}},
		"minio":{"image":"minio","environment":["SERVICE_FQDN_CONSOLE_9001","REDIRECT=${SERVICE_URL_CONSOLE}"]},
		"worker":{"image":"w","environment":["MAIN=${SERVICE_URL_N8N}"]},
		"plain":{"image":"p","expose":["8080"],"environment":["SELF=${SERVICE_URL_PLAIN}"]},
		"short":{"image":"s","ports":["127.0.0.1:9999:7000/tcp"],"environment":["SELF=${SERVICE_HTTPS_SHORT}"]}}}`)
	text := `SERVICE_FQDN_N8N_5678 SERVICE_FQDN_N8N SERVICE_URL_N8N SERVICE_FQDN_UPTIME_KUMA_3001 SERVICE_FQDN_CONSOLE_9001 SERVICE_URL_CONSOLE SERVICE_URL_PLAIN SERVICE_HTTPS_SHORT SERVICE_PASSWORD_X`
	got, err := Endpoints(raw, catalog.ScanMagic(text))
	if err != nil {
		t.Fatal(err)
	}
	want := []Endpoint{
		{Name: "CONSOLE", Service: "minio", Port: 9001}, // named after no service: the one that mentions it
		{Name: "N8N", Service: "n8n", Port: 5678},       // by name, although "worker" mentions it too
		{Name: "PLAIN", Service: "plain", Port: 8080},   // no port in the name: the first it exposes
		{Name: "SHORT", Service: "short", Port: 7000},
		{Name: "UPTIME_KUMA", Service: "uptime-kuma", Port: 3001},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("endpoint %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	for name, c := range map[string][2]string{
		"two ports":       {`{"name":"p","services":{"app":{"image":"x"}}}`, "SERVICE_FQDN_APP_80 SERVICE_URL_APP_8080"},
		"no owner":        {`{"name":"p","services":{"a":{"image":"x","environment":["U=${SERVICE_URL_SITE_80}"]},"b":{"image":"x","environment":["U=${SERVICE_URL_SITE_80}"]}}}`, "SERVICE_URL_SITE_80"},
		"nobody uses it":  {`{"name":"p","services":{"a":{"image":"x"}}}`, "SERVICE_URL_SITE_80"},
		"no port":         {`{"name":"p","services":{"app":{"image":"x","environment":["U=${SERVICE_URL_APP}"]}}}`, "SERVICE_URL_APP"},
		"same port twice": {`{"name":"p","services":{"app":{"image":"x","environment":["SERVICE_FQDN_APP_80","SERVICE_FQDN_ALSO_80"]}}}`, "SERVICE_FQDN_APP_80 SERVICE_FQDN_ALSO_80"},
	} {
		if got, err := Endpoints(parse(t, c[0]), catalog.ScanMagic(c[1])); err == nil {
			t.Errorf("%s: accepted as %+v", name, got)
		}
	}
}

func TestApply(t *testing.T) {
	p := parse(t, `{"name":"musdash-svc1","networks":{"default":{"name":"musdash-svc1_default"}},
		"volumes":{"data":{"name":"musdash-svc1_data"},"more":{"name":"musdash-svc1_more"}},
		"services":{
		"web":{"image":"nginx","labels":{"a":"b"},"networks":{"default":null},"ports":[{"target":25,"published":"2525"}]},
		"db":{"image":"postgres","restart":"no","networks":{"default":null},"environment":{"POSTGRES_DB":"app","AWS_SECRET_ACCESS_KEY":null,"EMPTY":""}},
		"side":{"image":"x","network_mode":"service:web"},
		"built":{"image":"postgres:17-alpine","build":{"context":"/src"},"networks":{"default":null}}}}`)
	p.Apply(Override{ServiceID: "svc1", EnvNetwork: "musdash-env1", Publish: []Published{{Service: "web", Port: 80, HostPort: 20007}}})
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Files of a stack's checkout are mounted read-only, every one of them
	// and in whatever form it was written; what lies elsewhere is not
	// touched. A path that is not there is not created.
	ro := parse(t, `{"name":"p","services":{"a":{"image":"x","build":{"context":"/data/apps/s/src-1","args":{"KEPT":"1","FROM_SERVER":null}},"volumes":[
		{"type":"bind","source":"/data/apps/s/src-1","target":"/all","bind":{"create_host_path":true}},
		{"type":"bind","source":"/data/apps/s/src-1/conf/site.conf","target":"/c"},
		{"type":"bind","source":"/data/apps/s/src-1/.git","target":"/g","read_only":false},
		{"type":"bind","source":"/srv/shared","target":"/s","bind":{"create_host_path":true}},
		{"type":"volume","source":"data","target":"/d"}]}}}`)
	ro.Apply(Override{ServiceID: "s", ReadOnlyUnder: "/data/apps/s/src-1"})
	roRaw, _ := ro.Marshal()
	var roDoc struct {
		Services map[string]struct {
			Build   struct{ Args map[string]any }
			Volumes []struct {
				Source   string
				ReadOnly bool `json:"read_only"`
				Bind     struct {
					Create *bool `json:"create_host_path"`
				}
			}
		}
	}
	json.Unmarshal(roRaw, &roDoc)
	for _, v := range roDoc.Services["a"].Volumes {
		inCheckout := strings.HasPrefix(v.Source, "/data/apps/s/src-1")
		if v.ReadOnly != inCheckout {
			t.Errorf("%s: read_only=%v", v.Source, v.ReadOnly)
		}
		if inCheckout && (v.Bind.Create == nil || *v.Bind.Create) {
			t.Errorf("%s: Docker may still create the path", v.Source)
		}
		if v.Source == "/srv/shared" && (v.Bind.Create == nil || !*v.Bind.Create) {
			t.Errorf("a mount outside the checkout was changed")
		}
	}
	// A build argument without a value would be read from musdash's own
	// environment by the build.
	if args := roDoc.Services["a"].Build.Args; len(args) != 1 || args["KEPT"] != "1" {
		t.Errorf("build arguments after Apply: %v", args)
	}

	// A variable left without a value would be filled in from musdash's own
	// environment when Compose starts the stack. An empty one is a value.
	if s := string(raw); strings.Contains(s, "AWS_SECRET_ACCESS_KEY") || !strings.Contains(s, `"POSTGRES_DB"`) || !strings.Contains(s, `"EMPTY"`) {
		t.Errorf("variables after Apply: %s", s)
	}
	var doc struct {
		Networks map[string]map[string]any
		Services map[string]struct {
			Image       string
			Restart     string
			Labels      map[string]string
			Networks    map[string]any
			NetworkMode string `json:"network_mode"`
			Ports       []map[string]any
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for name, svc := range doc.Services {
		if svc.Labels["musdash.managed"] != "true" || svc.Labels["musdash.kind"] != "service" || svc.Labels["musdash.resource"] != "svc1" {
			t.Errorf("%s: labels %v", name, svc.Labels)
		}
	}
	web := doc.Services["web"]
	if web.Labels["a"] != "b" || web.Restart != "unless-stopped" {
		t.Errorf("web: %+v", web)
	}
	// The endpoint is published on the loopback interface only, next to the
	// port the file itself publishes.
	if len(web.Ports) != 2 || web.Ports[1]["host_ip"] != "127.0.0.1" || web.Ports[1]["published"] != "20007" || web.Ports[1]["target"] != float64(80) {
		t.Errorf("web ports: %v", web.Ports)
	}
	if doc.Services["db"].Restart != "no" || len(doc.Services["db"].Ports) != 0 {
		t.Errorf("db: %+v", doc.Services["db"])
	}
	// Joined to the environment's network, except the service that has no
	// network of its own.
	for _, name := range []string{"web", "db", "built"} {
		if _, ok := doc.Services[name].Networks["musdash-environment"]; !ok {
			t.Errorf("%s did not join the environment network: %v", name, doc.Services[name].Networks)
		}
		if _, ok := doc.Services[name].Networks["default"]; !ok {
			t.Errorf("%s left the stack's own network", name)
		}
	}
	if len(doc.Services["side"].Networks) != 0 {
		t.Errorf("side shares web's network and cannot join another: %v", doc.Services["side"].Networks)
	}
	if n := doc.Networks["musdash-environment"]; n["name"] != "musdash-env1" || n["external"] != true {
		t.Errorf("environment network: %v", n)
	}
	// A built image never takes the name the file gave it.
	if doc.Services["built"].Image != "musdash-svc1-built" {
		t.Errorf("built image is named %q", doc.Services["built"].Image)
	}
	if got := strings.Join(p.Volumes(), ","); got != "musdash-svc1_data,musdash-svc1_more" {
		t.Errorf("volumes: %s", got)
	}

	// Without an environment network nothing is added.
	q := parse(t, `{"name":"p","services":{"web":{"image":"nginx"}}}`)
	q.Apply(Override{ServiceID: "s"})
	out, _ := q.Marshal()
	if strings.Contains(string(out), "musdash-environment") || strings.Contains(string(out), `"networks"`) {
		t.Errorf("networks were added without being asked:\n%s", out)
	}
}

func TestNumbersSurviveARoundTrip(t *testing.T) {
	p := parse(t, `{"name":"p","services":{"a":{"image":"x","mem_limit":"8589934592","cpus":0.25,"pids_limit":9007199254740993}}}`)
	out, _ := p.Marshal()
	for _, want := range []string{`"8589934592"`, `0.25`, `9007199254740993`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s was changed on the way through:\n%s", want, out)
		}
	}
}

func TestConfigCmd(t *testing.T) {
	envFile := testDir + "/sandbox.env"
	base := ConfigOptions{Image: "docker:29-cli", Dir: testDir, Source: []byte("services: {}"), EnvFile: envFile, Project: testProject, User: "1000:1000"}
	o := base
	o.Raw = true
	cmd, err := ConfigCmd(o)
	if err != nil {
		t.Fatal(err)
	}
	line := cmd.Name + " " + strings.Join(cmd.Args, " ")
	want := "docker run --rm --interactive --network none --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --pids-limit 128 --user 1000:1000 --tmpfs /tmp --workdir /tmp " +
		"--env-file " + envFile + " --env HOME=/tmp --env DOCKER_CONFIG=/tmp/.docker" +
		" docker:29-cli compose --project-name " + testProject + " --project-directory " + testDir + " --file - config --format json --no-interpolate"
	if line != want {
		t.Fatalf("got\n %s\nwant\n %s", line, want)
	}
	// A pasted file is fed on standard input; the sandbox is given nothing
	// of the server at all.
	if cmd.Stdin == nil || strings.Contains(line, "--mount") || strings.Contains(line, "docker.sock") || strings.Contains(line, " -v ") {
		t.Fatalf("the sandbox sees part of the server: %s", line)
	}

	// A stack from a repository: the checkout, read-only, and nothing else.
	git := base
	git.Source, git.Mount, git.Dir, git.File = nil, testDir+"/src", testDir+"/src/deploy", "compose.yaml"
	cmd, err = ConfigCmd(git)
	if err != nil {
		t.Fatal(err)
	}
	line = strings.Join(cmd.Args, " ")
	if !strings.Contains(line, "--mount type=bind,source="+testDir+"/src,target="+testDir+"/src,readonly") || strings.Count(line, "--mount") != 1 ||
		!strings.HasSuffix(line, "--project-directory "+testDir+"/src/deploy --file "+testDir+"/src/deploy/compose.yaml config --format json") || cmd.Stdin != nil {
		t.Fatalf("git: %s", line)
	}

	for name, change := range map[string]func(*ConfigOptions){
		"relative dir":            func(o *ConfigOptions) { o.Dir = "apps/svc1" },
		"unclean dir":             func(o *ConfigOptions) { o.Dir = testDir + "/../other" },
		"relative variables file": func(o *ConfigOptions) { o.EnvFile = "sandbox.env" },
		"nothing to load":         func(o *ConfigOptions) { o.Source = nil },
		"no image":                func(o *ConfigOptions) { o.Image = "" },
		"no user":                 func(o *ConfigOptions) { o.User = "" },
	} {
		o := base
		change(&o)
		if cmd, err := ConfigCmd(o); err == nil {
			t.Errorf("%s: accepted: %v", name, cmd.Args)
		}
	}
	for name, change := range map[string]func(*ConfigOptions){
		"mount with a comma": func(o *ConfigOptions) { o.Mount = "/a,source=/,target=/host"; o.Dir = o.Mount },
		"dir outside mount":  func(o *ConfigOptions) { o.Dir = "/etc" },
		"dir of a sibling":   func(o *ConfigOptions) { o.Dir = o.Mount + "-other" },
		"absolute file":      func(o *ConfigOptions) { o.File = "/etc/passwd" },
		"file climbs out":    func(o *ConfigOptions) { o.File = "../other/docker-compose.yml" },
		"no file":            func(o *ConfigOptions) { o.File = "" },
	} {
		o := git
		change(&o)
		if cmd, err := ConfigCmd(o); err == nil {
			t.Errorf("git, %s: accepted: %v", name, cmd.Args)
		}
	}
}

func TestSandboxImage(t *testing.T) {
	for version, want := range map[string]string{"29.8.0": "docker:29-cli", "27.3.1\n": "docker:27-cli", "": "docker:cli", "dev": "docker:cli", "v29": "docker:cli"} {
		if got := SandboxImage(version); got != want {
			t.Errorf("%q: %s, want %s", version, got, want)
		}
	}
}

func TestNestedInCheckout(t *testing.T) {
	p := parse(t, `{"name":"x","services":{
		"b":{"volumes":[
			{"type":"bind","source":"/co/app","target":"/usr/src/app"},
			{"type":"bind","source":"/co/app/conf","target":"/usr/src/app/conf/"},
			{"type":"volume","target":"/usr/src/app/node_modules"},
			{"type":"volume","source":"cache","target":"/usr/src/app/conf/cache"},
			{"type":"volume","source":"data","target":"/usr/src/application"},
			{"type":"tmpfs","target":"/tmp"}]},
		"a":{"volumes":[
			{"type":"bind","source":"/srv/files","target":"/files"},
			{"type":"volume","target":"/files/tmp"}]}}}`)
	got := p.NestedInCheckout("/co")
	want := []Nested{
		{"b", "/usr/src/app/conf", "/usr/src/app", "/co/app/conf"},
		{"b", "/usr/src/app/node_modules", "/usr/src/app", "/co/app/node_modules"},
		{"b", "/usr/src/app/conf/cache", "/usr/src/app/conf", "/co/app/conf/cache"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
