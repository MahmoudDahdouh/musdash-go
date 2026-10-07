package main

import (
	"os"
	"regexp"
	"strings"
)

// A logo is shown as an <img> under the dashboard's content security
// policy, which drops a style attribute and a <style> element inside the
// image. A file is taken only if it can be written without either, reaches
// for nothing outside itself and is small: internal/web's
// TestEveryOfferHasALogo holds every logo to the same.

const maxLogo = 6 << 10

var (
	xmlNoise  = regexp.MustCompile(`(?s)<\?xml.*?\?>|<!DOCTYPE.*?>|<!--.*?-->|<metadata.*?</metadata>|<title.*?</title>|<desc.*?</desc>|<sodipodi:namedview.*?(/>|</sodipodi:namedview>)`)
	editorAt  = regexp.MustCompile(`\s+(xmlns:(xlink|sodipodi|inkscape|dc|cc|rdf|svg|serif|sketch|xodm)|sodipodi:[a-z-]+|inkscape:[a-z-]+|xml:space|data-name|serif:[a-z-]+|sketch:[a-z-]+|version|enable-background)="[^"]*"`)
	styleElem = regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	classRule = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	styleAttr = regexp.MustCompile(`\sstyle="([^"]*)"`)
	classAttr = regexp.MustCompile(`\sclass="([^"]*)"`)
	spaces    = regexp.MustCompile(`>\s+<`)
	emptyDefs = regexp.MustCompile(`<defs>\s*</defs>|<defs\s*/>`)
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
		if !presentation[prop] || strings.ContainsAny(value, `"<>`) {
			return "", false
		}
		b.WriteString(" " + prop + `="` + value + `"`)
	}
	return b.String(), true
}

// cleanLogo returns an SVG file written the way the dashboard can show it,
// or "" when this one cannot be.
func cleanLogo(file string) string {
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) > 64<<10 {
		return ""
	}
	s := xmlNoise.ReplaceAllString(string(raw), "")
	s = editorAt.ReplaceAllString(s, "")
	classes := map[string]string{}
	ok := true
	s = styleElem.ReplaceAllStringFunc(s, func(el string) string {
		css := styleElem.FindStringSubmatch(el)[1]
		css = strings.NewReplacer("<![CDATA[", "", "]]>", "").Replace(css)
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
		// The style's value is the one that counts where both are given.
		for _, m := range regexp.MustCompile(` ([a-z-]+)="`).FindAllStringSubmatch(add, -1) {
			tag = regexp.MustCompile(`\s`+regexp.QuoteMeta(m[1])+`="[^"]*"`).ReplaceAllString(tag, "")
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
	if !ok || !strings.HasPrefix(s, "<svg") || !strings.HasSuffix(s, "</svg>") || len(s) > maxLogo {
		return ""
	}
	if !strings.Contains(s, "viewBox") && !(regexp.MustCompile(`<svg[^>]*\swidth="`).MatchString(s) && regexp.MustCompile(`<svg[^>]*\sheight="`).MatchString(s)) {
		return ""
	}
	for _, bad := range []string{"style", "class=", "currentColor", "<script", "href", "<image", "<foreignObject", "<text", "@import"} {
		if strings.Contains(s, bad) {
			return ""
		}
	}
	// A handler for an event, and a reference to anything but a part of
	// the drawing itself. Neither does anything in an <img> under the
	// dashboard's policy; neither has any business in a logo.
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(s) || regexp.MustCompile(`(?i)url\(\s*['"]?[^#'"\s]`).MatchString(s) {
		return ""
	}
	return s + "\n"
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
