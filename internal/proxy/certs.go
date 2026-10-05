package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// certs stands between a TLS handshake and the certificate manager.
//
// Left to itself the manager answers a handshake for a host without a
// certificate by ordering one, inside the handshake, for as long as that
// takes: up to five minutes when the certificate authority does not
// answer. The visitor waits, and whatever went wrong is told to nobody,
// because the server's own error log is thrown away (it would hold a line
// for every scanner). So certs does three things:
//
//   - A handshake waits only so long. After that the visitor is turned
//     away; the order goes on, and the next visitor gets the certificate.
//   - One caller a host and kind of key is inside the manager at a time.
//     Whoever comes meanwhile waits for that one, on a channel and a timer,
//     however many they are.
//   - That one caller says what came of it, since only it is told.
type certs struct {
	// get is the manager's GetCertificate.
	get func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	// routed reports whether a host has a route with TLS: whether its
	// certificate is ours to care about. Everything else is scanners.
	routed func(host string) bool
	log    *slog.Logger
	// wait is how long after its connection arrived a handshake stops
	// waiting for a certificate. It must be shorter than the server's own
	// deadline for a handshake, or the visitor gets a closed connection
	// instead of an error.
	wait time.Duration
	// slow is how long handing out a certificate takes at most when it is
	// already there. What takes longer was an order.
	slow time.Duration
	now  func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
	// said is when a thing was last logged about a host.
	said map[string]time.Time
}

// flight is one call into the manager and what it returned.
type flight struct {
	done chan struct{}
	cert *tls.Certificate
	err  error
}

const (
	// certSlow separates a certificate that was there from one that was
	// ordered. Reading one from disk takes milliseconds.
	certSlow = time.Second
	// expirySoon is how close to its end a certificate must be to be worth
	// a warning. The manager starts renewing thirty days before; with this
	// little left, renewing has been failing for more than two weeks, and
	// the manager keeps the reason to itself.
	expirySoon = 14 * 24 * time.Hour
	// expiryEvery is how often that warning is repeated for a host.
	expiryEvery = 24 * time.Hour
	// failEvery is how often a host's failure is logged. After an order
	// failed the manager makes no other for a minute, but an error from
	// before the order (a certificate directory it cannot read) comes back
	// for every handshake.
	failEvery = time.Minute
	// maxSaid bounds the table of what was logged about which host.
	maxSaid = 1024
	// challengeProto is what the certificate authority asks for when it
	// checks a TLS-ALPN challenge.
	challengeProto = "acme-tls/1"
)

var errCertWait = errors.New("proxy: the certificate for this host is still being obtained")

func newCerts(get func(*tls.ClientHelloInfo) (*tls.Certificate, error), routed func(host string) bool, log *slog.Logger, wait time.Duration) *certs {
	return &certs{
		get: get, routed: routed, log: log, wait: wait, slow: certSlow, now: time.Now,
		flights: make(map[string]*flight), said: make(map[string]time.Time),
	}
}

// guardCerts puts a certs between a server's handshakes and the manager.
func (p *Proxy) guardCerts(srv *http.Server, cfg *tls.Config, get func(*tls.ClientHelloInfo) (*tls.Certificate, error), log *slog.Logger) {
	// ReadHeaderTimeout is also the server's deadline for a handshake. A
	// handshake stops waiting for its certificate a little before that:
	// afterwards an error could no longer be sent.
	guard := newCerts(get, func(host string) bool {
		_, secure, _ := p.Table().Host(host)
		return secure
	}, log, srv.ReadHeaderTimeout*4/5)
	cfg.GetCertificate = guard.GetCertificate
	srv.ConnContext = markAccepted
}

// acceptedKey is where a connection's context keeps when it arrived.
type acceptedKey struct{}

// markAccepted is a server's ConnContext. The server's deadline for a
// handshake runs from when the connection arrived, not from when the client
// said which host it wants, so that is the moment certs counts from.
func markAccepted(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, acceptedKey{}, time.Now())
}

// patience is how long a handshake may still wait for its certificate.
func (c *certs) patience(hello *tls.ClientHelloInfo) time.Duration {
	wait := c.wait
	if ctx := hello.Context(); ctx != nil {
		if at, ok := ctx.Value(acceptedKey{}).(time.Time); ok {
			wait -= time.Since(at)
		}
	}
	// A certificate that is there takes milliseconds, and a client that
	// was slow to say hello must still get that one.
	return max(wait, c.slow)
}

// GetCertificate is what the TLS server asks for a handshake's certificate.
func (c *certs) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// The certificate authority checking a challenge: the manager answers
	// without waiting for anything, and this handshake must not wait
	// behind the order it belongs to.
	if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == challengeProto {
		return c.get(hello)
	}
	// As the route table writes hosts, so that Example.com and example.com
	// are one flight.
	host := NormalizeHost(hello.ServerName)
	limit := time.NewTimer(c.patience(hello))
	defer limit.Stop()
	for {
		f, mine := c.join(host, hello)
		select {
		case <-f.done:
		case <-limit.C:
			return nil, errCertWait
		}
		if mine {
			return f.cert, f.err
		}
		// Somebody else's flight has ended. What it got is not passed on:
		// the manager is asked again, which is quick now (the certificate
		// is in its memory, or its refusal is), and so every handshake has
		// the manager's answer to its own hello.
	}
}

// join returns the flight a handshake belongs to, starting one when there
// is none.
func (c *certs) join(host string, hello *tls.ClientHelloInfo) (f *flight, mine bool) {
	// The manager keeps two certificates for a host, one for clients that
	// can use an ECDSA key and one for the few that cannot, and orders them
	// apart. As one flight, the order of the second would turn away every
	// visitor the first is there for.
	key := host + " rsa"
	if supportsECDSA(hello) {
		key = host + " ecdsa"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.flights[key]; f != nil {
		return f, false
	}
	f = &flight{done: make(chan struct{})}
	c.flights[key] = f
	go c.fly(key, host, f, hello)
	return f, true
}

// fly asks the manager and reports. It outlives the handshake that started
// it when the manager takes longer than a handshake waits.
func (c *certs) fly(key, host string, f *flight, hello *tls.ClientHelloInfo) {
	started := c.now()
	ours := c.routed(host)
	var late *time.Timer
	if ours {
		late = time.AfterFunc(c.wait, func() {
			c.log.Warn("a certificate is taking long to obtain; visitors are turned away until it is there", "host", host)
		})
	}
	func() {
		// The server would recover a panic in a handshake; here it would
		// take the proxy, and every app with it.
		defer func() {
			if r := recover(); r != nil {
				f.cert, f.err = nil, fmt.Errorf("proxy: the certificate manager panicked: %v", r)
			}
		}()
		f.cert, f.err = c.get(hello)
	}()
	if late != nil {
		late.Stop()
	}
	if ours {
		c.report(host, f, c.now().Sub(started))
	}
	c.mu.Lock()
	delete(c.flights, key)
	c.mu.Unlock()
	close(f.done)
}

// report logs what a call into the manager returned for one of our hosts.
func (c *certs) report(host string, f *flight, took time.Duration) {
	took = took.Round(time.Millisecond)
	if f.err != nil {
		// The order's own error goes to the caller that made the order.
		// For the next minute the manager answers everybody else, and
		// that caller too, with this: nothing new.
		if strings.HasPrefix(f.err.Error(), "acme/autocert: missing ") {
			return
		}
		if c.due("failed", host, failEvery) {
			c.log.Warn("no certificate could be obtained", "host", host, "err", f.err, "took", took)
		}
		return
	}
	if took > c.slow {
		c.log.Info("certificate obtained", "host", host, "took", took)
	}
	if f.cert == nil || f.cert.Leaf == nil {
		return
	}
	if f.cert.Leaf.NotAfter.Sub(c.now()) > expirySoon {
		return
	}
	if c.due("expiring", host, expiryEvery) {
		c.log.Warn("a certificate is close to its end and has not been renewed", "host", host, "expires", f.cert.Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
}

// due reports whether it is time to say a thing about a host again, and
// takes it as said.
func (c *certs) due(what, host string, every time.Duration) bool {
	key := what + " " + host
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, seen := c.said[key]; seen && now.Sub(last) < every {
		return false
	}
	// Only routed hosts get here, so this is for a table of routes that
	// changed many times over.
	if len(c.said) >= maxSaid {
		clear(c.said)
	}
	c.said[key] = now
	return true
}

// supportsECDSA is the manager's own rule (autocert's supportsECDSA, which
// it does not export) for which of a host's two certificates a client gets.
func supportsECDSA(hello *tls.ClientHelloInfo) bool {
	// The signature algorithms a client names limit what the cipher suites
	// allow: RFC 5246, section 7.4.1.4.1.
	if hello.SignatureSchemes != nil {
		ok := false
		for _, scheme := range hello.SignatureSchemes {
			switch scheme {
			case tls.ECDSAWithSHA1, tls.ECDSAWithP256AndSHA256, tls.ECDSAWithP384AndSHA384, tls.ECDSAWithP521AndSHA512:
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if hello.SupportedCurves != nil {
		ok := false
		for _, curve := range hello.SupportedCurves {
			if curve == tls.CurveP256 {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	for _, suite := range hello.CipherSuites {
		switch suite {
		case tls.TLS_ECDHE_ECDSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305:
			return true
		}
	}
	return false
}
