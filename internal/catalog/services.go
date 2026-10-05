package catalog

import (
	"embed"
	"path"
	"sort"
	"strings"
	"sync"
)

// The service templates are Compose files written for musdash. Each starts
// with a few comment lines of the form "# key: value" that describe it:
//
//	name     what the catalogue calls it
//	about    one line for the catalogue
//	docs     where its documentation is
//	connect  "true" when the stack must join the environment's network to
//	         be of use, as a tunnel that forwards to apps does
//
//go:embed services/*.yaml
var serviceFiles embed.FS

// ServiceTemplate is one entry of the service catalogue.
type ServiceTemplate struct {
	Key     string // the file's name without ".yaml"; stable, used in URLs
	Name    string
	About   string
	Docs    string
	Compose string // the whole file, header included
	// ConnectEnv is the template's default for joining the environment's
	// network.
	ConnectEnv bool
}

// Read on first use: the proxy is the same binary and never needs them.
var serviceTemplates = sync.OnceValue(func() []ServiceTemplate {
	entries, err := serviceFiles.ReadDir("services")
	if err != nil {
		panic(err)
	}
	out := make([]ServiceTemplate, 0, len(entries))
	for _, e := range entries {
		raw, err := serviceFiles.ReadFile(path.Join("services", e.Name()))
		if err != nil {
			panic(err)
		}
		t := ServiceTemplate{Key: strings.TrimSuffix(e.Name(), ".yaml"), Compose: string(raw)}
		for _, line := range strings.Split(t.Compose, "\n") {
			rest, isComment := strings.CutPrefix(line, "#")
			if !isComment {
				break // the header ends at the first line of YAML
			}
			key, value, ok := strings.Cut(rest, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "name":
				t.Name = value
			case "about":
				t.About = value
			case "docs":
				t.Docs = value
			case "connect":
				t.ConnectEnv = value == "true"
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
})

// Services returns the catalogue, sorted by name.
func Services() []ServiceTemplate { return serviceTemplates() }

// Service finds a template by key.
func Service(key string) (ServiceTemplate, bool) {
	for _, t := range serviceTemplates() {
		if t.Key == key {
			return t, true
		}
	}
	return ServiceTemplate{}, false
}
