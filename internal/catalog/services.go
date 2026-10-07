package catalog

import (
	"bufio"
	"embed"
	"path"
	"sort"
	"strings"
	"sync"
)

// The service templates are Compose files. Each starts with a few comment
// lines of the form "# key: value" that describe it:
//
//	name        what the catalogue calls it
//	about       one line for the catalogue
//	docs        where its documentation is
//	website     where its makers present it
//	categories  one to three keys of Categories, separated by commas
//	source      whose template it was made from ("coolify", "dokploy");
//	            none for one written for musdash. See services.LICENSE
//	connect     "true" when the stack must join the environment's network to
//	            be of use, as a tunnel that forwards to apps does
//
//go:embed services/*.yaml
var serviceFiles embed.FS

// Category is one of the kinds of service the catalogue is sorted into.
type Category struct {
	Key   string // stable: what a template's header names
	Label string
}

// A template names one to three of these. The list is fixed, so that what
// the catalogue's makers called things does not become a filter button
// each.
var categories = []Category{
	{"ai", "AI"},
	{"analytics", "Analytics"},
	{"auth", "Authentication"},
	{"automation", "Automation"},
	{"backend", "Backend"},
	{"business", "Business"},
	{"cms", "CMS"},
	{"communication", "Communication"},
	{"database", "Databases"},
	{"devtools", "Developer tools"},
	{"docs", "Documentation"},
	{"ecommerce", "E-commerce"},
	{"email", "Email"},
	{"finance", "Finance"},
	{"games", "Games"},
	{"git", "Git and CI"},
	{"home", "Home"},
	{"media", "Media"},
	{"monitoring", "Monitoring"},
	{"networking", "Networking"},
	{"productivity", "Productivity"},
	{"rss", "RSS and reading"},
	{"search", "Search"},
	{"security", "Security"},
	{"storage", "Storage"},
	{"support", "Support"},
}

// Categories returns every category, sorted by label.
func Categories() []Category { return categories }

// CategoryLabel is a category's name, or "" for a key that is not one.
func CategoryLabel(key string) string {
	for _, c := range categories {
		if c.Key == key {
			return c.Label
		}
	}
	return ""
}

// ServiceTemplate is one entry of the service catalogue.
type ServiceTemplate struct {
	Key     string // the file's name without ".yaml"; stable, used in URLs
	Name    string
	About   string
	Docs    string
	Website string
	// Categories are keys of Categories, in the order the header has them.
	Categories []string
	Source     string
	// Compose is the whole file, header included. Service fills it in;
	// the entries of Services have none.
	Compose string
	// ConnectEnv is the template's default for joining the environment's
	// network.
	ConnectEnv bool
}

// header reads the comment lines a template starts with.
func (t *ServiceTemplate) header(line string) bool {
	rest, isComment := strings.CutPrefix(line, "#")
	if !isComment {
		return false // the header ends at the first line of YAML
	}
	key, value, ok := strings.Cut(rest, ":")
	if !ok {
		return true
	}
	value = strings.TrimSpace(value)
	switch strings.TrimSpace(key) {
	case "name":
		t.Name = value
	case "about":
		t.About = value
	case "docs":
		t.Docs = value
	case "website":
		t.Website = value
	case "source":
		t.Source = value
	case "connect":
		t.ConnectEnv = value == "true"
	case "categories":
		for _, c := range strings.Split(value, ",") {
			c = strings.TrimSpace(c)
			// The list's own string, so that a few hundred templates do
			// not each hold a copy of "productivity". A key that is not
			// in the list is kept as written for the test to find.
			for _, known := range categories {
				if known.Key == c {
					c = known.Key
				}
			}
			if c != "" {
				t.Categories = append(t.Categories, c)
			}
		}
	}
	return true
}

// Read on first use: the proxy is the same binary and never needs them.
// Only the headers are read and kept. The catalogue is several hundred
// Compose files, and the page that lists them needs none of their text.
var serviceTemplates = sync.OnceValue(func() []ServiceTemplate {
	entries, err := serviceFiles.ReadDir("services")
	if err != nil {
		panic(err)
	}
	out := make([]ServiceTemplate, 0, len(entries))
	for _, e := range entries {
		f, err := serviceFiles.Open(path.Join("services", e.Name()))
		if err != nil {
			panic(err)
		}
		t := ServiceTemplate{Key: strings.TrimSuffix(e.Name(), ".yaml")}
		lines := bufio.NewScanner(f)
		for lines.Scan() && t.header(lines.Text()) {
		}
		f.Close()
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
})

// Services returns the catalogue, sorted by name: what each template's
// header says, without its Compose text.
func Services() []ServiceTemplate { return serviceTemplates() }

// Service finds a template by key and reads its Compose text.
func Service(key string) (ServiceTemplate, bool) {
	for _, t := range serviceTemplates() {
		if t.Key != key {
			continue
		}
		// The name comes from the list, never from the caller: a key is
		// not a path.
		raw, err := serviceFiles.ReadFile(path.Join("services", t.Key+".yaml"))
		if err != nil {
			return ServiceTemplate{}, false
		}
		t.Compose = string(raw)
		return t, true
	}
	return ServiceTemplate{}, false
}

// ServiceName is the name of a template, for a page that shows what a
// service was made from and needs nothing else of it.
func ServiceName(key string) (string, bool) {
	for _, t := range serviceTemplates() {
		if t.Key == key {
			return t.Name, true
		}
	}
	return "", false
}

// CategoriesInUse returns the categories at least one template has, in the
// list's order, each with the number of its templates.
func CategoriesInUse() ([]Category, map[string]int) {
	count := map[string]int{}
	for _, t := range serviceTemplates() {
		for _, c := range t.Categories {
			count[c]++
		}
	}
	var out []Category
	for _, c := range categories {
		if count[c.Key] > 0 {
			out = append(out, c)
		}
	}
	return out, count
}
