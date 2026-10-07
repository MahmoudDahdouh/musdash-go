// Command catalog fills musdash's service catalogue from the two catalogues
// people know: Coolify's templates and Dokploy's blueprints.
//
//	go run . -coolify ~/src/coolify -dokploy ~/src/dokploy-templates \
//	    -icons ~/src/selfhst-icons -repo ../..
//
// It writes internal/catalog/services/<key>.yaml for every template it can
// turn into one musdash's rules accept, internal/web/static/logo-<key>.svg
// where there is a logo the dashboard can show, and the lists of what it
// did (internal/catalog/services.SOURCES, docs/catalogue-left-out.md).
// Templates written for musdash (no "source" line in their header) are
// never touched, and nothing of this program is part of musdash: it is a
// module of its own because it needs a YAML parser, which musdash does
// without.
//
// With CATALOG_NOTES=1 it says which service it made wait for which job
// (see oneShots), the one change it makes that is worth reading through.
//
// A template that converts is not yet one that passes: the test
// TestCatalogueLoadsInTheSandbox (internal/compose) loads every template the
// way a deployment does. What it refuses is written to rejected.txt, by
// hand or from the test's output, and left out on the next run.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type leftOut struct{ source, key, reason string }

// engines are the database engines musdash runs itself
// (internal/catalog/databases.go), under the names the two catalogues have
// them. A service template of one would be the same thing offered twice,
// the second time without backups.
var engines = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true, "mongodb": true, "mongo": true,
	"redis": true, "keydb": true, "dragonfly": true, "dragonflydb": true, "clickhouse": true,
}

func main() {
	coolify := flag.String("coolify", "", "a checkout of github.com/coollabsio/coolify")
	dokployDir := flag.String("dokploy", "", "a checkout of github.com/Dokploy/templates")
	icons := flag.String("icons", "", "a checkout of github.com/selfhst/icons")
	repo := flag.String("repo", "../..", "the musdash checkout to write into")
	rejectedFile := flag.String("rejected", "rejected.txt", "the list of templates the sandbox test refused")
	flag.Parse()
	if *coolify == "" || *dokployDir == "" || *icons == "" {
		flag.Usage()
		os.Exit(2)
	}
	services := filepath.Join(*repo, "internal/catalog/services")
	static := filepath.Join(*repo, "internal/web/static")

	// What is there: the templates written for musdash stay, the imported
	// ones are made again.
	own := map[string]bool{}
	var imported []string // what an earlier run wrote: removed once this one has its own to write
	existing, _ := filepath.Glob(filepath.Join(services, "*.yaml"))
	for _, file := range existing {
		raw, _ := os.ReadFile(file)
		if regexp.MustCompile(`(?m)^# source: `).Match(raw) {
			imported = append(imported, file)
			continue
		}
		key := strings.TrimSuffix(filepath.Base(file), ".yaml")
		own[same(key)] = true
		if m := regexp.MustCompile(`(?m)^# name: (.*)$`).FindSubmatch(raw); m != nil {
			own[same(string(m[1]))] = true
		}
	}
	// A logo is this program's to replace only if it put it there. The
	// others are musdash's own (the database engines', the notification
	// channels'), and a template of the same name is shown with that one.
	manifest := filepath.Join(*repo, "internal/catalog/services.SOURCES")
	if raw, err := os.ReadFile(manifest); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) >= 3 && strings.Contains(f[2], ":") && !strings.HasPrefix(line, "#") {
				imported = append(imported, filepath.Join(static, "logo-"+f[0]+".svg"))
			}
		}
	}
	// Without the list of what the sandbox refused, every refused template
	// would be written again: no list is an error, not an empty list.
	rejected := map[string]string{}
	raw, err := os.ReadFile(*rejectedFile)
	if err != nil {
		fatal(fmt.Errorf("%w (run this from tools/catalog, or name the file with -rejected)", err))
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if id, reason, ok := strings.Cut(line, "\t"); ok && !strings.HasPrefix(line, "#") {
			rejected[strings.TrimSpace(id)] = strings.TrimSpace(reason)
		}
	}

	type candidate struct {
		t   *tmpl
		err error
	}
	byKey := map[string][]candidate{}
	var order []string
	add := func(t *tmpl, err error) {
		k := same(t.Key)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], candidate{t, err})
	}

	coolifyFiles, _ := filepath.Glob(filepath.Join(*coolify, "templates/compose/*.yaml"))
	sort.Strings(coolifyFiles)
	for _, file := range coolifyFiles {
		t, err := readCoolify(file, filepath.Join(*coolify, "public/svgs"))
		if t == nil {
			fmt.Fprintln(os.Stderr, file, err)
			continue
		}
		if err == nil && t.Ignore {
			err = fmt.Errorf("Coolify does not offer it itself (its template is marked to be ignored)")
		}
		add(t, err)
	}
	dokployNames := map[string]string{}
	blueprints, _ := filepath.Glob(filepath.Join(*dokployDir, "blueprints/*/meta.json"))
	sort.Strings(blueprints)
	for _, meta := range blueprints {
		t, err := readDokploy(filepath.Dir(meta))
		if t == nil {
			fmt.Fprintln(os.Stderr, meta, err)
			continue
		}
		dokployNames[norm(t.Key)] = t.Name
		add(t, err)
	}
	names := readNames(*icons, dokployNames)

	var out []leftOut
	var taken, winners []*tmpl
	byName := map[string]string{}
	sort.Strings(order)
	for _, k := range order {
		if own[k] {
			continue // musdash has its own template for this
		}
		if engines[k] {
			for _, c := range byKey[k] {
				out = append(out, leftOut{c.t.Source, c.t.Key, "musdash runs it as a database, with backups and a port of its own: see Databases"})
			}
			continue
		}
		var winner *tmpl
		var reasons []leftOut
		for _, c := range byKey[k] {
			t, err := c.t, c.err
			if err == nil {
				if reason, no := rejected[t.Source+"/"+t.Key]; no {
					err = fmt.Errorf("%s", reason)
				}
			}
			if err == nil {
				err = t.convert()
			}
			if err == nil {
				err = t.describe(names)
			}
			if err == nil {
				err = t.reach()
			}
			// What a person may paste as a Compose file is the measure
			// (maxComposeBytes in internal/web): a service made from a
			// larger template could not have its file saved again.
			if err == nil && len(t.render()) > 128<<10 {
				err = fmt.Errorf("with the files it carries it is larger than a Compose file may be (128 KB)")
			}
			if err != nil {
				reasons = append(reasons, leftOut{t.Source, t.Key, err.Error()})
				continue
			}
			winner = t
			break
		}
		if winner == nil {
			out = append(out, reasons...)
			continue
		}
		if own[same(winner.Name)] {
			continue
		}
		winners = append(winners, winner)
	}
	// Two templates of one name could not be told apart on the page.
	// Coolify's is the one that stays, as everywhere.
	sort.SliceStable(winners, func(i, j int) bool { return winners[i].Source < winners[j].Source })
	for _, winner := range winners {
		if other, dup := byName[strings.ToLower(winner.Name)]; dup {
			out = append(out, leftOut{winner.Source, winner.Key, "the catalogue has it as " + other})
			continue
		}
		byName[strings.ToLower(winner.Name)] = winner.Key
		taken = append(taken, winner)
	}
	sort.Slice(taken, func(i, j int) bool { return taken[i].Key < taken[j].Key })

	// Everything is converted: only now is the earlier run's output
	// removed, so that a run that fails leaves the catalogue as it was.
	for _, file := range imported {
		os.Remove(file)
	}

	var sources bytes.Buffer
	sources.WriteString("# Written by tools/catalog. One line for each imported template:\n# its key, whose template it was made from, and where its logo is from\n# (\"-\" for none, \"musdash\" for one musdash had already). See\n# services.LICENSE and ../web/static/logos.LICENSE.\n")
	logos := 0
	for _, t := range taken {
		logo := "-"
		var tried []string
		for _, n := range logoNames(t) {
			tried = append(tried, filepath.Join(*icons, "svg", n+".svg"))
		}
		tried = append(tried, t.Logos...)
		if _, err := os.Stat(filepath.Join(static, "logo-"+t.Key+".svg")); err == nil {
			logo, tried = "musdash", nil
			logos++
		}
		for i, file := range tried {
			if svg := cleanLogo(file); svg != "" {
				if err := os.WriteFile(filepath.Join(static, "logo-"+t.Key+".svg"), []byte(svg), 0o644); err != nil {
					fatal(err)
				}
				logo = "selfhst:" + filepath.Base(file)
				if i >= len(tried)-len(t.Logos) {
					logo = t.Source + ":" + filepath.Base(file)
				}
				logos++
				break
			}
		}
		if err := os.WriteFile(filepath.Join(services, t.Key+".yaml"), t.render(), 0o644); err != nil {
			fatal(err)
		}
		fmt.Fprintf(&sources, "%s %s %s\n", t.Key, t.Source, logo)
	}
	if err := os.WriteFile(manifest, sources.Bytes(), 0o644); err != nil {
		fatal(err)
	}

	var doc bytes.Buffer
	doc.WriteString("# Services that are not in the catalogue\n\n")
	doc.WriteString("Written by `tools/catalog`. The catalogue is filled from Coolify's templates and\nDokploy's blueprints; these are the ones that could not be turned into a\ntemplate musdash's rules accept, each with the reason. A service listed here\ncan still be run from its own Compose file, changed by hand.\n\n")
	sort.Slice(out, func(i, j int) bool {
		if out[i].key != out[j].key {
			return out[i].key < out[j].key
		}
		return out[i].source < out[j].source
	})
	fmt.Fprintf(&doc, "| Service | From | Why it is left out |\n|---|---|---|\n")
	for _, o := range out {
		fmt.Fprintf(&doc, "| %s | %s | %s |\n", o.key, o.source, strings.ReplaceAll(firstLine(o.reason), "|", "\\|"))
	}
	if err := os.WriteFile(filepath.Join(*repo, "docs/catalogue-left-out.md"), doc.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	bySource := map[string]int{}
	for _, t := range taken {
		bySource[t.Source]++
	}
	leftKeys := map[string]bool{}
	for _, o := range out {
		leftKeys[same(o.key)] = true
	}
	fmt.Printf("%d templates written (%d from Coolify, %d from Dokploy), %d with a logo; %d services left out\n",
		len(taken), bySource["coolify"], bySource["dokploy"], logos, len(leftKeys))
	uncategorised := 0
	for _, t := range taken {
		if len(t.Categories) == 0 {
			uncategorised++
			fmt.Printf("no category: %s (%s) [%s] %v\n", t.Key, t.Source, t.SrcCategory, t.Tags)
		}
	}
	if uncategorised > 0 {
		os.Exit(1)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

var keyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// describe fills in what the catalogue says about a template.
func (t *tmpl) describe(names nameBook) error {
	if !keyRE.MatchString(t.Key) {
		t.Key = slug(t.Key)
	}
	if t.Source == "coolify" || t.Name == "" {
		t.Name = names.coolifyName(t.Key)
	}
	if n, ok := nameOverrides[t.Key]; ok {
		t.Name = n
	}
	t.About = about(t.About)
	if t.About == "" {
		t.About = aboutOverrides[t.Key]
	}
	if t.Docs == "" && t.Website == "" {
		t.Docs = docsOverrides[t.Key]
	}
	if t.About == "" {
		return fmt.Errorf("its catalogue says nothing about it")
	}
	https := func(u string) string {
		u = strings.TrimSpace(u)
		if rest, ok := strings.CutPrefix(u, "http://"); ok {
			u = "https://" + rest
		}
		if !strings.HasPrefix(u, "https://") || strings.ContainsAny(u, " \"'<>") {
			return ""
		}
		return u
	}
	t.Docs, t.Website = https(t.Docs), https(t.Website)
	if t.Docs == t.Website {
		t.Website = ""
	}
	if t.Docs == "" && t.Website == "" {
		return fmt.Errorf("its catalogue has no address to read about it at")
	}
	categorise(t)
	return nil
}

// render writes the template as a file of musdash's catalogue.
func (t *tmpl) render() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# name: %s\n# about: %s\n", t.Name, t.About)
	if t.Docs != "" {
		fmt.Fprintf(&b, "# docs: %s\n", t.Docs)
	}
	if t.Website != "" {
		fmt.Fprintf(&b, "# website: %s\n", t.Website)
	}
	fmt.Fprintf(&b, "# categories: %s\n# source: %s\n", strings.Join(t.Categories, ", "), t.Source)
	if t.Connect {
		b.WriteString("# connect: true\n")
	}
	// "services" first, as a Compose file is read.
	first := []string{"services", "volumes", "networks", "configs", "secrets"}
	ordered := newMap()
	for _, key := range first {
		if v := mapGet(t.Root, key); v != nil {
			ordered.Content = append(ordered.Content, str(key), v)
		}
	}
	for _, key := range mapKeys(t.Root) {
		if !slices.Contains(first, key) {
			ordered.Content = append(ordered.Content, str(key), mapGet(t.Root, key))
		}
	}
	stripComments(ordered)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(ordered); err != nil {
		fatal(fmt.Errorf("%s: %w", t.Key, err))
	}
	enc.Close()
	return b.Bytes()
}

// stripComments removes the source's comments: they speak of its platform
// ("change this in Coolify's UI"), and a header line of musdash's must be
// the only comment a template starts with.
func stripComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}
