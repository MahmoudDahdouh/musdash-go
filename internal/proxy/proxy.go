// Package proxy is the edge proxy: it terminates HTTP(S) for every deployed
// app and forwards each request to the container that serves its host.
//
// It runs as its own process so restarting or upgrading the control plane
// never drops app traffic. It holds no state beyond the route table it loads
// from routes.json; the control plane rewrites that file and sends SIGHUP.
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Options configures the proxy process.
type Options struct {
	HTTPAddr   string // plain HTTP listener, ":80" in production
	HTTPSAddr  string // TLS listener, ":443" in production; "" turns HTTPS off
	RoutesPath string // routes.json
	PIDPath    string // where the control plane finds the process to signal
	CertDir    string // certificate cache
	Log        *slog.Logger
}

// Proxy routes requests by host name.
type Proxy struct {
	table  atomic.Pointer[Table]
	rp     *httputil.ReverseProxy
	log    *slog.Logger
	routes string
	// loaded identifies the routes file that was last read, so the poll can
	// tell when it changed.
	loaded atomic.Value // fileStamp
	// https reports whether a TLS listener exists. Without one, hosts that
	// ask for TLS are served over HTTP rather than redirected nowhere.
	https bool
	// auth checks the passwords of guarded routes.
	auth *gate
}

// upstream is where one request goes: the container's address and, when
// the route's path was taken off the request, that path.
type upstream struct {
	target string
	prefix string
}

type upstreamKey struct{}

// New returns a proxy serving the routes in routesPath.
func New(routesPath string, https bool, logger *slog.Logger) (*Proxy, error) {
	p := &Proxy{log: logger, routes: routesPath, https: https, auth: newGate()}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Rewrite starts from a request with every client-supplied
			// X-Forwarded-* and Forwarded header removed, so the values set
			// here cannot be spoofed.
			up := pr.In.Context().Value(upstreamKey{}).(*upstream)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = up.target
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			// Not one of the headers Rewrite starts without, so it is set
			// or removed here: an app must not read a client's value.
			if up.prefix != "" {
				pr.Out.Header.Set("X-Forwarded-Prefix", up.prefix)
			} else {
				pr.Out.Header.Del("X-Forwarded-Prefix")
			}
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       60 * time.Second,
			ExpectContinueTimeout: time.Second,
			// Pass the app's own encoding through untouched.
			DisableCompression: true,
		},
		BufferPool:   &bufferPool{},
		ErrorHandler: p.upstreamError,
		ErrorLog:     log.New(io.Discard, "", 0),
	}
	return p, nil
}

// Reload reads routes.json again. On any error the current table stays in
// place, so a half-written or invalid file never takes sites down.
func (p *Proxy) Reload() error {
	// Stamp first: if the file changes while it is being read, the next poll
	// sees a different stamp and reads it again.
	stamp := stampOf(p.routes)
	t, err := Load(p.routes)
	if err != nil {
		return err
	}
	p.table.Store(t)
	p.loaded.Store(stamp)
	return nil
}

// fileStamp identifies one version of the routes file.
type fileStamp struct {
	modified time.Time
	size     int64
	exists   bool
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modified: info.ModTime(), size: info.Size(), exists: true}
}

// Changed reports whether the routes file differs from the one in use.
func (p *Proxy) Changed() bool {
	last, _ := p.loaded.Load().(fileStamp)
	return stampOf(p.routes) != last
}

// pollEvery is how often the proxy checks the routes file on its own. The
// control plane also sends SIGHUP for an immediate reload; the poll makes
// sure a missed or refused signal delays a route change by seconds rather
// than leaving it unapplied.
const pollEvery = 3 * time.Second

// Table returns the route table in use.
func (p *Proxy) Table() *Table { return p.table.Load() }

// HTTP is the handler for the plain HTTP listener: it redirects hosts that
// have HTTPS and serves the rest directly.
func (p *Proxy) HTTP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := p.route(w, r)
		if !ok {
			return
		}
		if rt.TLS && p.https {
			// Go straight to the final host when this one only redirects.
			host := rt.Host
			if rt.RedirectTo != "" {
				host = rt.RedirectTo
			}
			http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		p.serve(w, r, rt, "http")
	})
}

// HTTPS is the handler for the TLS listener.
func (p *Proxy) HTTPS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := p.route(w, r)
		if !ok {
			return
		}
		p.serve(w, r, rt, "https")
	})
}

// route finds the route for a request, and answers the request itself when
// there is none.
//
// On a host where the path decides which route serves, or where a password
// guards one, the path must be in its simplest form: a client that sends
// "/public/../admin" or "//admin" is sent to "/admin" first. What is then
// matched here is what the app receives, so a path cannot be one thing to
// the proxy and another to the app behind it.
func (p *Proxy) route(w http.ResponseWriter, r *http.Request) (Route, bool) {
	t := p.Table()
	routed, _, strict := t.Host(r.Host)
	if !routed {
		noRoute(w)
		return Route{}, false
	}
	if strict {
		if plain := Plain(r.URL.Path); plain != r.URL.Path {
			// Built by url.URL so that nothing in the path (a backslash,
			// say) can turn the redirect into one to another site.
			to := url.URL{Path: plain, RawQuery: r.URL.RawQuery}
			http.Redirect(w, r, to.String(), http.StatusPermanentRedirect)
			return Route{}, false
		}
	}
	rt, ok := t.Lookup(r.Host, r.URL.Path)
	if !ok {
		noRoute(w)
	}
	return rt, ok
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request, rt Route, scheme string) {
	if rt.RedirectTo != "" {
		http.Redirect(w, r, scheme+"://"+rt.RedirectTo+r.URL.RequestURI(), http.StatusPermanentRedirect)
		return
	}
	if rt.AuthUser != "" {
		switch p.auth.check(r, rt) {
		case authOK:
			// The password is the proxy's business; the app does not get it.
			r.Header.Del("Authorization")
		case authBusy:
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Too many sign-in attempts at once. Try again in a moment.", http.StatusServiceUnavailable)
			return
		default:
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted", charset="UTF-8"`)
			http.Error(w, "A user name and password are needed for this address.", http.StatusUnauthorized)
			return
		}
	}
	up := &upstream{target: rt.Target}
	out := r.WithContext(context.WithValue(r.Context(), upstreamKey{}, up))
	if rt.StripPrefix && rt.Path != "" {
		up.prefix = rt.Path
		u := *r.URL
		u.Path = strings.TrimPrefix(u.Path, rt.Path)
		if u.Path == "" {
			u.Path = "/"
		}
		// The client's own encoding of the rest is kept when the prefix can
		// be taken off it as it stands; otherwise the path is encoded anew.
		if rest, ok := strings.CutPrefix(u.RawPath, rt.Path); ok && strings.HasPrefix(rest, "/") {
			u.RawPath = rest
		} else {
			u.RawPath = ""
		}
		out.URL = &u
	}
	p.rp.ServeHTTP(w, out)
}

// hostPolicy lets certificates be requested only for routed hosts that asked
// for TLS, so nobody can make the proxy request certificates for other names.
func (p *Proxy) hostPolicy(_ context.Context, host string) error {
	if _, tls, _ := p.Table().Host(host); tls {
		return nil
	}
	return errors.New("proxy: no TLS route for host " + strconv.Quote(host))
}

// upstreamError answers when the app's container cannot be reached.
func (p *Proxy) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client went away; there is nobody to answer
	}
	p.log.Debug("upstream error", "host", r.Host, "err", err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusBadGateway)
	io.WriteString(w, "The app on this address is not responding. It may be starting or redeploying.\n")
}

// noRoute answers a request for a host nothing is deployed on.
func noRoute(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, "Nothing is deployed on this address.\n")
}

// bufferPool recycles the copy buffers of proxied responses.
type bufferPool struct{ pool sync.Pool }

func (b *bufferPool) Get() []byte {
	if v := b.pool.Get(); v != nil {
		return *(v.(*[]byte))
	}
	return make([]byte, 32<<10)
}

func (b *bufferPool) Put(buf []byte) { b.pool.Put(&buf) }

// Run serves until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	p, err := New(o.RoutesPath, o.HTTPSAddr != "", o.Log)
	if err != nil {
		return err
	}

	// Handshake failures from scanners are not worth a log line each.
	quiet := log.New(io.Discard, "", 0)
	newServer := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          quiet,
		}
	}

	httpHandler := p.HTTP()
	var servers []*http.Server
	errc := make(chan error, 2)

	if o.HTTPSAddr != "" {
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(o.CertDir),
			HostPolicy: p.hostPolicy,
			Email:      p.Table().Email,
		}
		// Port 80 answers ACME HTTP-01 challenges before anything else.
		httpHandler = manager.HTTPHandler(httpHandler)
		tlsLn, err := net.Listen("tcp", o.HTTPSAddr)
		if err != nil {
			return err
		}
		cfg := manager.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		srv := newServer(p.HTTPS())
		servers = append(servers, srv)
		go func() { errc <- srv.Serve(tls.NewListener(tlsLn, cfg)) }()
		o.Log.Info("proxy listening", "https", tlsLn.Addr().String())
	}

	ln, err := net.Listen("tcp", o.HTTPAddr)
	if err != nil {
		return err
	}
	srv := newServer(httpHandler)
	servers = append(servers, srv)
	go func() { errc <- srv.Serve(ln) }()
	o.Log.Info("proxy listening", "http", ln.Addr().String(), "routes", p.Table().Len())

	if o.PIDPath != "" {
		// Written whole and renamed into place, so the control plane never
		// reads half a number.
		tmp := o.PIDPath + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, o.PIDPath); err != nil {
			return err
		}
		defer os.Remove(o.PIDPath)
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()

	reload := func() {
		if err := p.Reload(); err != nil {
			o.Log.Error("reload failed; keeping the current routes", "err", err)
		} else {
			o.Log.Info("routes reloaded", "routes", p.Table().Len())
		}
	}
	for {
		select {
		case <-hup:
			reload()
		case <-poll.C:
			if p.Changed() {
				reload()
			}
		case err := <-errc:
			return err
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, s := range servers {
				if err := s.Shutdown(shut); err != nil && !errors.Is(err, context.DeadlineExceeded) {
					return err
				}
			}
			return nil
		}
	}
}
