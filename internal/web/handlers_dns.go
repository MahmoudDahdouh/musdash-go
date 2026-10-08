package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// dnsCheckTimeout bounds one press of Check DNS, both names of it.
const dnsCheckTimeout = 5 * time.Second

// maxDNSShown is how many of a name's addresses a verdict names. A name
// can have any number of them.
const maxDNSShown = 4

// dnsRecord says which record leads a name to a server with this public
// address: its type and the address in its usual form. Two empty strings
// when the server has none stored.
func dnsRecord(serverIP string) (kind, value string) {
	ip, err := netip.ParseAddr(strings.TrimSpace(serverIP))
	if err != nil {
		return "", ""
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return "A", ip.String()
	}
	return "AAAA", ip.String()
}

// resolve looks a name up with the control plane's own resolver, or with
// the one a test put in.
func (s *Server) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if s.Resolve != nil {
		return s.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dnsVerdict says what a lookup of host means for a domain that is to
// lead to the server with the given public address. What was answered is
// the outside's word: it is shown only as the addresses it parsed into.
func dnsVerdict(host string, addrs []netip.Addr, err error, serverIP string) pages.DNSLine {
	line := pages.DNSLine{Host: host}
	kind, want := dnsRecord(serverIP)
	record := "a DNS record"
	if kind != "" {
		record = "an " + kind + " record with the value " + want
	}
	var notFound *net.DNSError
	switch {
	case errors.As(err, &notFound) && notFound.IsNotFound, err == nil && len(addrs) == 0:
		line.Tone, line.Label = ui.ToneWarn, "No address"
		line.Text = "It has no address yet. Add " + record + " for it at your DNS provider; a new record can take a while to be seen."
		return line
	case err != nil:
		line.Tone, line.Label = ui.ToneNeutral, "Not checked"
		line.Text = "The name could not be looked up just now. Try again in a moment."
		return line
	}
	seen := make(map[netip.Addr]bool, len(addrs))
	var shown []string
	here, elsewhere := false, false
	for _, a := range addrs {
		a = a.Unmap()
		if !a.IsValid() || seen[a] {
			continue
		}
		seen[a] = true
		if kind != "" && a.String() == want {
			here = true
		} else {
			elsewhere = true
		}
		if len(shown) < maxDNSShown {
			shown = append(shown, a.String())
		}
	}
	list := strings.Join(shown, ", ")
	if len(seen) > len(shown) {
		list += " and others"
	}
	switch {
	case kind == "":
		line.Tone, line.Label = ui.ToneNeutral, "Not compared"
		line.Text = "It points at " + list + ". This server's public IP address is not set (Servers), so the two cannot be compared."
	case here && !elsewhere:
		line.Tone, line.Label = ui.ToneOK, "Points here"
		line.Text = "It points at " + list + ", this server."
	case here:
		line.Tone, line.Label = ui.ToneWarn, "Points elsewhere too"
		// The others may be the server's too (its IPv6 address, say):
		// only one address of a server is stored.
		line.Text = "It points at " + list + ". Only " + want + " is known to be this server. Unless the others are its addresses too, requests that go to them do not reach the app, and a certificate may not be issued."
	default:
		line.Tone, line.Label = ui.ToneWarn, "Points elsewhere"
		line.Text = "It points at " + list + ", not at this server (" + want + "). Change its " + kind + " record, unless a CDN in front of the domain is meant to answer for it."
	}
	return line
}

// appDomainDNS answers Check DNS for one of the app's domains: where the
// name, and the other form of it when that redirects here, leads now. The
// name is the stored row's; nothing a request says is looked up. Nothing
// of the answer is kept.
func (s *Server) appDomainDNS(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	var names []string
	for _, d := range v.Domains {
		if d.ID != r.PathValue("did") {
			continue
		}
		names = append(names, d.Host)
		if d.RedirectWWW {
			names = append(names, deploy.OtherForm(d.Host))
		}
	}
	if len(names) == 0 {
		s.notFound(w, r)
		return
	}
	// What follows answers 200 whatever happens: htmx puts nothing else
	// under the domain.
	server, err := s.DB.ServerByID(r.Context(), v.App.ServerID)
	if err != nil {
		s.Log.Error("check DNS: the app's server", "app", v.App.ID, "err", err)
		s.render(w, r, http.StatusOK, pages.DomainDNS([]pages.DNSLine{{Host: names[0], Tone: ui.ToneNeutral, Label: "Not checked", Text: "The app's server could not be read just now. Try again in a moment."}}))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dnsCheckTimeout)
	defer cancel()
	lines := make([]pages.DNSLine, 0, len(names))
	for _, name := range names {
		if deploy.Generated(name) {
			lines = append(lines, pages.DNSLine{Host: name, Tone: ui.ToneOK, Label: "No DNS needed", Text: "A generated address leads to the address written in it."})
			continue
		}
		addrs, err := s.resolve(ctx, name)
		lines = append(lines, dnsVerdict(name, addrs, err, server.IP))
	}
	s.render(w, r, http.StatusOK, pages.DomainDNS(lines))
}
