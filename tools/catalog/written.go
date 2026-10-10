package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// readWritten reads a template written for musdash (templates/*.yaml): a
// Compose file under the header a file of the catalogue has, already in
// musdash's words (its magic variables, its categories). Neither catalogue
// has the service, so there is nobody's template to convert; it goes through
// the same steps as theirs all the same, which is what holds it to the same
// rules and finds its logo.
func readWritten(file string) (*tmpl, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	t := &tmpl{Key: strings.TrimSuffix(filepath.Base(file), ".yaml"), Source: "musdash", files: map[string]string{}}
	lines := strings.Split(string(raw), "\n")
	body := len(lines)
	for i, line := range lines {
		rest, comment := strings.CutPrefix(line, "#")
		if !comment {
			body = i
			break
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
		case "website":
			t.Website = value
		case "categories":
			for _, c := range strings.Split(value, ",") {
				if c = strings.TrimSpace(c); c != "" {
					t.Categories = append(t.Categories, c)
				}
			}
		default:
			return t, fmt.Errorf("its header has a line this does not read: %s", strings.TrimSpace(key))
		}
	}
	// Nothing fills in what such a header leaves out: there is no source
	// to take it from.
	if !keyRE.MatchString(t.Key) || t.Name == "" || t.About == "" || t.Docs == "" || t.Website == "" {
		return t, fmt.Errorf("its header needs a name, a line about it and both links, and its file a name that can be a key")
	}
	if n := len(t.Categories); n < 1 || n > 3 {
		return t, fmt.Errorf("its header names %d categories, and a template has one to three", n)
	}
	for i, c := range t.Categories {
		if !categoryKeys[c] || slices.Contains(t.Categories[:i], c) {
			return t, fmt.Errorf("its header names %q, which is no category or is there twice", c)
		}
	}
	// A logo no collection has is kept beside the template, under its name.
	for _, kind := range []string{".svg", ".png", ".webp", ".jpg"} {
		if logo := strings.TrimSuffix(file, ".yaml") + kind; exists(logo) {
			t.Logos = append(t.Logos, logo)
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[body:], "\n")), &doc); err != nil || len(doc.Content) == 0 || !isMap(doc.Content[0]) {
		return t, fmt.Errorf("its Compose file could not be read")
	}
	t.Root = expand(doc.Content[0])
	return t, nil
}

func exists(file string) bool {
	_, err := os.Stat(file)
	return err == nil
}
