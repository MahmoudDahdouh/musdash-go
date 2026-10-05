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
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed app.css app.js htmx.min.js htmx-sse.js favicon.svg
var files embed.FS

type asset struct {
	name string
	raw  []byte
	hash string
	mime string

	once sync.Once
	gz   []byte // built on first request, so an idle server never pays for it
}

// assets reads and hashes the embedded files on first use, not when the
// program starts: the proxy is the same binary and never serves them, and
// reading them copies them onto the heap.
var assets = sync.OnceValue(func() map[string]*asset {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err)
	}
	all := make(map[string]*asset, len(entries))
	for _, e := range entries {
		raw, err := files.ReadFile(e.Name())
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(raw)
		all[e.Name()] = &asset{
			name: e.Name(),
			raw:  raw,
			hash: hex.EncodeToString(sum[:6]),
			mime: mime.TypeByExtension(path.Ext(e.Name())),
		}
	}
	return all
})

// URL returns the cache-busting URL of an embedded asset.
func URL(name string) string {
	a, ok := assets()[name]
	if !ok {
		panic("static: unknown asset " + name)
	}
	return "/static/" + name + "?v=" + a.hash
}

// Handler serves GET /static/{name}.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := assets()[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.mime)
		h.Set("Vary", "Accept-Encoding")
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
