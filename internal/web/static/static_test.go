package static

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, name, version string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /static/{name}", Handler())
	r := httptest.NewRequest(http.MethodGet, "/static/"+name+"?v="+version, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// The catalogue has a logo for several hundred services and one page shows
// them all. A logo that was served leaves nothing on the heap but its hash.
func TestALogoIsNotKept(t *testing.T) {
	const name = "logo-docker.svg"
	want, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	url := URL(name)
	version := url[strings.Index(url, "?v=")+3:]
	if len(version) != 12 {
		t.Fatalf("the address %q carries no hash", url)
	}
	w := get(t, name, version, "Accept-Encoding", "gzip")
	if w.Code != http.StatusOK || w.Body.String() != string(want) {
		t.Fatalf("%d, %d bytes, want the file's %d", w.Code, w.Body.Len(), len(want))
	}
	h := w.Header()
	if h.Get("Content-Type") != "image/svg+xml" || h.Get("Content-Encoding") != "" || !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Errorf("headers: %v", h)
	}
	if again := get(t, name, version, "If-None-Match", h.Get("ETag")); again.Code != http.StatusNotModified {
		t.Errorf("a logo the browser has: %d", again.Code)
	}
	for name, a := range assets() {
		a.read()
		if a.logo != strings.HasPrefix(name, "logo-") {
			t.Errorf("%s: logo=%v", name, a.logo)
		}
		if a.logo && (a.raw != nil || a.gz != nil || a.hash == "") {
			t.Errorf("%s is kept in memory (%d bytes), or has no hash", name, len(a.raw)+len(a.gz))
		}
	}
}

// Everything else is kept once read, and compressed for a browser that
// asks.
func TestAScriptIsCompressedOnce(t *testing.T) {
	url := URL("app.js")
	w := get(t, "app.js", url[strings.Index(url, "?v=")+3:], "Accept-Encoding", "gzip")
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if a := assets()["app.js"]; a.raw == nil || a.gz == nil || len(a.gz) >= len(a.raw) {
		t.Errorf("app.js: %d bytes, %d compressed", len(a.raw), len(a.gz))
	}
}

// A logo is shown as an image under the dashboard's content security
// policy, which drops a style inside it, and reaches for no other address.
// There are several hundred, so each is small.
var (
	handler   = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	elsewhere = regexp.MustCompile(`(?i)url\(\s*['"]?[^#'"\s]`)
)

func TestEveryLogoCanBeShown(t *testing.T) {
	logos := 0
	for name, a := range assets() {
		if !a.logo {
			continue
		}
		logos++
		raw, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if a.mime != "image/svg+xml" || !strings.HasPrefix(body, "<svg") && !strings.HasPrefix(body, "<?xml") {
			t.Errorf("%s is not an SVG image", name)
		}
		for _, bad := range []string{"style", "class=", "currentColor", "<script", "href", "<image", "<foreignObject"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s holds %q", name, bad)
			}
		}
		if handler.MatchString(body) || elsewhere.MatchString(body) {
			t.Errorf("%s has an event handler, or refers to something outside itself", name)
		}
		if len(body) > 6<<10 {
			t.Errorf("%s is %d bytes: find a simpler drawing", name, len(body))
		}
	}
	if logos < 25 {
		t.Errorf("%d logos", logos)
	}
}

// app.js is one function, and a name declared twice in it is a syntax
// error that stops the whole script: no dialog opens, no menu, no filter.
// Nothing here runs JavaScript, so the one mistake that costs everything is
// looked for in the text.
func TestScriptsDeclareEachNameOnce(t *testing.T) {
	declared := regexp.MustCompile(`(?m)^  (?:const|let|function) ([A-Za-z_$][A-Za-z0-9_$]*)`)
	for _, name := range []string{"app.js", "terminal.js"} {
		raw, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, m := range declared.FindAllStringSubmatch(string(raw), -1) {
			if seen[m[1]] {
				t.Errorf("%s declares %s twice at its top level", name, m[1])
			}
			seen[m[1]] = true
		}
		if name == "app.js" && len(seen) < 10 {
			t.Errorf("%s: only %d declarations were found; has its indentation changed?", name, len(seen))
		}
	}
}
