// Package notify tells people what happened: a deployment finished, a
// backup failed, a container stopped. Each channel is a webhook call or an
// email; there is no client library behind any of them.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Kinds of event a channel can subscribe to.
const (
	EventDeploy    = "deploy"
	EventBackup    = "backup"
	EventTask      = "task"
	EventContainer = "container"
	EventDisk      = "disk"
	EventTest      = "test"
)

// Events lists the event kinds with what each means, for forms. Short is
// the word for it where there is no room for the sentence.
var Events = []struct{ Kind, Label, Short string }{
	{EventDeploy, "A deployment finished or failed", "Deployments"},
	{EventBackup, "A backup finished or failed", "Backups"},
	{EventTask, "A scheduled task failed", "Tasks"},
	{EventContainer, "A container stopped unexpectedly", "Containers"},
	{EventDisk, "The server's disk is nearly full", "Disk"},
}

// Event is one thing worth telling.
type Event struct {
	Kind  string
	OK    bool
	Title string // one line: "Backup of maindb failed"
	Body  string // a few lines of detail; may be empty
	URL   string // the dashboard page about it; may be empty
	At    time.Time
}

// Channel kinds.
const (
	KindEmail      = "email"
	KindDiscord    = "discord"
	KindSlack      = "slack"
	KindMattermost = "mattermost"
	KindTelegram   = "telegram"
	KindPushover   = "pushover"
	KindWebhook    = "webhook"
)

// Field is one setting of a channel kind.
type Field struct {
	Key, Label, Hint string
	Secret           bool // never shown again once saved
	Optional         bool
}

// KindInfo describes a channel kind for forms.
type KindInfo struct {
	Kind, Label string
	Fields      []Field
}

// Kinds lists the channel kinds in display order.
var Kinds = []KindInfo{
	{KindDiscord, "Discord", []Field{{Key: "url", Label: "Webhook URL", Hint: "Server Settings, Integrations, Webhooks.", Secret: true}}},
	{KindSlack, "Slack", []Field{{Key: "url", Label: "Webhook URL", Hint: "An incoming webhook of a Slack app.", Secret: true}}},
	{KindMattermost, "Mattermost", []Field{{Key: "url", Label: "Webhook URL", Hint: "An incoming webhook.", Secret: true}}},
	{KindTelegram, "Telegram", []Field{
		{Key: "token", Label: "Bot token", Hint: "From @BotFather.", Secret: true},
		{Key: "chat", Label: "Chat ID", Hint: "The chat, group or channel the bot posts to."},
	}},
	{KindPushover, "Pushover", []Field{
		{Key: "token", Label: "Application token", Secret: true},
		{Key: "user", Label: "User or group key", Secret: true},
	}},
	{KindWebhook, "Webhook", []Field{
		{Key: "url", Label: "URL", Hint: "Receives a POST with a JSON body.", Secret: true},
		{Key: "secret", Label: "Signing secret", Hint: "When set, each request carries X-Musdash-Signature: sha256=<HMAC of the body>.", Secret: true, Optional: true},
	}},
	{KindEmail, "Email", []Field{
		{Key: "host", Label: "SMTP server", Hint: "Such as smtp.example.com."},
		{Key: "port", Label: "Port", Hint: "587 for STARTTLS, 465 for TLS from the start."},
		{Key: "username", Label: "Username", Optional: true},
		{Key: "password", Label: "Password", Secret: true, Optional: true},
		{Key: "from", Label: "From address"},
		{Key: "to", Label: "To address", Hint: "Several addresses separated by commas."},
	}},
}

// Kind finds a channel kind.
func Kind(kind string) (KindInfo, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindInfo{}, false
}

// Check validates a channel's settings before they are saved.
func Check(kind string, cfg map[string]string) error {
	info, ok := Kind(kind)
	if !ok {
		return fmt.Errorf("unknown channel kind %q", kind)
	}
	for _, f := range info.Fields {
		v := cfg[f.Key]
		if v == "" && !f.Optional {
			return fmt.Errorf("%s is required", f.Label)
		}
		if strings.ContainsAny(v, "\r\n\x00") || len(v) > 2000 {
			return fmt.Errorf("%s must be one line, under 2000 characters", f.Label)
		}
		if f.Key == "url" {
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return fmt.Errorf("%s must be an http:// or https:// address", f.Label)
			}
		}
	}
	if kind == KindEmail {
		if p, err := strconv.Atoi(cfg["port"]); err != nil || p < 1 || p > 65535 {
			return errors.New("Port must be a number between 1 and 65535")
		}
		if _, err := mail.ParseAddress(cfg["from"]); err != nil {
			return errors.New("From address is not a valid email address")
		}
		if list, err := mail.ParseAddressList(cfg["to"]); err != nil || len(list) == 0 {
			return errors.New("To address is not a valid email address")
		}
	}
	return nil
}

// Sender delivers events to channels.
type Sender struct {
	Dialer Dialer
	// Endpoints overrides the fixed addresses of Telegram and Pushover.
	// Tests set it.
	Endpoints map[string]string
}

const (
	maxTitle = 200
	maxBody  = 1500
)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// plain is the event as text: title, detail, link.
func plain(e Event) string {
	var b strings.Builder
	b.WriteString(e.Title)
	if e.Body != "" {
		b.WriteString("\n\n" + e.Body)
	}
	if e.URL != "" {
		b.WriteString("\n\n" + e.URL)
	}
	return b.String()
}

// Send delivers one event to one channel. The error never contains the
// channel's address or credentials.
func (s Sender) Send(ctx context.Context, kind string, cfg map[string]string, e Event) error {
	e.Title = clip(oneLine(e.Title), maxTitle)
	e.Body = clip(e.Body, maxBody)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch kind {
	case KindDiscord:
		colour := 0xC2410C // warm red: something failed
		if e.OK {
			colour = 0x1A4FD8
		}
		return s.postJSON(ctx, cfg["url"], nil, map[string]any{
			"embeds": []map[string]any{{"title": e.Title, "description": e.Body, "url": e.URL, "color": colour}},
			// Text that came from a build log must not ping anyone.
			"allowed_mentions": map[string]any{"parse": []string{}},
		})
	case KindSlack, KindMattermost:
		// Their own markup: only these three characters need escaping.
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
		text := "*" + esc.Replace(e.Title) + "*"
		if e.Body != "" {
			text += "\n" + esc.Replace(e.Body)
		}
		if e.URL != "" {
			text += "\n" + e.URL
		}
		return s.postJSON(ctx, cfg["url"], nil, map[string]any{"text": text})
	case KindTelegram:
		base := s.endpoint("telegram", "https://api.telegram.org")
		// Sent as plain text: no parse mode, so nothing in it is markup.
		return s.postJSON(ctx, base+"/bot"+url.PathEscape(cfg["token"])+"/sendMessage", nil, map[string]any{
			"chat_id": cfg["chat"], "text": plain(e), "disable_web_page_preview": true,
		})
	case KindPushover:
		form := url.Values{"token": {cfg["token"]}, "user": {cfg["user"]}, "title": {e.Title}, "message": {orDash(e.Body)}}
		if e.URL != "" {
			form.Set("url", e.URL)
		}
		return s.post(ctx, s.endpoint("pushover", "https://api.pushover.net")+"/1/messages.json", "application/x-www-form-urlencoded", nil, []byte(form.Encode()))
	case KindWebhook:
		body, err := json.Marshal(map[string]any{"event": e.Kind, "ok": e.OK, "title": e.Title, "body": e.Body, "url": e.URL, "time": e.At.UTC().Format(time.RFC3339)})
		if err != nil {
			return err
		}
		header := http.Header{}
		if secret := cfg["secret"]; secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			header.Set("X-Musdash-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		return s.post(ctx, cfg["url"], "application/json", header, body)
	case KindEmail:
		return s.email(ctx, cfg, e)
	}
	return fmt.Errorf("unknown channel kind %q", kind)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (s Sender) endpoint(name, def string) string {
	if v := s.Endpoints[name]; v != "" {
		return v
	}
	return def
}

func (s Sender) postJSON(ctx context.Context, target string, header http.Header, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.post(ctx, target, "application/json", header, body)
}

// post sends a request and maps the answer to an error that is safe to
// show: the status and a little of what the service said, never the address
// that was called, which for most channels is itself the secret.
func (s Sender) post(ctx context.Context, target, contentType string, header http.Header, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("the channel's address is not valid")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "musdash")
	res, err := s.Dialer.HTTPClient().Do(req)
	if err != nil {
		return connectError(err)
	}
	defer res.Body.Close()
	said, _ := io.ReadAll(io.LimitReader(res.Body, 300))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// What the other end wrote back is shown only for the public
		// services whose answers explain a refusal ("Unknown Webhook").
		// For any other address it is not, or a channel pointed at a
		// service inside the network would be a way to read from it.
		if knownService[res.Request.URL.Hostname()] {
			return fmt.Errorf("the service answered with status %d: %s", res.StatusCode, clip(oneLine(string(said)), 200))
		}
		return fmt.Errorf("the service answered with status %d", res.StatusCode)
	}
	return nil
}

// knownService are the hosts whose error answers are passed on.
var knownService = map[string]bool{
	"discord.com": true, "discordapp.com": true, "hooks.slack.com": true,
	"api.telegram.org": true, "api.pushover.net": true,
}

// connectError words a failed connection without the address it went to or
// how exactly it failed: with those, a channel would tell its owner which
// machines and ports exist behind the server.
func connectError(err error) error {
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, ErrForbiddenAddress):
		return ErrForbiddenAddress
	case errors.As(err, &certErr):
		return errors.New("the server's certificate is not trusted or is for another name")
	}
	return errors.New("no connection could be made, or it gave no answer in time")
}

// email sends the event through an SMTP server: TLS from the start on port
// 465, otherwise STARTTLS when the server offers it. A password is only
// ever sent over an encrypted connection.
func (s Sender) email(ctx context.Context, cfg map[string]string, e Event) error {
	from, err := mail.ParseAddress(cfg["from"])
	if err != nil {
		return errors.New("the From address is not valid")
	}
	to, err := mail.ParseAddressList(cfg["to"])
	if err != nil || len(to) == 0 {
		return errors.New("the To address is not valid")
	}
	host, port := cfg["host"], cfg["port"]
	addr := net.JoinHostPort(host, port)
	dialer := s.Dialer
	// A mail relay on the server itself is a normal setup.
	dialer.LoopbackPorts = append(dialer.LoopbackPorts, 25, 465, 587, 2525)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if errors.Is(err, ErrForbiddenAddress) {
			return ErrForbiddenAddress
		}
		return errors.New("the mail server could not be reached")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	encrypted := port == "465"
	if encrypted {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return errors.New("TLS with the mail server failed: is this its port for TLS from the start?")
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		// Whatever answered is not shown: it may not be a mail server.
		return errors.New("what answers at this address and port does not speak SMTP")
	}
	defer c.Close()
	if !encrypted {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("STARTTLS with the mail server failed: %w", err)
			}
			encrypted = true
		}
	}
	if cfg["username"] != "" {
		if !encrypted && !s.Dialer.AllowLoopback {
			return errors.New("the mail server offers no encryption, so the password was not sent")
		}
		if err := c.Auth(plainAuth{cfg["username"], cfg["password"]}); err != nil {
			return fmt.Errorf("the mail server refused the sign-in: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("the mail server refused the sender: %w", err)
	}
	var rcpt []string
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("the mail server refused a recipient: %w", err)
		}
		rcpt = append(rcpt, a.String())
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from.String())
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(rcpt, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", e.Title))
	fmt.Fprintf(&msg, "Date: %s\r\n", e.At.Format(time.RFC1123Z))
	msg.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	// Line ends as mail wants them; smtp's writer handles leading dots.
	msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(plain(e), "\r\n", "\n"), "\n", "\r\n"))
	msg.WriteString("\r\n")
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	return c.Quit()
}

// plainAuth is SMTP PLAIN authentication. The caller has already made sure
// the connection is encrypted; net/smtp's own PlainAuth refuses to work
// through a TLS connection it did not set up itself.
type plainAuth struct{ username, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected challenge from the mail server")
	}
	return nil, nil
}
