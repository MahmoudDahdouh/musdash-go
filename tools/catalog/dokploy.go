package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// A Dokploy blueprint is three files: docker-compose.yml, meta.json (what
// the catalogue says about it) and template.toml, which names the values
// Dokploy generates, the variables it writes into the stack's .env, the
// services that get a domain and the files it puts next to the stack.

type dokMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Logo        string   `json:"logo"`
	Tags        []string `json:"tags"`
	Links       struct {
		GitHub  string `json:"github"`
		Website string `json:"website"`
		Docs    string `json:"docs"`
	} `json:"links"`
}

type dokDomain struct {
	ServiceName string
	Port        int64
	Host        string
}

type dokMount struct{ FilePath, Content string }

type dokTOML struct {
	Variables map[string]any
	Env       any
	Domains   []dokDomain
	Mounts    []dokMount
}

var emptyList = regexp.MustCompile(`(?m)^[ \t]*(mounts|domains|env)[ \t]*=[ \t]*\[[ \t]*\][ \t]*\r?\n`)

// readTOML reads a template.toml the way Dokploy does, which is more
// forgiving than the format: a list may be given empty and then again as
// tables, and a number may be written as text.
func readTOML(file string) (dokTOML, error) {
	var conf dokTOML
	raw, err := os.ReadFile(file)
	if err != nil {
		return conf, err
	}
	text := emptyList.ReplaceAllStringFunc(string(raw), func(line string) string {
		name := emptyList.FindStringSubmatch(line)[1]
		if strings.Contains(string(raw), "[[config."+name+"]]") || strings.Contains(string(raw), "[config."+name+"]") {
			return ""
		}
		return line
	})
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		return conf, err
	}
	conf.Variables, _ = doc["variables"].(map[string]any)
	config, _ := doc["config"].(map[string]any)
	conf.Env = config["env"]
	tables := func(v any) []map[string]any {
		var out []map[string]any
		switch list := v.(type) {
		case []map[string]any:
			out = list
		case []any:
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
		return out
	}
	text2 := func(v any) string {
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	for _, d := range tables(config["domains"]) {
		port, _ := strconv.ParseInt(strings.ReplaceAll(text2(d["port"]), "_", ""), 10, 64)
		conf.Domains = append(conf.Domains, dokDomain{text2(d["serviceName"]), port, text2(d["host"])})
	}
	for _, m := range tables(config["mounts"]) {
		conf.Mounts = append(conf.Mounts, dokMount{text2(m["filePath"]), text2(m["content"])})
	}
	return conf, nil
}

type envPair struct{ Key, Value string }

// dokploy turns a blueprint's own values into musdash's.
type dokploy struct {
	vars  map[string]string
	bound map[string]string // a variable that is a service's address: its endpoint
	only  string            // the endpoint, when the blueprint has just one
	uses  map[string]int    // how often template.toml refers to a variable
	done  map[string]string
	busy  map[string]bool
	names map[string]int
	err   error
}

func (d *dokploy) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf(format, args...)
	}
}

var tokenRE = regexp.MustCompile(`\$\{([^${}]+)\}`)

func magicID(s string) string {
	s = strings.Trim(regexp.MustCompile(`[^A-Z0-9]+`).ReplaceAllString(strings.ToUpper(s), "_"), "_")
	if s == "" {
		s = "VALUE"
	}
	return s
}

// id makes the label of a generated value from what it is for, distinct
// from the others of the blueprint.
func (d *dokploy) id(ctx string) string {
	id := magicID(ctx)
	// A label that starts with a length would be read as one.
	if regexp.MustCompile(`^(16|32|64|128)_`).MatchString(id) {
		id = "V" + id
	}
	d.names[id]++
	if n := d.names[id]; n > 1 {
		id += "_" + strconv.Itoa(n)
	}
	return id
}

// expand rewrites a value of template.toml into text Compose can fill in:
// a variable is replaced by what it stands for, a helper by the magic
// variable that generates the same kind of value, and a dollar sign that is
// the value's own (a password hash, a price) is escaped. ctx names what the
// value is for. file is set for the content of a file, where a "${…}" that
// is neither is the file's own text too; in a variable of the .env file it
// is one a person sets.
func (d *dokploy) expand(s, ctx string, file bool) string {
	var b strings.Builder
	last := 0
	for _, loc := range tokenRE.FindAllStringIndex(s, -1) {
		b.WriteString(escapeDollars(s[last:loc[0]]))
		tok := s[loc[0]:loc[1]]
		out, known := d.token(tok, ctx)
		if !known && file {
			out = escapeDollars(tok)
		}
		b.WriteString(out)
		last = loc[1]
	}
	b.WriteString(escapeDollars(s[last:]))
	return b.String()
}

// token is what one "${…}" of template.toml becomes, and whether it is a
// variable or a helper of the blueprint at all.
func (d *dokploy) token(tok, ctx string) (string, bool) {
	inner := strings.TrimSpace(tok[2 : len(tok)-1])
	// A variable named after the helper it is made with
	// (username = "${username}") is the helper.
	if _, ok := d.vars[inner]; ok && !d.busy[inner] {
		return d.variable(inner), true
	}
	name, arg, _ := strings.Cut(inner, ":")
	n, _ := strconv.Atoi(arg)
	switch name {
	case "domain":
		// An address beside the one the blueprint routes: with one
		// routed service it can only be that one's.
		if d.only != "" {
			return "${SERVICE_FQDN_" + d.only + "}", true
		}
		d.fail("it names an address that no service is routed to (%s)", ctx)
		return "", true
	case "password":
		if n > 32 {
			return "${SERVICE_PASSWORD_64_" + d.id(ctx) + "}", true
		}
		return "${SERVICE_PASSWORD_" + d.id(ctx) + "}", true
	case "base64":
		switch {
		case n > 64:
			return "${SERVICE_BASE64_128_" + d.id(ctx) + "}", true
		case n > 32:
			return "${SERVICE_BASE64_64_" + d.id(ctx) + "}", true
		}
		return "${SERVICE_BASE64_" + d.id(ctx) + "}", true
	case "hash":
		switch {
		case n > 32:
			return "${SERVICE_HEX_64_" + d.id(ctx) + "}", true
		case n > 16 || n == 0:
			return "${SERVICE_HEX_" + d.id(ctx) + "}", true
		}
		return "${SERVICE_HEX_16_" + d.id(ctx) + "}", true
	case "username":
		return "${SERVICE_USER_" + d.id(ctx) + "}", true
	case "email":
		v := magicID(ctx)
		if !strings.Contains(v, "EMAIL") {
			v += "_EMAIL"
		}
		return "${" + v + ":?Set an email address}", true
	case "uuid", "jwt", "timestamp", "timestampms", "timestamps", "randomPort":
		d.fail("it needs a generated value musdash has no equal of: ${%s}", name)
		return "", true
	}
	return tok, false
}

var secretName = regexp.MustCompile(`(?i)(password|passwd|pass|secret)$`)

func (d *dokploy) variable(name string) string {
	if id, ok := d.bound[name]; ok {
		return "${SERVICE_FQDN_" + id + "}"
	}
	if v, ok := d.done[name]; ok {
		return v
	}
	// A password the blueprint writes out is the same for everyone who
	// installs it. Where the blueprint itself hands it to both ends
	// through this variable, a generated one serves as well and is nobody
	// else's.
	if raw := d.vars[name]; secretName.MatchString(name) && !strings.Contains(raw, "$") && len(raw) >= 3 {
		v := ""
		switch {
		case appLogin.MatchString(name):
			// The app's own sign-in: see appLogin.
			v = "${" + magicID(name) + askPassword + "}"
		case d.uses[name] >= 2:
			v = "${SERVICE_PASSWORD_" + d.id(name) + "}"
		}
		if v != "" {
			d.done[name] = v
			return v
		}
	}
	d.busy[name] = true
	v := d.expand(d.vars[name], name, false)
	d.busy[name] = false
	d.done[name] = v
	return v
}

// readDokploy reads a blueprint and applies its template.toml to its
// Compose file, which leaves a file in the shape of a Coolify template.
func readDokploy(dir string) (*tmpl, error) {
	var meta dokMeta
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	t := &tmpl{
		Key: filepath.Base(dir), Name: strings.TrimSpace(meta.Name), About: meta.Description, Source: "dokploy",
		Docs: meta.Links.Docs, Website: meta.Links.Website, Tags: meta.Tags, files: map[string]string{},
	}
	if t.Website == "" {
		t.Website = meta.Links.GitHub
	}
	if strings.HasSuffix(strings.ToLower(meta.Logo), ".svg") {
		t.Logos = append(t.Logos, filepath.Join(dir, meta.Logo))
	}
	body, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil || len(doc.Content) == 0 || !isMap(doc.Content[0]) {
		return t, fmt.Errorf("its Compose file could not be read")
	}
	t.Root = expand(doc.Content[0])

	conf, err := readTOML(filepath.Join(dir, "template.toml"))
	if err != nil {
		return t, fmt.Errorf("its template.toml could not be read")
	}
	// A password as a hash is one nobody can replace: the blueprint ships
	// a known sign-in to everyone who installs it.
	tomlText, _ := os.ReadFile(filepath.Join(dir, "template.toml"))
	if hashRE.Match(tomlText) || hashRE.Match(body) {
		return t, fmt.Errorf("it ships a fixed password, as a hash, that would be the same on every install")
	}
	d := &dokploy{vars: map[string]string{}, bound: map[string]string{}, done: map[string]string{}, busy: map[string]bool{}, names: map[string]int{}, uses: map[string]int{}}
	for k, v := range conf.Variables {
		d.vars[k] = fmt.Sprint(v)
		d.uses[k] = strings.Count(string(tomlText), "${"+k+"}")
	}
	services := mapGet(t.Root, "services")
	if !isMap(services) {
		return t, fmt.Errorf("its Compose file has no services")
	}

	// Addresses first: a variable that is a service's host name stands for
	// that service's endpoint wherever else it is used.
	type endpoint struct {
		service string
		port    int64
		id      string
	}
	var endpoints []endpoint
	perService := map[string]int{}
	for _, dom := range conf.Domains {
		if mapGet(services, dom.ServiceName) == nil {
			return t, fmt.Errorf("it routes an address to a service its Compose file does not have (%s)", dom.ServiceName)
		}
		if dom.Port < 1 || dom.Port > 65535 {
			return t, fmt.Errorf("it routes an address to no port")
		}
		host := strings.TrimSpace(dom.Host)
		variable := ""
		if m := tokenRE.FindStringSubmatch(host); m != nil && m[0] == host {
			if _, ok := d.vars[m[1]]; ok {
				variable = m[1]
			}
		}
		same := false
		for _, e := range endpoints {
			if e.service == dom.ServiceName && e.port == dom.Port {
				// One endpoint under two addresses or paths: the second
				// name is the first's.
				if variable != "" && d.bound[variable] == "" {
					d.bound[variable] = e.id
				}
				same = true
			}
		}
		if same {
			continue
		}
		if variable != "" && d.bound[variable] != "" {
			return t, fmt.Errorf("it serves one address from two services, by path")
		}
		id := magicID(dom.ServiceName)
		if perService[dom.ServiceName] > 0 {
			id += "_P" + strconv.FormatInt(dom.Port, 10)
		}
		perService[dom.ServiceName]++
		endpoints = append(endpoints, endpoint{dom.ServiceName, dom.Port, id})
		if variable != "" {
			d.bound[variable] = id
		}
	}

	if len(endpoints) == 1 {
		d.only = endpoints[0].id
	}

	var env []envPair
	switch e := conf.Env.(type) {
	case []any:
		for _, item := range e {
			k, v, _ := strings.Cut(fmt.Sprint(item), "=")
			if k = strings.TrimSpace(k); k != "" && !strings.HasPrefix(k, "#") {
				env = append(env, envPair{k, v})
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = append(env, envPair{k, fmt.Sprint(e[k])})
		}
	}
	values := map[string]string{}
	for i, p := range env {
		// Quotes around a value are the .env file's, not the value's.
		v := strings.TrimSpace(p.Value)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		env[i].Value = d.expand(v, p.Key, false)
		values[p.Key] = env[i].Value
	}
	// inPlace is what a reference to a variable of the .env file becomes.
	inPlace := func(name string) string {
		v := values[name]
		if strings.ContainsAny(v, "$}\n") {
			return v
		}
		// A fixed value stays a variable, so that a person can change it.
		return "${" + name + ":-" + v + "}"
	}
	subst := func(n *yaml.Node) {
		setValue(n, rewriteRefs(n.Value, func(r ref) (string, bool) {
			if _, ok := values[r.Name]; ok {
				return inPlace(r.Name), true
			}
			return "", false
		}))
	}
	valuesOf(t.Root, subst)

	for _, name := range mapKeys(services) {
		svc := mapGet(services, name)
		if !isMap(svc) {
			continue
		}
		environment := mapGet(svc, "environment")
		// "- NAME" and "NAME:" take the value from the .env file.
		if isSeq(environment) {
			for _, item := range environment.Content {
				if _, ok := values[item.Value]; ok && isStr(item) {
					setValue(item, item.Value+"="+inPlace(item.Value))
				}
			}
		} else if isMap(environment) {
			for i := 0; i+1 < len(environment.Content); i += 2 {
				if _, ok := values[environment.Content[i].Value]; ok && isNull(environment.Content[i+1]) {
					environment.Content[i+1] = str(inPlace(environment.Content[i].Value))
				}
			}
		}
		// env_file hands a container the whole .env file.
		if file := mapDel(svc, "env_file"); file != nil {
			for _, p := range env {
				if !hasEnv(svc, p.Key) {
					addEnv(svc, p.Key, p.Value)
				}
			}
		}
	}
	for _, e := range endpoints {
		addEnv(mapGet(services, e.service), "SERVICE_FQDN_"+e.id+"_"+strconv.FormatInt(e.port, 10), "")
	}
	for i, m := range conf.Mounts {
		// Dokploy fills its variables into a file's content; everything
		// else in it is the file's own, and must get past Compose as
		// written.
		t.files[cleanRel(m.FilePath)] = d.expand(m.Content, "FILE"+strconv.Itoa(i+1), true)
	}
	if d.err != nil {
		return t, d.err
	}
	// An address written with its scheme follows the endpoint's own.
	valuesOf(t.Root, func(n *yaml.Node) {
		setValue(n, schemeRE.ReplaceAllString(n.Value, "${SERVICE_URL_$1}"))
	})
	for path, content := range t.files {
		t.files[path] = schemeRE.ReplaceAllString(content, "${SERVICE_URL_$1}")
	}
	return t, nil
}

var hashRE = regexp.MustCompile(`\$(argon2(id|i|d)?|2[aby]|apr1|pbkdf2[a-z0-9-]*|scrypt)\$`)

var schemeRE = regexp.MustCompile(`https?://\$\{SERVICE_FQDN_([A-Z0-9_]+)\}`)

func cleanRel(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+p)), "/")
}

// valuesOf is values under its other name, for a scope that has a variable
// called values.
func valuesOf(n *yaml.Node, fn func(*yaml.Node)) { values(n, fn) }

// hasEnv reports whether a service's environment sets a variable.
func hasEnv(svc *yaml.Node, key string) bool {
	environment := mapGet(svc, "environment")
	if isMap(environment) {
		return mapGet(environment, key) != nil
	}
	if isSeq(environment) {
		for _, item := range environment.Content {
			if k, _, _ := strings.Cut(item.Value, "="); k == key {
				return true
			}
		}
	}
	return false
}

// addEnv adds a variable to a service's environment, in the form the
// service already writes it in. An empty value is the bare name, which
// Compose fills from the stack's variables.
func addEnv(svc *yaml.Node, key, value string) {
	if hasEnv(svc, key) {
		return
	}
	environment := mapGet(svc, "environment")
	switch {
	case isMap(environment):
		v := str(value)
		if value == "" {
			v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
		}
		environment.Content = append(environment.Content, str(key), v)
	case isSeq(environment):
		if value != "" {
			key += "=" + value
		}
		environment.Content = append(environment.Content, str(key))
	default:
		seq := newSeq()
		if value != "" {
			key += "=" + value
		}
		seq.Content = append(seq.Content, str(key))
		mapSet(svc, "environment", seq)
	}
}
