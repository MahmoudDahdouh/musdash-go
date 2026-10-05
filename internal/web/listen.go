package web

import (
	"net/http"
	"time"
)

// maxHeaderBytes is how much of a request's headers the dashboard reads.
// Go's own limit is a megabyte, held in memory for each connection that
// sends it, and anybody can open connections to the sign-in page. What the
// dashboard is really sent is small: its cookies, a webhook's signature, a
// terminal's upgrade. The proxy has the same limit.
const maxHeaderBytes = 64 << 10

// HTTPServer is the server the dashboard is served with.
func HTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
		// No WriteTimeout: log and event streams stay open.
	}
}
