package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// tmpl is one template on its way into the catalogue.
type tmpl struct {
	Key, Name, About, Docs, Website string
	Source                          string
	// SrcCategory and Tags are the source's own words; Categories are
	// musdash's.
	SrcCategory string
	Tags        []string
	Categories  []string
	// Port is the port a Coolify header names: where the template's one
	// address goes when no variable says.
	Port int
	// Ignore is set for a template its own catalogue does not offer.
	Ignore bool
	Root   *yaml.Node
	// Logos are files to try for its logo, the best first.
	Logos []string
	Logo  string // the one that was taken
	// files are the files a Dokploy blueprint puts next to the stack, by
	// path, with variables already filled in.
	files map[string]string
	// notWaited are the services the source marks as not to be waited for.
	notWaited map[string]bool
	// Connect is set for a template that is of use only to the apps beside
	// it, and so joins the environment's network.
	Connect bool
	// Published says the source published a port of the server, and
	// Addressed that a service of the template has a web address.
	Published, Addressed bool
	// Ports is how many ports of the server the template publishes.
	Ports int
}

// reach settles how a template with no web address is reached, once its
// categories are known: from its environment's network, on the ports of
// the server it publishes, or not at all.
func (t *tmpl) reach() error {
	if t.Addressed {
		return nil
	}
	if !t.Published {
		// It never listened for the outside: a tunnel, a bot, a worker.
		// What it works with is beside it.
		t.Connect = true
		return nil
	}
	for _, c := range t.Categories {
		if c == "database" {
			// A database, a cache, a pooler: the apps of its environment
			// are who it is for.
			t.Connect = true
			return nil
		}
	}
	if t.Ports > 0 {
		return nil
	}
	return fmt.Errorf("it is reached on a port of its own that a stack may not publish (one under 1024, or one that is not named)")
}

// readCoolify reads a Coolify template: a Compose file under a header of
// comments.
func readCoolify(file, svgs string) (*tmpl, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	t := &tmpl{Key: strings.TrimSuffix(filepath.Base(file), ".yaml"), Source: "coolify", files: map[string]string{}}
	lines := strings.Split(string(raw), "\n")
	body := 0
	for i, line := range lines {
		rest, comment := strings.CutPrefix(line, "#")
		if !comment {
			if strings.TrimSpace(line) == "" {
				continue
			}
			body = i
			break
		}
		key, value, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "documentation":
			t.Docs = value
		case "slogan":
			t.About = value
		case "category":
			t.SrcCategory = value
		case "tags":
			for _, tag := range strings.Split(value, ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					t.Tags = append(t.Tags, tag)
				}
			}
		case "logo":
			// Of any kind: what is no SVG is a picture (pictures.go).
			if value != "" {
				t.Logos = append(t.Logos, filepath.Join(svgs, strings.TrimPrefix(value, "svgs/")))
			}
		case "port":
			// "80, 2112": the first is where the address goes.
			first, _, _ := strings.Cut(value, ",")
			t.Port, _ = strconv.Atoi(strings.TrimSpace(first))
		case "ignore":
			t.Ignore = value == "true"
		}
	}
	// An address that carries where a visit came from is not the address.
	if u, err := url.Parse(t.Docs); err == nil && u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if strings.HasPrefix(k, "utm_") {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
		t.Docs = u.String()
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[body:], "\n")), &doc); err != nil || len(doc.Content) == 0 || !isMap(doc.Content[0]) {
		return t, fmt.Errorf("its Compose file could not be read")
	}
	t.Root = expand(doc.Content[0])
	if unknown := regexp.MustCompile(`SERVICE_(SUPABASEANON|SUPABASESERVICE)_`).FindString(string(raw)); unknown != "" {
		return t, fmt.Errorf("it needs a generated value musdash has no equal of: %s", strings.TrimSuffix(unknown, "_"))
	}
	// Coolify's BASE64 is a random string of letters and digits, which is
	// what musdash calls a password; its REALBASE64 is base64. HEX_32 is
	// musdash's plain HEX.
	rename := strings.NewReplacer(
		"SERVICE_BASE64_128_", "SERVICE_PASSWORD_64_",
		"SERVICE_BASE64_64_", "SERVICE_PASSWORD_64_",
		"SERVICE_BASE64_32_", "SERVICE_PASSWORD_",
		"SERVICE_BASE64_", "SERVICE_PASSWORD_",
		"SERVICE_REALBASE64_32_", "SERVICE_BASE64_",
		"SERVICE_REALBASE64_", "SERVICE_BASE64_",
		"SERVICE_HEX_32_", "SERVICE_HEX_",
		"SERVICE_PASSWORD_32_", "SERVICE_PASSWORD_",
	)
	// A renamed variable must not become one the file already has for
	// something else: the two would share a value.
	had := map[string]bool{}
	for _, m := range regexp.MustCompile(`SERVICE_PASSWORD_[A-Z0-9_]*[A-Z0-9]`).FindAllString(string(raw), -1) {
		had[m] = true
	}
	scalars(t.Root, func(n *yaml.Node) {
		if !strings.Contains(n.Value, "SERVICE_") {
			return
		}
		setValue(n, regexp.MustCompile(`SERVICE_(REALBASE64|BASE64|HEX|PASSWORD)_[A-Z0-9_]*[A-Z0-9]`).ReplaceAllStringFunc(n.Value, func(name string) string {
			renamed := rename.Replace(name)
			if renamed != name && strings.HasPrefix(name, "SERVICE_BASE64_") && had[renamed] {
				renamed += "_KEY"
			}
			return renamed
		}))
	})
	return t, nil
}
