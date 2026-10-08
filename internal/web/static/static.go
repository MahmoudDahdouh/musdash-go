// Package static serves the embedded front-end assets.
//
// Each asset URL carries a hash of its content, so responses are cached
// forever and a new build is picked up at once.
package static

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed app.css app.js htmx.min.js htmx-sse.js favicon.svg terminal.js logo-*.svg logo-*.webp
var files embed.FS

type asset struct {
	name string
	mime string
	// A logo is one of several hundred, and a page can show them all. Its
	// body is never kept: it is read once for its hash and let go, and a
	// request is answered from the embedded file, which is part of the
	// program and not of the heap.
	logo bool

	// Read and hashed when first asked for, not when the program starts
	// and not with the others: reading an embedded file copies it onto the
	// heap, the proxy is the same binary and serves none of them, and a
	// script only one page uses should cost nothing until that page is
	// opened.
	load sync.Once
	raw  []byte
	hash string

	once sync.Once
	gz   []byte // built on first request, so an idle server never pays for it
}

func (a *asset) read() {
	a.load.Do(func() {
		raw, err := files.ReadFile(a.name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(raw)
		a.hash = hex.EncodeToString(sum[:6])
		if !a.logo {
			a.raw = raw
		}
	})
}

// assets lists the embedded files on first use. Their content is not read
// here.
var assets = sync.OnceValue(func() map[string]*asset {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err)
	}
	all := make(map[string]*asset, len(entries))
	for _, e := range entries {
		all[e.Name()] = &asset{name: e.Name(), mime: mime.TypeByExtension(path.Ext(e.Name())), logo: strings.HasPrefix(e.Name(), "logo-")}
	}
	return all
})

// URL returns the cache-busting URL of an embedded asset.
func URL(name string) string {
	a, ok := assets()[name]
	if !ok {
		panic("static: unknown asset " + name)
	}
	a.read()
	return "/static/" + name + "?v=" + a.hash
}

// Has says whether there is an embedded asset of this name, for a name that
// comes from data: URL panics for one there is not.
func Has(name string) bool {
	_, ok := assets()[name]
	return ok
}

// Logo returns the asset that holds the logo of this name, and whether
// there is one. A logo is a drawing (logo-<name>.svg) or, where its owner
// has no drawing the dashboard can show, a small picture of it
// (logo-<name>.webp, made by tools/catalog). A name has one of the two.
func Logo(name string) (string, bool) {
	for _, kind := range [...]string{".svg", ".webp"} {
		if file := "logo-" + name + kind; Has(file) {
			return file, true
		}
	}
	return "", false
}

// Handler serves GET /static/{name}.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := assets()[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		a.read()
		h := w.Header()
		h.Set("Content-Type", a.mime)
		if !a.logo {
			h.Set("Vary", "Accept-Encoding")
		}
		if r.URL.Query().Get("v") == a.hash {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		etag := `"` + a.hash + `"`
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if a.logo {
			// Sent as it is: a logo is a few kilobytes, fetched once and
			// kept by the browser, and a gzip writer for each of a page's
			// several hundred would cost more memory than it saves bytes.
			if r.Method == http.MethodHead {
				return
			}
			f, err := files.Open(a.name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			io.Copy(w, f)
			return
		}
		body := a.raw
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			a.once.Do(func() {
				var buf bytes.Buffer
				zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
				zw.Write(a.raw)
				zw.Close()
				a.gz = buf.Bytes()
			})
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
		if r.Method == http.MethodHead {
			return
		}
		w.Write(body)
	})
}
