package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// logLines collects what was logged, one line a record.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) Enabled(context.Context, slog.Level) bool { return true }
func (l *logLines) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logLines) WithGroup(string) slog.Handler            { return l }
func (l *logLines) Handle(_ context.Context, r slog.Record) error {
	line := r.Level.String() + " " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		line += " " + a.Key + "=" + a.Value.String()
		return true
	})
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	return nil
}

// with returns the lines that contain a text.
func (l *logLines) with(text string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.Contains(line, text) {
			out = append(out, line)
		}
	}
	return out
}

// manager is a certificate manager scripted by a test.
type manager struct {
	mu    sync.Mutex
	calls []string
	// inside is how many callers are in the manager now, most the most
	// there ever were.
	inside, most int
	// answer decides what the nth call for a host returns; it may wait.
	answer func(n int, hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

func (m *manager) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.Lock()
	m.calls = append(m.calls, hello.ServerName)
	n := len(m.calls)
	m.inside++
	m.most = max(m.most, m.inside)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.inside--
		m.mu.Unlock()
	}()
	return m.answer(n, hello)
}

func (m *manager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// guarded is a certs in front of a scripted manager, with shop.example.com
// and blog.example.com routed, and the times the proxy runs with. The tests
// that wait run in a synctest bubble, where those seconds cost nothing.
func guarded(m *manager) (*certs, *logLines) {
	log := &logLines{}
	c := newCerts(m.get, func(host string) bool { return host == "shop.example.com" || host == "blog.example.com" }, slog.New(log), 8*time.Second)
	return c, log
}

func hello(host string) *tls.ClientHelloInfo { return &tls.ClientHelloInfo{ServerName: host} }

func certFor(left time.Duration) *tls.Certificate {
	return &tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(left)}}
}

func (c *certs) inFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.flights)
}

// An order that the certificate authority refuses was told to nobody: the
// visitor's browser waited, and the journal was empty.
func TestAFailedOrderIsLoggedOnceByWhoeverMadeIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		m := &manager{answer: func(n int, _ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if n == 1 {
				<-release
				return nil, errors.New("acme: urn:ietf:params:acme:error:rateLimited: too many certificates")
			}
			// What the manager answers everybody for a minute after an
			// order failed.
			return nil, errors.New("acme/autocert: missing certificate")
		}}
		c, log := guarded(m)

		errs := make(chan error, 3)
		for range 3 {
			// In another spelling: one host is one flight.
			go func() { _, err := c.GetCertificate(hello("Shop.Example.com.")); errs <- err }()
		}
		// Every goroutine is waiting now: one in the manager, two for it.
		synctest.Wait()
		if n := m.count(); n != 1 {
			t.Fatalf("%d callers are inside the manager for one host, want one", n)
		}
		close(release)
		for range 3 {
			if err := <-errs; err == nil {
				t.Fatal("a handshake got a certificate from an order that failed")
			}
		}
		synctest.Wait()
		// The two that waited asked for themselves, one after the other.
		if n := m.count(); n != 3 || m.most != 1 {
			t.Fatalf("%d calls into the manager, %d at once", n, m.most)
		}
		failed := log.with("no certificate could be obtained")
		if len(failed) != 1 || !strings.Contains(failed[0], "rateLimited") || !strings.Contains(failed[0], "host=shop.example.com") || !strings.HasPrefix(failed[0], "WARN") {
			t.Fatalf("logged about the order: %q", failed)
		}
		if len(log.lines) != 1 {
			t.Fatalf("the manager's answer for the minute after was logged: %q", log.lines)
		}
	})
}

// An error from before any order (a certificate directory the proxy cannot
// read) comes back for every handshake, and the manager has no quiet minute
// for it.
func TestAFailureThatComesBackIsLoggedOnceAMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &manager{answer: func(int, *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return nil, errors.New("open certs/shop.example.com: permission denied")
		}}
		c, log := guarded(m)
		visit := func() {
			for range 50 {
				if _, err := c.GetCertificate(hello("shop.example.com")); err == nil {
					t.Fatal("a certificate from a manager that has none")
				}
			}
			synctest.Wait()
		}
		visit()
		if got := log.with("permission denied"); len(got) != 1 {
			t.Fatalf("fifty handshakes logged %d lines", len(got))
		}
		time.Sleep(failEvery)
		visit()
		if got := log.with("permission denied"); len(got) != 2 {
			t.Fatalf("a minute later: %d lines", len(got))
		}
		// Another host's failure is its own.
		c.GetCertificate(hello("blog.example.com"))
		synctest.Wait()
		if got := log.with("host=blog.example.com"); len(got) != 1 {
			t.Fatalf("another host: %q", log.lines)
		}
	})
}

// Names that nothing is routed on are what scanners ask for.
func TestNothingIsLoggedForAHostThatIsNotOurs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &manager{answer: func(int, *tls.ClientHelloInfo) (*tls.Certificate, error) {
			time.Sleep(3 * time.Second)
			return nil, errors.New(`proxy: no TLS route for host "scanner.example.net"`)
		}}
		c, log := guarded(m)
		if _, err := c.GetCertificate(hello("scanner.example.net")); err == nil {
			t.Fatal("a certificate for a host that is not routed")
		}
		if _, err := c.GetCertificate(hello("")); err == nil {
			t.Fatal("a certificate for no host")
		}
		synctest.Wait()
		if len(log.lines) != 0 {
			t.Fatalf("logged: %q", log.lines)
		}
	})
}

func TestAnOrderThatSucceedsIsLoggedAndAStoredCertificateIsNot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &manager{answer: func(n int, _ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if n == 1 {
				time.Sleep(3 * time.Second) // an order
			} else {
				time.Sleep(5 * time.Millisecond) // a read from the disk
			}
			return certFor(90 * 24 * time.Hour), nil
		}}
		c, log := guarded(m)
		for range 3 {
			if cert, err := c.GetCertificate(hello("shop.example.com")); err != nil || cert == nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		if got := log.with("certificate obtained"); len(got) != 1 || !strings.HasPrefix(got[0], "INFO") || !strings.Contains(got[0], "host=shop.example.com") || !strings.Contains(got[0], "took=3s") {
			t.Fatalf("logged: %q", log.lines)
		}
		if len(log.lines) != 1 {
			t.Fatalf("logged: %q", log.lines)
		}
	})
}

// The visitor is turned away after the wait; the order goes on and is
// reported when it ends, and the next visitor is served.
func TestAHandshakeDoesNotWaitForAnOrderThatTakesLong(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		m := &manager{answer: func(n int, h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if n == 1 {
				<-release
			}
			return certFor(90 * 24 * time.Hour), nil
		}}
		c, log := guarded(m)

		started := time.Now()
		_, err := c.GetCertificate(hello("shop.example.com"))
		if took := time.Since(started); !errors.Is(err, errCertWait) || took != c.wait {
			t.Fatalf("the first handshake: %v after %s", err, took)
		}
		// A second visitor waits for the same order, not for one of its
		// own, and no longer than the first did.
		if _, err := c.GetCertificate(hello("shop.example.com")); !errors.Is(err, errCertWait) {
			t.Fatalf("the second handshake: %v", err)
		}
		if n := m.count(); n != 1 {
			t.Fatalf("%d calls into the manager for one host", n)
		}
		// Another host is not held up.
		started = time.Now()
		if cert, err := c.GetCertificate(hello("blog.example.com")); err != nil || cert == nil || time.Since(started) != 0 {
			t.Fatalf("another host while an order is running: %v after %s", err, time.Since(started))
		}
		// What the certificate authority asks while it checks the order
		// must not wait behind the order.
		challenge := &tls.ClientHelloInfo{ServerName: "shop.example.com", SupportedProtos: []string{"acme-tls/1"}}
		if cert, err := c.GetCertificate(challenge); err != nil || cert == nil || time.Since(started) != 0 {
			t.Fatalf("a challenge handshake while its order is running: %v", err)
		}
		if got := log.with("taking long"); len(got) != 1 || !strings.Contains(got[0], "host=shop.example.com") {
			t.Fatalf("logged about the wait: %q", log.lines)
		}

		close(release)
		synctest.Wait()
		if c.inFlight() != 0 {
			t.Fatal("the order ended and its flight is still there")
		}
		if got := log.with("certificate obtained"); len(got) != 1 {
			t.Fatalf("the order that ended after its visitors had left was not reported: %q", log.lines)
		}
		if cert, err := c.GetCertificate(hello("shop.example.com")); err != nil || cert == nil {
			t.Fatalf("the visitor after the order ended: %v", err)
		}
	})
}

// The manager has two certificates for a host and orders them apart. The
// order for the few clients that need an RSA key must not turn away the
// visitors whose certificate is there.
func TestAnOrderForOneKindOfKeyDoesNotHoldUpTheOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := func() *tls.ClientHelloInfo {
			return &tls.ClientHelloInfo{ServerName: "shop.example.com", CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}
		}
		modern := func() *tls.ClientHelloInfo {
			return &tls.ClientHelloInfo{
				ServerName:       "shop.example.com",
				CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
				SupportedCurves:  []tls.CurveID{tls.X25519, tls.CurveP256},
				SignatureSchemes: []tls.SignatureScheme{tls.PSSWithSHA256, tls.ECDSAWithP256AndSHA256},
			}
		}
		if supportsECDSA(old()) || !supportsECDSA(modern()) {
			t.Fatal("the two hellos are not told apart")
		}
		release := make(chan struct{})
		m := &manager{answer: func(_ int, h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !supportsECDSA(h) {
				<-release
			}
			return certFor(90 * 24 * time.Hour), nil
		}}
		c, _ := guarded(m)

		turned := make(chan error, 1)
		go func() { _, err := c.GetCertificate(old()); turned <- err }()
		synctest.Wait()
		started := time.Now()
		for range 3 {
			if cert, err := c.GetCertificate(modern()); err != nil || cert == nil {
				t.Fatalf("a visitor whose certificate is there, while the other is ordered: %v", err)
			}
		}
		if waited := time.Since(started); waited != 0 {
			t.Fatalf("waited %s behind the other order", waited)
		}
		// Two of the old kind are still one flight.
		if _, err := c.GetCertificate(old()); !errors.Is(err, errCertWait) {
			t.Fatalf("%v", err)
		}
		if err := <-turned; !errors.Is(err, errCertWait) {
			t.Fatalf("%v", err)
		}
		if n := m.count(); n != 4 {
			t.Fatalf("%d calls into the manager, want one order and three reads", n)
		}
		close(release)
		synctest.Wait()
	})
}

// The manager renews by itself and keeps a failure to itself: the old
// certificate is served until its last day.
func TestACertificateCloseToItsEndIsWarnedAboutOnceADay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		left := 10 * 24 * time.Hour
		m := &manager{answer: func(int, *tls.ClientHelloInfo) (*tls.Certificate, error) { return certFor(left), nil }}
		c, log := guarded(m)
		for range 3 {
			c.GetCertificate(hello("shop.example.com"))
		}
		c.GetCertificate(hello("scanner.example.net"))
		synctest.Wait()
		if got := log.with("close to its end"); len(got) != 1 || !strings.Contains(got[0], "host=shop.example.com") {
			t.Fatalf("logged: %q", log.lines)
		}
		time.Sleep(23 * time.Hour)
		c.GetCertificate(hello("shop.example.com"))
		synctest.Wait()
		if got := log.with("close to its end"); len(got) != 1 {
			t.Fatalf("within the day: %q", log.lines)
		}
		time.Sleep(2 * time.Hour)
		c.GetCertificate(hello("shop.example.com"))
		synctest.Wait()
		if got := log.with("close to its end"); len(got) != 2 {
			t.Fatalf("a day later: %q", log.lines)
		}
		// One that has its time is nothing to talk about.
		left = 60 * 24 * time.Hour
		c.GetCertificate(hello("blog.example.com"))
		synctest.Wait()
		if len(log.lines) != 2 {
			t.Fatalf("a certificate with sixty days left: %q", log.lines)
		}
	})
}

// In a handshake the server would recover; in a goroutine of its own a
// panic would take the proxy and every app behind it.
func TestAPanicInTheManagerIsAnErrorForOneHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &manager{answer: func(n int, _ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if n == 1 {
				panic("a nil map in somebody's code")
			}
			return certFor(90 * 24 * time.Hour), nil
		}}
		c, log := guarded(m)
		if _, err := c.GetCertificate(hello("shop.example.com")); err == nil || !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("%v", err)
		}
		if cert, err := c.GetCertificate(hello("shop.example.com")); err != nil || cert == nil {
			t.Fatalf("the handshake after: %v", err)
		}
		synctest.Wait()
		if got := log.with("no certificate could be obtained"); len(got) != 1 {
			t.Fatalf("logged: %q", log.lines)
		}
	})
}

// The guard as the proxy puts it on its server, in real handshakes: a
// visitor is served when the manager answers, and one who is turned away
// gets a TLS error, not a closed connection, however late it said which
// host it wants.
func TestGuardCertsOnAServer(t *testing.T) {
	target := backend(t, "app")
	p, _ := newProxy(t, true,
		Route{Host: "example.com", Target: target, TLS: true},
		Route{Host: "blog.example.com", Target: target, TLS: true},
	)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "served") }))
	// The server gives up on a handshake after this long, so the guard
	// turns a visitor away after four fifths of it.
	const deadline = 2 * time.Second
	srv.Config.ReadHeaderTimeout = deadline
	// As the proxy's own server: a visitor turned away is not worth a line.
	srv.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	release := make(chan struct{})
	var own *tls.Certificate
	m := &manager{answer: func(_ int, h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		// The test server's certificate is for example.com.
		if h.ServerName != "example.com" {
			<-release
		}
		return own, nil
	}}
	log := &logLines{}
	srv.TLS = &tls.Config{}
	p.guardCerts(srv.Config, srv.TLS, m.get, slog.New(log))
	srv.StartTLS()
	defer srv.Close()
	defer close(release)
	own = &srv.TLS.Certificates[0]
	trusted := x509.NewCertPool()
	trusted.AddCert(srv.Certificate())

	// visit shakes hands for a host, after keeping the server waiting.
	visit := func(host string, after time.Duration) (time.Duration, error) {
		started := time.Now()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		time.Sleep(after)
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		err = tls.Client(conn, &tls.Config{ServerName: host, RootCAs: trusted}).Handshake()
		return time.Since(started), err
	}
	if _, err := visit("example.com", 0); err != nil {
		t.Fatalf("a visitor of a host whose certificate is there: %v", err)
	}

	type result struct {
		took time.Duration
		err  error
	}
	results := make(chan result, 2)
	for _, after := range []time.Duration{0, deadline / 4} {
		go func() {
			took, err := visit("blog.example.com", after)
			results <- result{took, err}
		}()
	}
	for range 2 {
		r := <-results
		// A closed connection would be an EOF here: the server's deadline
		// came first.
		if r.err == nil || !strings.Contains(r.err.Error(), "tls: internal error") {
			t.Errorf("a visitor who was turned away should see a TLS error, got: %v", r.err)
		}
		if r.took < deadline/2 || r.took >= deadline {
			t.Errorf("turned away after %s; the server's deadline is %s", r.took, deadline)
		}
	}
	// The host is one of the table's, so its wait is worth a line, which is
	// written at the moment the first visitor is turned away. The one order
	// was shared.
	for until := time.Now().Add(5 * time.Second); len(log.with("taking long")) == 0 && time.Now().Before(until); {
		time.Sleep(5 * time.Millisecond)
	}
	if got := log.with("taking long"); len(got) != 1 || !strings.Contains(got[0], "host=blog.example.com") {
		t.Errorf("logged: %q", got)
	}
	if n := m.count(); n != 2 {
		t.Errorf("%d calls into the manager, want one for each host", n)
	}
}
