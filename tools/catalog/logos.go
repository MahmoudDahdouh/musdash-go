package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A logo is shown as an <img> under the dashboard's content security
// policy, which drops a style attribute and a <style> element inside the
// image. A file is taken only if it can be written without either, reaches
// for nothing outside itself and is small: internal/web/static's
// TestEveryLogoCanBeShown holds every logo to the same.
//
// Small is two sizes. Nearly every logo there is fits in smallLogo, and a
// service gets a file of that size wherever one of its files is: the page
// shows several hundred, and all of them are in the binary. maxLogo is for
// the service that has no such file, so that it has a logo at all.
const (
	smallLogo = 6 << 10
	maxLogo   = 20 << 10
)

var (
	xmlNoise  = regexp.MustCompile(`(?s)<\?xml.*?\?>|<!DOCTYPE.*?>|<!--.*?-->|<metadata.*?</metadata>|<title.*?</title>|<desc.*?</desc>|<sodipodi:namedview.*?(/>|</sodipodi:namedview>)`)
	editorAt  = regexp.MustCompile(`\s+(xmlns:(xlink|sodipodi|inkscape|dc|cc|rdf|svg|serif|sketch|xodm)|sodipodi:[a-z-]+|inkscape:[a-z-]+|xml:space|data-name|serif:[a-z-]+|sketch:[a-z-]+|version|enable-background)="[^"]*"`)
	styleElem = regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	classRule = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	styleAttr = regexp.MustCompile(`\sstyle="([^"]*)"`)
	classAttr = regexp.MustCompile(`\sclass="([^"]*)"`)
	spaces    = regexp.MustCompile(`>\s+<`)
	emptyDefs = regexp.MustCompile(`<defs>\s*</defs>|<defs\s*/>`)
	// What a logo says for a dark page: the dashboard has none.
	darkOnly = regexp.MustCompile(`(?s)@media\s*\(\s*prefers-color-scheme\s*:\s*dark\s*\)\s*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}`)
)

// What can be written as an attribute of the element instead of in a style.
var presentation = map[string]bool{
	"fill": true, "fill-opacity": true, "fill-rule": true, "clip-rule": true, "clip-path": true, "stroke": true,
	"stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true,
	"stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true, "opacity": true, "stop-color": true,
	"stop-opacity": true, "display": true, "mask": true, "filter": true, "transform": true, "font-family": true,
	"font-size": true, "font-weight": true, "text-anchor": true, "letter-spacing": true, "paint-order": true,
	"color": true, "overflow": true, "visibility": true, "shape-rendering": true, "vector-effect": true,
}

// What changes nothing in a logo drawn by itself.
var harmless = map[string]bool{
	"enable-background": true, "isolation": true, "mix-blend-mode": true, "white-space": true, "line-height": true,
	"font-variant": true, "font-stretch": true, "font-variation-settings": true, "text-rendering": true,
	"image-rendering": true, "color-interpolation-filters": true, "color-rendering": true, "marker": true,
	"text-indent": true, "text-transform": true, "block-progression": true, "direction": true, "baseline-shift": true,
	"text-decoration": true, "word-spacing": true, "writing-mode": true, "-inkscape-font-specification": true,
	"font-feature-settings": true, "text-align": true, "text-decoration-line": true, "text-decoration-color": true,
	"solid-color": true, "solid-opacity": true, "shape-padding": true, "text-decoration-style": true,
	"text-orientation": true, "dominant-baseline": true, "font-variant-ligatures": true, "font-variant-caps": true,
	"font-variant-numeric": true, "font-variant-east-asian": true, "font-variant-position": true,
	"font-variant-alternates": true, "shape-margin": true, "inline-size": true, "font-style": true, "mask-type": true,
}

// declarations turns "fill:#fff;stroke:none" into attributes. It reports
// false for one that has no attribute to be.
func declarations(css string) (string, bool) {
	var b strings.Builder
	for _, decl := range strings.Split(css, ";") {
		prop, value, ok := strings.Cut(decl, ":")
		prop, value = strings.TrimSpace(prop), strings.TrimSpace(value)
		if !ok || prop == "" {
			continue
		}
		if harmless[prop] || strings.HasPrefix(prop, "-") {
			continue
		}
		// An entity in a value has the semicolon this was split at.
		if !presentation[prop] || strings.ContainsAny(value, `"<>&`) {
			return "", false
		}
		b.WriteString(" " + prop + `="` + value + `"`)
	}
	return b.String(), true
}

// cleanLogo returns an SVG file written the way the dashboard can show it,
// or "" when this one cannot be.
func cleanLogo(file string, limit int) string {
	if s, _ := drawing(file); s != "" && len(s) < limit {
		return s + "\n"
	}
	return ""
}

// smallerLogo is cleanLogo for a file that is too large as it stands: the
// same drawing with its coordinates written to fewer places.
func smallerLogo(file string, limit int) string {
	s, _ := drawing(file)
	if s == "" {
		return ""
	}
	if s = shrink(s, places(s)); len(s) < limit {
		return s + "\n"
	}
	return ""
}

// whyNot says what kept a file that is there from being a logo, for the
// notes of a run.
func whyNot(file string) string {
	s, why := drawing(file)
	if s != "" {
		why = fmt.Sprintf("%d bytes, %d with fewer decimals", len(s), len(shrink(s, places(s))))
	}
	return why
}

// drawing is the file as the dashboard can show it, whatever its size, or
// "" and what it has that the dashboard would not show.
func drawing(file string) (string, string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", "not there"
	}
	if len(raw) > 64<<10 {
		return "", "larger than 64 KB"
	}
	s := xmlNoise.ReplaceAllString(string(raw), "")
	s = editorAt.ReplaceAllString(s, "")
	classes := map[string]string{}
	ok := true
	s = styleElem.ReplaceAllStringFunc(s, func(el string) string {
		css := styleElem.FindStringSubmatch(el)[1]
		css = strings.NewReplacer("<![CDATA[", "", "]]>", "").Replace(css)
		css = darkOnly.ReplaceAllString(css, "")
		rest := classRule.ReplaceAllStringFunc(css, func(rule string) string {
			m := classRule.FindStringSubmatch(rule)
			attrs, fine := declarations(m[2])
			if !fine {
				ok = false
			}
			for _, sel := range strings.Split(m[1], ",") {
				sel = strings.TrimSpace(sel)
				if !regexp.MustCompile(`^\.[A-Za-z0-9_-]+$`).MatchString(sel) {
					ok = false
				}
				classes[sel[min(1, len(sel)):]] += attrs
			}
			return ""
		})
		if strings.TrimSpace(rest) != "" {
			ok = false
		}
		return ""
	})
	// A class's declarations go onto what had the class. An attribute the
	// element has already wins, as it would not have in the stylesheet:
	// such a file is not taken.
	s = regexp.MustCompile(`<[A-Za-z][^<>]*>`).ReplaceAllStringFunc(s, func(tag string) string {
		add := ""
		tag = classAttr.ReplaceAllStringFunc(tag, func(attr string) string {
			for _, name := range strings.Fields(classAttr.FindStringSubmatch(attr)[1]) {
				add += classes[name]
			}
			return ""
		})
		tag = styleAttr.ReplaceAllStringFunc(tag, func(attr string) string {
			attrs, fine := declarations(styleAttr.FindStringSubmatch(attr)[1])
			if !fine {
				ok = false
			}
			add += attrs
			return ""
		})
		if add == "" {
			return tag
		}
		// Two rules for one class, or a class and the style, can say the
		// same thing twice. The later one counts, as in the stylesheet,
		// and is written once: an attribute given twice is no XML, and
		// the browser shows nothing of such a file.
		said, names := map[string]string{}, []string{}
		for _, m := range regexp.MustCompile(` ([a-z-]+)="([^"]*)"`).FindAllStringSubmatch(add, -1) {
			if _, twice := said[m[1]]; !twice {
				names = append(names, m[1])
			}
			said[m[1]] = m[2]
		}
		add = ""
		// The style's value is the one that counts where both are given.
		for _, name := range names {
			add += " " + name + `="` + said[name] + `"`
			tag = regexp.MustCompile(`\s`+regexp.QuoteMeta(name)+`="[^"]*"`).ReplaceAllString(tag, "")
		}
		end := len(tag) - 1
		if strings.HasSuffix(tag, "/>") {
			end--
		}
		return strings.TrimRight(tag[:end], " ") + add + tag[end:]
	})
	s = emptyDefs.ReplaceAllString(s, "")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, "><"))
	s = regexp.MustCompile(`\s*\n\s*`).ReplaceAllString(s, " ")
	if !ok {
		return "", "a style that is no attribute"
	}
	if !strings.HasPrefix(s, "<svg") || !strings.HasSuffix(s, "</svg>") {
		return "", "not an SVG file"
	}
	if !strings.Contains(s, "viewBox") && !(regexp.MustCompile(`<svg[^>]*\swidth="`).MatchString(s) && regexp.MustCompile(`<svg[^>]*\sheight="`).MatchString(s)) {
		return "", "no size"
	}
	for _, bad := range []string{"style", "class=", "currentColor", "<script", "href", "<image", "<foreignObject", "<text", "@import"} {
		if strings.Contains(s, bad) {
			return "", "holds " + bad
		}
	}
	// A handler for an event, and a reference to anything but a part of
	// the drawing itself. Neither does anything in an <img> under the
	// dashboard's policy; neither has any business in a logo.
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(s) || regexp.MustCompile(`(?i)url\(\s*['"]?[^#'"\s]`).MatchString(s) {
		return "", "an event handler, or an address outside the file"
	}
	if why := unseen(s); why != "" {
		return "", why
	}
	return s, ""
}

// What is drawn, and what is only kept to be used by something drawn.
var (
	shapes = map[string]bool{"path": true, "rect": true, "circle": true, "ellipse": true, "polygon": true, "polyline": true, "line": true}
	stored = map[string]bool{"defs": true, "clipPath": true, "mask": true, "symbol": true, "pattern": true, "marker": true, "linearGradient": true, "radialGradient": true}
)

// unseen says why a drawing would be an empty square on the page, or "".
// A file the browser cannot read as XML is one (a prefix whose declaration
// was removed above counts), and so is a logo made for a dark page: every
// shape white, on a card that is white. Coolify's and Dokploy's pages are
// dark, so their folders have a number of those.
func unseen(s string) string {
	type paint struct{ fill, stroke string }
	stack := []paint{{"black", "none"}}
	hidden, seen := 0, false
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "not XML a browser reads"
		}
		switch el := tok.(type) {
		case xml.StartElement:
			// The decoder leaves a prefix nobody declared where the
			// namespace's address would be.
			unbound := func(n xml.Name) bool {
				return n.Space != "" && n.Space != "xmlns" && n.Space != "xml" && !strings.Contains(n.Space, ":")
			}
			if unbound(el.Name) {
				return "not XML a browser reads"
			}
			now := stack[len(stack)-1]
			for i, a := range el.Attr {
				// The decoder does not mind an attribute given twice. A
				// browser does.
				if unbound(a.Name) || slices.ContainsFunc(el.Attr[:i], func(b xml.Attr) bool { return b.Name == a.Name }) {
					return "not XML a browser reads"
				}
				// Chrome reads a colour written without its "#" in an
				// attribute, as it does on an old page: Appsmith's logo
				// in Coolify's folder is white by fill="ffffff".
				if hashless.MatchString(a.Value) {
					a.Value = "#" + a.Value
				}
				switch a.Name.Local {
				case "fill":
					now.fill = a.Value
				case "stroke":
					now.stroke = a.Value
				}
			}
			stack = append(stack, now)
			if stored[el.Name.Local] {
				hidden++
			}
			if hidden == 0 && shapes[el.Name.Local] && (shown(now.fill) || shown(now.stroke)) {
				seen = true
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if stored[el.Name.Local] {
				hidden--
			}
		}
	}
	if !seen {
		return "white on white: a logo for a dark page"
	}
	return ""
}

var hashless = regexp.MustCompile(`^([0-9A-Fa-f]{3}|[0-9A-Fa-f]{6})$`)

var rgbColour = regexp.MustCompile(`^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)`)

// shown reports whether a fill or a stroke can be seen on a white card.
// What it cannot read (a gradient, a colour's name) it takes to be seen.
func shown(colour string) bool {
	colour = strings.ToLower(strings.TrimSpace(colour))
	var r, g, b uint64
	switch {
	case colour == "none" || colour == "transparent":
		return false
	case colour == "white" || colour == "snow" || colour == "ivory" || colour == "ghostwhite" || colour == "whitesmoke":
		return false
	case rgbColour.MatchString(colour):
		m := rgbColour.FindStringSubmatch(colour)
		r, _ = strconv.ParseUint(m[1], 10, 16)
		g, _ = strconv.ParseUint(m[2], 10, 16)
		b, _ = strconv.ParseUint(m[3], 10, 16)
	case regexp.MustCompile(`^#[0-9a-f]{3,8}$`).MatchString(colour):
		hex := colour[1:]
		if len(hex) < 6 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		v, _ := strconv.ParseUint(hex[:6], 16, 32)
		r, g, b = v>>16, v>>8&0xff, v&0xff
	default:
		return true
	}
	return light(r, g, b) < 225
}

// light is how bright a colour is, from 0 to 255.
func light(r, g, b uint64) float64 {
	return 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
}

var (
	pathData  = regexp.MustCompile(`\s(d|points)="[^"]*"`)
	decimal   = regexp.MustCompile(`(\d*)\.(\d+)([eE][-+]?\d+)?`)
	viewBox   = regexp.MustCompile(`<svg[^>]*\sviewBox="\s*[-\d.e]+[\s,]+[-\d.e]+[\s,]+([\d.e]+)[\s,]+([\d.e]+)`)
	svgWidth  = regexp.MustCompile(`<svg[^>]*\swidth="([\d.]+)`)
	svgHeight = regexp.MustCompile(`<svg[^>]*\sheight="([\d.]+)`)
	scaling   = regexp.MustCompile(`(matrix|scale)\(([^)]*)\)`)
)

// places is how many decimals a drawing's coordinates need. A logo is
// shown 48px wide, twice that in device pixels, and a path of relative
// steps adds up what each step lost: a step is kept to a ten-thousandth of
// the drawing's size, finer by the most a transform inside it enlarges.
func places(s string) int {
	size := 0.0
	if m := viewBox.FindStringSubmatch(s); m != nil {
		w, _ := strconv.ParseFloat(m[1], 64)
		h, _ := strconv.ParseFloat(m[2], 64)
		size = max(w, h)
	} else if w, h := svgWidth.FindStringSubmatch(s), svgHeight.FindStringSubmatch(s); w != nil && h != nil {
		a, _ := strconv.ParseFloat(w[1], 64)
		b, _ := strconv.ParseFloat(h[1], 64)
		size = max(a, b)
	}
	if size <= 0 {
		return 6
	}
	enlarged := 1.0
	for _, m := range scaling.FindAllStringSubmatch(s, -1) {
		factors := strings.FieldsFunc(m[2], func(r rune) bool { return r == ',' || r == ' ' })
		if m[1] == "matrix" && len(factors) == 6 {
			factors = factors[:4]
		}
		for _, f := range factors {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				enlarged = max(enlarged, math.Abs(v))
			}
		}
	}
	size /= enlarged
	n := 0
	for step := 1.0; step > size/10000 && n < 6; step /= 10 {
		n++
	}
	return n
}

// shrink writes the coordinates of every path and polygon with at most n
// decimals. Only those: a number in a transform or a gradient is a factor,
// and a factor rounded is a different drawing.
func shrink(s string, n int) string {
	return pathData.ReplaceAllStringFunc(s, func(attr string) string {
		var b strings.Builder
		last := 0
		for _, at := range decimal.FindAllStringSubmatchIndex(attr, -1) {
			whole, frac := attr[at[2]:at[3]], attr[at[4]:at[5]]
			if at[6] >= 0 || len(frac) <= n {
				continue
			}
			kept := []byte(frac[:n])
			if frac[n] >= '5' {
				// Rounded up, unless that would reach the digits before
				// the point: in an arc those can be its two flags and the
				// number run together ("011.5"), where a carry would be
				// read as another flag.
				i := len(kept) - 1
				for ; i >= 0 && kept[i] == '9'; i-- {
				}
				if i >= 0 {
					kept[i]++
					for i++; i < len(kept); i++ {
						kept[i] = '0'
					}
				}
			}
			short := whole
			if rest := strings.TrimRight(string(kept), "0"); rest != "" {
				short += "." + rest
			} else if whole == "" {
				short = "0"
			}
			b.WriteString(attr[last:at[0]])
			// A path leaves out the space between two numbers where the
			// second starts with its point ("1.5.25"). A number that lost
			// its point needs the space back, on whichever side it was.
			if short[0] != '.' && at[0] > 0 && (attr[at[0]-1] >= '0' && attr[at[0]-1] <= '9' || attr[at[0]-1] == '.') {
				b.WriteByte(' ')
			}
			b.WriteString(short)
			if !strings.Contains(short, ".") && at[1] < len(attr) && attr[at[1]] == '.' {
				b.WriteByte(' ')
			}
			last = at[1]
		}
		b.WriteString(attr[last:])
		return b.String()
	})
}

// logoAlias is what a service is called where its logo is, for the ones
// whose key and name say something else: a product's second template has
// the product's logo, and a collection may know a product by its full
// name. A line here is a claim that the two are one product, or that the
// first is published by the second's owner as a part of it.
var logoAlias = map[string][]string{
	"affinepro":                  {"affine"},
	"answer":                     {"apache-answer"},
	"apprise-api":                {"apprise"},
	"calcom":                     {"cal-com"},
	"code-server":                {"coder"},
	"collabora-office":           {"collabora-online"},
	"coralproject":               {"voxmedia-coral"},
	"docker-registry":            {"docker"},
	"docling-serve":              {"docling"},
	"emqx-enterprise":            {"emqx"},
	"evolutiongo":                {"evolution-api"},
	"flatnotes-totp":             {"flatnotes"},
	"gitea-sqlite":               {"gitea"},
	"hermes":                     {"hermes-agent"},
	"joplin-server":              {"joplin"},
	"jupyter-notebook-python":    {"jupyter"},
	"n8n-queue":                  {"n8n"},
	"n8n-runner-postgres-ollama": {"n8n"},
	"newt-pangolin":              {"pangolin"},
	"nexus-arm":                  {"nexus"},
	"otterwiki":                  {"an-otter-wiki", "otter-wiki"},
	"proxyscotch":                {"hoppscotch"},
	"pterodactyl-panel":          {"pterodactyl"},
	"redis-insight":              {"redis"},
	"sure":                       {"sure-finance"},
	"trilium":                    {"trilium-notes"},
}

// logoNames are the file names to look for in an icon collection, the
// likeliest first.
func logoNames(t *tmpl) []string {
	base, _, _ := strings.Cut(t.Key, "-with-")
	seen := map[string]bool{}
	var out []string
	for _, n := range []string{t.Key, base, slug(t.Name), slug(strings.Split(t.Name, " with ")[0]), strings.ReplaceAll(base, "-", "")} {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// logoPicked is where a service's logo was found by hand, in a collection
// of brands of every kind ("svgl:" and "simple:", see collections). There
// a name may be another company's (Hermes, Codex, Buzz), so nothing is
// taken from one by its name: a line here says somebody looked.
var logoPicked = map[string]string{
	"foundryvtt":   "simple:foundryvirtualtabletop",
	"mulesoft-esb": "svgl:mulesoft",
	"powersync":    "svgl:powersync",
	"typesense":    "svgl:typesense",
}

// logoRefused are files that pass every rule here and are still no logo
// for a white card, by what services.SOURCES would call them. Somebody
// looked at these too.
var logoRefused = map[string]bool{
	"coolify:appsmith.svg": true, // the name in white letters; all that shows is the orange stroke under its last one
}

// collections are the checkouts a logo is looked for in besides a
// template's own: the selfh.st icons, Coolify, Dashboard Icons
// (homarr-labs/dashboard-icons), svgl (pheralb/svgl) and Simple Icons
// (simple-icons/simple-icons).
type collections struct{ selfhst, coolify, dashboard, svgl, simple string }

// A logoFile is a file that may be a service's logo, and what
// services.SOURCES says of it when it is. Simple Icons draws every mark as
// one outline in no colour and says the brand's colour beside it: fill is
// that colour.
type logoFile struct{ from, file, fill string }

// simpleColour is the colour Simple Icons gives a brand, or "" when it has
// none for it or one that a white card would not show.
func simpleColour(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "data/simple-icons.json"))
	if err != nil {
		return ""
	}
	var brands []struct{ Title, Hex, Slug string }
	if json.Unmarshal(raw, &brands) != nil {
		return ""
	}
	for _, b := range brands {
		slug := b.Slug
		if slug == "" {
			// The collection's own rule for a file's name.
			slug = regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.NewReplacer("+", "plus", ".", "dot", "&", "and").Replace(strings.ToLower(b.Title)), "")
		}
		if slug != name || !regexp.MustCompile(`^[0-9A-Fa-f]{6}$`).MatchString(b.Hex) {
			continue
		}
		// One outline in one colour has nothing else to be seen by: it
		// is held to more than a logo of several.
		rgb, _ := strconv.ParseUint(b.Hex, 16, 32)
		if light(rgb>>16, rgb>>8&0xff, rgb&0xff) > 200 {
			return ""
		}
		return "#" + b.Hex
	}
	return ""
}

// logoPlaces are the files to try for a template's logo, the likeliest
// first: the selfh.st collection, which most of the logos musdash had were
// from; the template's own; that of the same service in the other
// catalogue (others); Coolify's whole folder, which has logos no template
// of its own names; the Dashboard Icons collection. All of that under the
// service's own names first, and then under its other ones. Last, the one
// picked by hand.
func logoPlaces(t *tmpl, others []*tmpl, c collections) []logoFile {
	var out []logoFile
	named := func(names []string, own bool) {
		for _, n := range names {
			out = append(out, logoFile{from: "selfhst:" + n + ".svg", file: filepath.Join(c.selfhst, "svg", n+".svg")})
		}
		if own {
			for _, f := range t.Logos {
				out = append(out, logoFile{from: t.Source + ":" + filepath.Base(f), file: f})
			}
		}
		for _, o := range others {
			if (same(o.Key) == same(t.Key)) != own {
				continue
			}
			for _, f := range o.Logos {
				from := o.Source + ":" + filepath.Base(f)
				if o.Source == "dokploy" {
					// A blueprint's logo is often logo.svg: of another
					// blueprint, it is named with its folder.
					from = o.Source + ":" + filepath.Base(filepath.Dir(f)) + "/" + filepath.Base(f)
				}
				out = append(out, logoFile{from: from, file: f})
			}
		}
		for _, n := range names {
			out = append(out, logoFile{from: "coolify:" + n + ".svg", file: filepath.Join(c.coolify, "public/svgs", n+".svg")})
		}
		for _, n := range names {
			out = append(out, logoFile{from: "dashboard:" + n + ".svg", file: filepath.Join(c.dashboard, "svg", n+".svg")})
		}
		// Both collections have a second drawing of some logos, for a
		// light page ("-dark" is the colour of the drawing): the one to
		// take where the first is white.
		for _, n := range names {
			out = append(out,
				logoFile{from: "selfhst:" + n + "-dark.svg", file: filepath.Join(c.selfhst, "svg", n+"-dark.svg")},
				logoFile{from: "dashboard:" + n + "-dark.svg", file: filepath.Join(c.dashboard, "svg", n+"-dark.svg")})
		}
	}
	named(logoNames(t), true)
	if alias := logoAlias[t.Key]; len(alias) > 0 {
		named(alias, false)
	}
	switch from, name, _ := strings.Cut(logoPicked[t.Key], ":"); from {
	case "svgl":
		out = append(out, logoFile{from: "svgl:" + name + ".svg", file: filepath.Join(c.svgl, "static/library", name+".svg")})
	case "simple":
		if fill := simpleColour(c.simple, name); fill != "" {
			out = append(out, logoFile{from: "simple:" + name + ".svg", file: filepath.Join(c.simple, "icons", name+".svg"), fill: fill})
		}
	}
	return slices.DeleteFunc(out, func(p logoFile) bool { return logoRefused[p.from] })
}
