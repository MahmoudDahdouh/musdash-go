package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/netguard"
)

// capture is a server that records the one request a channel sends.
type capture struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   []byte
	status int
	answer string
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path, c.header = r.URL.Path, r.Header.Clone()
	c.body, _ = io.ReadAll(r.Body)
	if c.status != 0 {
		w.WriteHeader(c.status)
	}
	io.WriteString(w, c.answer)
}

func (c *capture) json(t *testing.T) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var v map[string]any
	if err := json.Unmarshal(c.body, &v); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, c.body)
	}
	return v
}

var local = Sender{Dialer: Dialer{AllowLoopback: true}}

var failed = Event{
	Kind: EventBackup, Title: "Backup of maindb failed",
	Body: "pg_dump: error: connection refused\n@everyone <b>bold</b> & more", URL: "https://dash.example.com/databases/abc",
	At: time.Date(2026, 3, 10, 3, 0, 5, 0, time.UTC),
}

func TestWebhookKinds(t *testing.T) {
	ctx := context.Background()
	c := &capture{}
	srv := httptest.NewServer(c)
	defer srv.Close()

	// Discord: an embed, and nothing in the text may mention anyone.
	if err := local.Send(ctx, KindDiscord, map[string]string{"url": srv.URL + "/api/webhooks/1/tok"}, failed); err != nil {
		t.Fatal(err)
	}
	d := c.json(t)
	embed := d["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != failed.Title || !strings.Contains(embed["description"].(string), "connection refused") || embed["url"] != failed.URL {
		t.Fatalf("discord embed: %v", embed)
	}
	if parse, ok := d["allowed_mentions"].(map[string]any)["parse"].([]any); !ok || len(parse) != 0 {
		t.Fatalf("discord mentions are not switched off: %v", d["allowed_mentions"])
	}
	if c.path != "/api/webhooks/1/tok" || c.header.Get("Content-Type") != "application/json" {
		t.Fatalf("discord request: %s %v", c.path, c.header)
	}

	// Slack and Mattermost: their markup characters are escaped.
	for _, kind := range []string{KindSlack, KindMattermost} {
		if err := local.Send(ctx, kind, map[string]string{"url": srv.URL + "/hooks/x"}, failed); err != nil {
			t.Fatal(err)
		}
		text := c.json(t)["text"].(string)
		if !strings.HasPrefix(text, "*Backup of maindb failed*\n") || !strings.Contains(text, "&lt;b&gt;bold&lt;/b&gt; &amp; more") || strings.Contains(text, "<b>") || !strings.HasSuffix(text, failed.URL) {
			t.Fatalf("%s text: %q", kind, text)
		}
	}

	// Telegram: plain text to the chat, the token in the path.
	tg := local
	tg.Endpoints = map[string]string{"telegram": srv.URL}
	if err := tg.Send(ctx, KindTelegram, map[string]string{"token": "123:ABC", "chat": "-100200"}, failed); err != nil {
		t.Fatal(err)
	}
	m := c.json(t)
	if c.path != "/bot123:ABC/sendMessage" || m["chat_id"] != "-100200" || m["parse_mode"] != nil || !strings.Contains(m["text"].(string), "<b>bold</b>") {
		t.Fatalf("telegram: %s %v", c.path, m)
	}

	// Pushover: a form.
	po := local
	po.Endpoints = map[string]string{"pushover": srv.URL}
	if err := po.Send(ctx, KindPushover, map[string]string{"token": "app-token", "user": "user-key"}, failed); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(string(c.body))
	if c.path != "/1/messages.json" || form.Get("token") != "app-token" || form.Get("user") != "user-key" || form.Get("title") != failed.Title || form.Get("url") != failed.URL {
		t.Fatalf("pushover: %s %v", c.path, form)
	}

	// Generic webhook: the event as JSON, signed when a secret is set.
	if err := local.Send(ctx, KindWebhook, map[string]string{"url": srv.URL + "/in", "secret": "s3cret"}, failed); err != nil {
		t.Fatal(err)
	}
	w := c.json(t)
	if w["event"] != "backup" || w["ok"] != false || w["title"] != failed.Title || w["time"] != "2026-03-10T03:00:05Z" {
		t.Fatalf("webhook body: %v", w)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(c.body)
	if c.header.Get("X-Musdash-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("webhook signature: %q", c.header.Get("X-Musdash-Signature"))
	}
	local.Send(ctx, KindWebhook, map[string]string{"url": srv.URL + "/in"}, failed)
	if c.header.Get("X-Musdash-Signature") != "" {
		t.Fatal("a webhook without a secret was signed")
	}
}

func TestFailuresDoNotQuoteTheAddress(t *testing.T) {
	ctx := context.Background()
	c := &capture{status: http.StatusNotFound, answer: `{"message": "Unknown Webhook", "code": 10015}`}
	srv := httptest.NewServer(c)
	secretURL := srv.URL + "/api/webhooks/1/very-secret-token"
	err := local.Send(ctx, KindDiscord, map[string]string{"url": secretURL}, failed)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a refused webhook: %v", err)
	}
	if strings.Contains(err.Error(), "very-secret-token") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("the error quotes the channel's address: %v", err)
	}
	// Nothing listening any more.
	srv.Close()
	err = local.Send(ctx, KindDiscord, map[string]string{"url": secretURL}, failed)
	if err == nil || strings.Contains(err.Error(), "very-secret-token") || strings.Contains(err.Error(), "/api/webhooks") {
		t.Fatalf("an unreachable webhook: %v", err)
	}
	tg := local
	tg.Endpoints = map[string]string{"telegram": "http://127.0.0.1:1"}
	err = tg.Send(ctx, KindTelegram, map[string]string{"token": "123:SECRET", "chat": "1"}, failed)
	if err == nil || strings.Contains(err.Error(), "123:SECRET") {
		t.Fatalf("telegram's error quotes the bot token: %v", err)
	}
}

func TestLongAndMultilineTextIsTamed(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	e := Event{Title: "Line one\r\nX-Injected: yes   " + strings.Repeat("t", 500), Body: strings.Repeat("é", 4000)}
	if err := local.Send(context.Background(), KindWebhook, map[string]string{"url": srv.URL}, e); err != nil {
		t.Fatal(err)
	}
	w := c.json(t)
	title, body := w["title"].(string), w["body"].(string)
	if strings.ContainsAny(title, "\r\n") || len(title) > maxTitle+4 || !strings.HasPrefix(title, "Line one X-Injected: yes t") {
		t.Fatalf("title: %q", title)
	}
	if len(body) > maxBody+4 || !strings.HasSuffix(body, "…") || strings.ContainsRune(body, '\uFFFD') {
		t.Fatalf("body is %d bytes and ends %q", len(body), body[len(body)-8:])
	}
}

// A channel must not be a way to read from a service inside the network:
// what the other end answers is never part of the error.
func TestAnswersAreNotEchoed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"internal":"cluster-secret-123"}`)
	}))
	defer srv.Close()
	err := local.Send(context.Background(), KindWebhook, map[string]string{"url": srv.URL}, failed)
	if err == nil || !strings.Contains(err.Error(), "status 403") || strings.Contains(err.Error(), "cluster-secret") {
		t.Fatalf("%v", err)
	}
	// Nor does a failed connection say where it went or how it failed.
	err = local.Send(context.Background(), KindWebhook, map[string]string{"url": "http://127.0.0.1:1/x"}, failed)
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "refused") {
		t.Fatalf("%v", err)
	}
}

// The private addresses of containers on the server, and the server's own
// services, are out of reach.
func TestWebhooksCannotReachContainers(t *testing.T) {
	strict := Sender{Dialer: Dialer{Interfaces: func() []netguard.Interface {
		return []netguard.Interface{
			{Name: "eth0", Prefix: netip.MustParsePrefix("203.0.113.7/24")},
			{Name: "br-3f2a", Prefix: netip.MustParsePrefix("172.18.0.1/16")},
		}
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, target := range []string{"http://172.18.0.5:9200/_cat", "http://203.0.113.7:9100/metrics", "http://100.100.100.200/latest/meta-data/"} {
		if err := strict.Send(ctx, KindWebhook, map[string]string{"url": target}, failed); !errors.Is(err, ErrForbiddenAddress) {
			t.Errorf("%s: %v", target, err)
		}
	}
}

func TestWebhooksCannotReachTheServerItself(t *testing.T) {
	ctx := context.Background()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	strict := Sender{} // as in production

	// Directly, by address.
	err := strict.Send(ctx, KindWebhook, map[string]string{"url": srv.URL + "/internal"}, failed)
	if !errors.Is(err, ErrForbiddenAddress) || hits != 0 {
		t.Fatalf("a webhook to the loopback address: %v (%d requests arrived)", err, hits)
	}
	// By a name that resolves there.
	u, _ := url.Parse(srv.URL)
	err = strict.Send(ctx, KindWebhook, map[string]string{"url": "http://localhost:" + u.Port() + "/internal"}, failed)
	if !errors.Is(err, ErrForbiddenAddress) || hits != 0 {
		t.Fatalf("a webhook to localhost: %v (%d requests arrived)", err, hits)
	}
	// The metadata address of cloud providers.
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := strict.Send(short, KindWebhook, map[string]string{"url": "http://169.254.169.254/latest/meta-data/"}, failed); !errors.Is(err, ErrForbiddenAddress) {
		t.Fatalf("a webhook to the metadata address: %v", err)
	}
	// Through a redirect from somewhere allowed: the second connection is
	// checked like the first. The redirecting server stands in for a public
	// one, so only it is let through.
	target := srv.URL + "/internal"
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	ru, _ := url.Parse(redirector.URL)
	var port int
	for _, c := range ru.Port() {
		port = port*10 + int(c-'0')
	}
	onlyRedirector := Sender{Dialer: Dialer{LoopbackPorts: []int{port}}}
	err = onlyRedirector.Send(ctx, KindWebhook, map[string]string{"url": redirector.URL}, failed)
	if !errors.Is(err, ErrForbiddenAddress) || hits != 0 {
		t.Fatalf("a redirect to the loopback address: %v (%d requests arrived)", err, hits)
	}
}

func TestCheck(t *testing.T) {
	good := map[string]map[string]string{
		KindDiscord:  {"url": "https://discord.com/api/webhooks/1/x"},
		KindSlack:    {"url": "https://hooks.slack.com/services/T/B/x"},
		KindTelegram: {"token": "1:x", "chat": "42"},
		KindPushover: {"token": "a", "user": "u"},
		KindWebhook:  {"url": "http://10.0.0.5:9000/hook"},
		KindEmail:    {"host": "smtp.example.com", "port": "587", "from": "musdash@example.com", "to": "a@example.com, B <b@example.com>"},
	}
	for kind, cfg := range good {
		if err := Check(kind, cfg); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	bad := []struct {
		kind string
		cfg  map[string]string
	}{
		{"carrier-pigeon", map[string]string{}},
		{KindDiscord, map[string]string{}},
		{KindDiscord, map[string]string{"url": "discord.com/api"}},
		{KindDiscord, map[string]string{"url": "file:///etc/passwd"}},
		{KindDiscord, map[string]string{"url": "https://x/\nHost: evil"}},
		{KindTelegram, map[string]string{"token": "1:x"}},
		{KindEmail, map[string]string{"host": "smtp.example.com", "port": "0", "from": "a@example.com", "to": "b@example.com"}},
		{KindEmail, map[string]string{"host": "smtp.example.com", "port": "587", "from": "not an address", "to": "b@example.com"}},
		{KindEmail, map[string]string{"host": "smtp.example.com", "port": "587", "from": "a@example.com", "to": ""}},
		{KindEmail, map[string]string{"host": "smtp.example.com\r\nRCPT TO:<x>", "port": "587", "from": "a@example.com", "to": "b@example.com"}},
	}
	for _, c := range bad {
		if err := Check(c.kind, c.cfg); err == nil {
			t.Errorf("%s %v was accepted", c.kind, c.cfg)
		}
	}
}

// smtpServer is a mail server that speaks just enough to take one message.
func smtpServer(t *testing.T) (addr string, received func() (from string, to []string, data string, auth string)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var from, auth, data string
	var to []string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { io.WriteString(conn, s+"\r\n") }
		say("220 test ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			upper := strings.ToUpper(line)
			mu.Lock()
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				say("250-test\r\n250 AUTH PLAIN")
			case strings.HasPrefix(upper, "AUTH PLAIN"):
				auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
				say("235 ok")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				from = line[len("MAIL FROM:"):]
				say("250 ok")
			case strings.HasPrefix(upper, "RCPT TO:"):
				to = append(to, line[len("RCPT TO:"):])
				say("250 ok")
			case upper == "DATA":
				say("354 go")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				data = b.String()
				say("250 queued")
			case upper == "QUIT":
				say("221 bye")
				mu.Unlock()
				return
			default:
				say("250 ok")
			}
			mu.Unlock()
		}
	}()
	return ln.Addr().String(), func() (string, []string, string, string) {
		mu.Lock()
		defer mu.Unlock()
		return from, to, data, auth
	}
}

func TestEmail(t *testing.T) {
	addr, received := smtpServer(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg := map[string]string{"host": host, "port": port, "username": "mailer", "password": "pw", "from": "musdash <musdash@example.com>", "to": "ops@example.com, Sam <sam@example.com>"}
	e := failed
	e.Title = "Sauvegarde de maindb échouée"
	e.Body = "first line\n.a line that starts with a dot\nlast line"
	if err := local.Send(context.Background(), KindEmail, cfg, e); err != nil {
		t.Fatal(err)
	}
	from, to, data, auth := received()
	if from != "<musdash@example.com>" || len(to) != 2 || to[0] != "<ops@example.com>" || to[1] != "<sam@example.com>" {
		t.Fatalf("envelope: %q %q", from, to)
	}
	if auth == "" {
		t.Fatal("the sign-in was not sent")
	}
	for _, want := range []string{
		"From: \"musdash\" <musdash@example.com>\r\n", "To: <ops@example.com>, \"Sam\" <sam@example.com>\r\n",
		"Subject: =?utf-8?q?Sauvegarde_de_maindb_=C3=A9chou=C3=A9e?=\r\n", "Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\nSauvegarde de maindb échouée\r\n\r\nfirst line\r\n", "last line\r\n\r\nhttps://dash.example.com/databases/abc\r\n",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("the message is missing %q:\n%s", want, data)
		}
	}
	// The dot line arrived, stuffed on the wire as the protocol requires.
	if !strings.Contains(data, "\r\n..a line that starts with a dot\r\n") {
		t.Errorf("a leading dot was not escaped:\n%s", data)
	}

	// A subject cannot add headers.
	addr, received = smtpServer(t)
	host, port, _ = net.SplitHostPort(addr)
	cfg["host"], cfg["port"] = host, port
	e.Title = "Hello\r\nBcc: attacker@example.com"
	if err := local.Send(context.Background(), KindEmail, cfg, e); err != nil {
		t.Fatal(err)
	}
	if _, _, data, _ := received(); strings.Contains(data, "\r\nBcc:") {
		t.Fatalf("a header was injected through the subject:\n%s", data)
	}

	// Without encryption a password is not sent in production.
	addr, received = smtpServer(t)
	host, port, _ = net.SplitHostPort(addr)
	cfg["host"], cfg["port"] = host, port
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	strict := Sender{Dialer: Dialer{LoopbackPorts: []int{p}}}
	err := strict.Send(context.Background(), KindEmail, cfg, failed)
	if err == nil || !strings.Contains(err.Error(), "no encryption") {
		t.Fatalf("a password over a plain connection: %v", err)
	}
	if _, _, _, auth := received(); auth != "" {
		t.Fatal("the password was sent over an unencrypted connection")
	}
}
