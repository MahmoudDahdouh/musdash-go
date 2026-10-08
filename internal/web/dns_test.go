package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

func addrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, len(list))
	for i, a := range list {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// What a lookup means for a domain that is to lead to a server.
func TestDNSVerdict(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "asd.com", IsNotFound: true}
	for name, c := range map[string]struct {
		addrs    []netip.Addr
		err      error
		serverIP string
		tone     string
		label    string
		says     []string
	}{
		"the server's address":            {addrs("203.0.113.7"), nil, "203.0.113.7", ui.ToneOK, "Points here", []string{"203.0.113.7, this server"}},
		"the same address twice":          {addrs("203.0.113.7", "::ffff:203.0.113.7"), nil, "203.0.113.7", ui.ToneOK, "Points here", nil},
		"another address":                 {addrs("198.51.100.4"), nil, "203.0.113.7", ui.ToneWarn, "Points elsewhere", []string{"198.51.100.4", "not at this server (203.0.113.7)", "A record", "CDN"}},
		"the server's and another":        {addrs("203.0.113.7", "198.51.100.4"), nil, "203.0.113.7", ui.ToneWarn, "Points elsewhere too", []string{"Only 203.0.113.7 is known to be this server"}},
		"an IPv6 server":                  {addrs("2001:db8::7"), nil, "2001:db8::7", ui.ToneOK, "Points here", nil},
		"IPv4 only, an IPv6 server":       {addrs("203.0.113.7"), nil, "2001:db8::7", ui.ToneWarn, "Points elsewhere", []string{"AAAA record"}},
		"no such name":                    {nil, notFound, "203.0.113.7", ui.ToneWarn, "No address", []string{"an A record with the value 203.0.113.7", "take a while"}},
		"a name with no address":          {nil, nil, "203.0.113.7", ui.ToneWarn, "No address", nil},
		"no such name, no server address": {nil, notFound, "", ui.ToneWarn, "No address", []string{"Add a DNS record for it"}},
		"a lookup that failed":            {nil, &net.DNSError{Err: "timeout", IsTimeout: true}, "203.0.113.7", ui.ToneNeutral, "Not checked", []string{"could not be looked up"}},
		"another error":                   {nil, errors.New("boom"), "203.0.113.7", ui.ToneNeutral, "Not checked", nil},
		"no address of the server":        {addrs("198.51.100.4"), nil, "", ui.ToneNeutral, "Not compared", []string{"198.51.100.4", "not set (Servers)"}},
		"more addresses than shown":       {addrs("198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5", "198.51.100.6"), nil, "203.0.113.7", ui.ToneWarn, "Points elsewhere", []string{"198.51.100.4 and others"}},
	} {
		got := dnsVerdict("asd.com", c.addrs, c.err, c.serverIP)
		if got.Host != "asd.com" || got.Tone != c.tone || got.Label != c.label {
			t.Errorf("%s: %q in tone %q (%s), want %q in %q", name, got.Label, got.Tone, got.Text, c.label, c.tone)
		}
		for _, want := range c.says {
			if !strings.Contains(got.Text, want) {
				t.Errorf("%s: %q does not say %q", name, got.Text, want)
			}
		}
		if strings.Contains(got.Text, "198.51.100.5") {
			t.Errorf("%s: more than four addresses are named: %s", name, got.Text)
		}
	}
}

// Check DNS looks up the stored name of one of the app's own domains, the
// other form of it too when that redirects here, and says where they lead.
func TestCheckDNS(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	servers, _ := a.db.ListServers(ctx, firstTeam(t, a))
	res, _ := a.post("/servers", "/servers/"+servers[0].ID, url.Values{"ip": {"203.0.113.7"}})
	wantRedirect(t, res, "/servers")

	var mu sync.Mutex
	var asked []string
	answers := map[string][]netip.Addr{"asd.com": addrs("203.0.113.7"), "elsewhere.example.com": addrs("198.51.100.4")}
	a.server.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, host)
		if got, ok := answers[host]; ok {
			return got, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", false, nil)
	other := a.newApp(projectID, env, "api", false, url.Values{"domain": {"api.example.com"}})
	tab := a.appPath(web) + "/domains"
	for _, form := range []url.Values{
		{"host": {"asd.com"}, "redirect_www": {"1"}},
		{"host": {"elsewhere.example.com"}},
		{"host": {"abcd2345.203.0.113.7.sslip.io"}, "scheme": {"http"}},
	} {
		res, _ := a.post(tab, tab, form)
		wantRedirect(t, res, tab)
	}
	id := func(app, host string) string {
		t.Helper()
		doms, _ := a.db.ListDomains(ctx, db.KindApp, app)
		for _, d := range doms {
			if d.Host == host {
				return d.ID
			}
		}
		t.Fatalf("%s was not stored", host)
		return ""
	}

	// The page names the record to make, and offers the check for a domain
	// of one's own, not for a generated address.
	_, page := a.get(tab)
	if n := strings.Count(page, "an A record with the value 203.0.113.7"); n != 2 {
		t.Errorf("the record to add is named %d times, want in the tab's head and in the dialog", n)
	}
	for host, want := range map[string]bool{"asd.com": true, "elsewhere.example.com": true, "abcd2345.203.0.113.7.sslip.io": false} {
		did := id(web, host)
		has := strings.Contains(page, `hx-get="`+tab+"/"+did+`/dns"`) && strings.Contains(page, `hx-target="#dns-`+did+`"`) && strings.Contains(page, `id="dns-`+did+`"`)
		if has != want {
			t.Errorf("%s: Check DNS on the page is %v, want %v", host, has, want)
		}
	}

	res, body := a.get(tab + "/" + id(web, "asd.com") + "/dns")
	wantStatus(t, res, http.StatusOK)
	if strings.Count(body, "Points here") != 1 || strings.Count(body, "No address") != 1 || !strings.Contains(body, "www.asd.com") {
		t.Errorf("asd.com with its www form:\n%s", body)
	}
	if strings.Contains(body, "<html") {
		t.Error("the answer is a whole page, not what goes under the domain")
	}
	res, body = a.get(tab + "/" + id(web, "elsewhere.example.com") + "/dns")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "Points elsewhere") || !strings.Contains(body, "198.51.100.4") {
		t.Errorf("a domain that leads elsewhere:\n%s", body)
	}

	// Only the stored names were looked up.
	mu.Lock()
	got := strings.Join(asked, " ")
	mu.Unlock()
	if got != "asd.com www.asd.com elsewhere.example.com" {
		t.Errorf("looked up %q", got)
	}

	// A domain of another app is not one of this app's, and neither is an
	// id nobody has.
	for _, did := range []string{id(other, "api.example.com"), "nosuchdomain"} {
		res, _ = a.get(tab + "/" + did + "/dns")
		wantStatus(t, res, http.StatusNotFound)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 3 {
		t.Errorf("a domain that is not the app's was looked up: %v", asked)
	}
}
