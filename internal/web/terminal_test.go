package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// wsClient is a WebSocket client written out by hand, so that the tests
// can also send what a real one never would.
type wsClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// dialWS makes the handshake request with the given headers and returns
// the response and, when the server switched protocols, the connection.
func (a *app) dialWS(path string, header http.Header) (*http.Response, *wsClient) {
	a.t.Helper()
	u, _ := url.Parse(a.url)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { conn.Close() })
	key := make([]byte, 16)
	rand.Read(key)
	req, _ := http.NewRequest(http.MethodGet, a.url+path, nil)
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(key))
	req.Header.Set("Origin", a.url)
	for _, c := range a.client.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	for k, v := range header {
		if v == nil {
			req.Header.Del(k)
		} else {
			req.Header[k] = v
		}
	}
	if err := req.Write(conn); err != nil {
		a.t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	res, err := http.ReadResponse(r, req)
	if err != nil {
		a.t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res, nil
	}
	return res, &wsClient{t: a.t, conn: conn, r: r}
}

// frame sends one frame. Everything about it can be bent.
func (c *wsClient) frame(first byte, masked bool, payload []byte) {
	c.t.Helper()
	head := []byte{first, 0}
	switch n := len(payload); {
	case n <= 125:
		head[1] = byte(n)
	case n <= 0xFFFF:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	body := append([]byte(nil), payload...)
	if masked {
		head[1] |= 0x80
		mask := []byte{0x12, 0x34, 0x56, 0x78}
		head = append(head, mask...)
		for i := range body {
			body[i] ^= mask[i%4]
		}
	}
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	c.conn.Write(append(head, body...))
}

func (c *wsClient) text(s string)   { c.frame(0x80|wsText, true, []byte(s)) }
func (c *wsClient) binary(s string) { c.frame(0x80|wsBinary, true, []byte(s)) }

// next reads one frame from the server.
func (c *wsClient) next() (opcode byte, payload []byte, err error) {
	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var head [2]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return 0, nil, err
	}
	if head[1]&0x80 != 0 {
		c.t.Fatal("the server masked a frame")
	}
	n := uint64(head[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		io.ReadFull(c.r, ext[:])
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		io.ReadFull(c.r, ext[:])
		n = binary.BigEndian.Uint64(ext[:])
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(c.r, payload)
	return head[0] & 0x0F, payload, err
}

// until reads frames until one of the wanted kind arrives, answering
// nothing on the way.
func (c *wsClient) until(kind byte) []byte {
	c.t.Helper()
	for {
		op, payload, err := c.next()
		if err != nil {
			c.t.Fatalf("waiting for a frame of kind %d: %v", kind, err)
		}
		if op == kind {
			return payload
		}
	}
}

// closeCode waits for the server to close and returns the code it gave.
func (c *wsClient) closeCode() int {
	c.t.Helper()
	payload := c.until(wsClose)
	if len(payload) < 2 {
		return 0
	}
	return int(binary.BigEndian.Uint16(payload))
}

// fakeTerminal is a shell that says what it was given.
type fakeTerminal struct {
	mu      sync.Mutex
	out     chan []byte
	typed   []string
	sizes   [][2]int
	closed  bool
	closeCh chan struct{}
	// deaf makes typing wait for ever, as into a program that does not
	// read what it is sent.
	deaf bool
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{out: make(chan []byte, 16), closeCh: make(chan struct{})}
}

func (f *fakeTerminal) Read(p []byte) (int, error) {
	select {
	case b, ok := <-f.out:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, b), nil
	case <-f.closeCh:
		return 0, io.EOF
	}
}

func (f *fakeTerminal) size(i int) [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sizes[i]
}

func (f *fakeTerminal) Write(p []byte) (int, error) {
	if f.deaf {
		// A program that reads nothing: typing into it waits, until the
		// terminal is hung up.
		<-f.closeCh
		return 0, io.ErrClosedPipe
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, string(p))
	// A terminal echoes.
	select {
	case f.out <- append([]byte("echo:"), p...):
	default:
	}
	return len(p), nil
}

func (f *fakeTerminal) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes = append(f.sizes, [2]int{cols, rows})
	return nil
}

func (f *fakeTerminal) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.closeCh)
	}
	return nil
}

func (f *fakeTerminal) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// started is the terminals a scripted server has handed out.
type started struct {
	mu    sync.Mutex
	terms []*fakeTerminal
	lines []string
	// make builds the next terminal; nil builds an ordinary one.
	make func() *fakeTerminal
}

func (s *started) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.terms)
}

func (s *started) term(i int) *fakeTerminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terms[i]
}

func (s *started) line(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lines[i]
}

// terminalApp is a deployed app whose server hands out scripted terminals.
func terminalApp(t *testing.T) (a *app, appID, csrf string, st *started) {
	a = newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	appID = a.newApp(projectID, env, "web", true, nil)
	st = &started{}
	a.fake.Term = func(line string, _ runner.Cmd, cols, rows int) (runner.Terminal, error) {
		st.mu.Lock()
		defer st.mu.Unlock()
		term := newFakeTerminal()
		if st.make != nil {
			term = st.make()
		}
		term.sizes = append(term.sizes, [2]int{cols, rows})
		st.terms = append(st.terms, term)
		st.lines = append(st.lines, line)
		return term, nil
	}
	_, page := a.get(a.appPath(appID) + "/terminal")
	m := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `data-terminal="`+a.appPath(appID)+`/terminal/ws"`) || !strings.Contains(page, "/static/terminal.js?v=") {
		t.Fatalf("the terminal page:\n%s", page)
	}
	return a, appID, m[1], st
}

func hi(csrf string) string {
	raw, _ := json.Marshal(map[string]any{"csrf": csrf, "cols": 120, "rows": 40})
	return string(raw)
}

func TestTerminal(t *testing.T) {
	a, appID, csrf, st := terminalApp(t)
	app, _ := a.db.AppByID(context.Background(), appID)
	path := a.appPath(appID) + "/terminal/ws"

	res, ws := a.dialWS(path, nil)
	if ws == nil {
		t.Fatalf("the handshake was refused: %d", res.StatusCode)
	}
	if res.Header.Get("Sec-WebSocket-Accept") == "" || !strings.EqualFold(res.Header.Get("Upgrade"), "websocket") {
		t.Fatalf("handshake answer: %v", res.Header)
	}
	ws.text(hi(csrf))
	waitFor(t, "a terminal being started", func() bool { return st.count() == 1 })
	term := st.term(0)
	// A fixed command in the app's own container, with the size the page
	// measured. Nothing of the request is in it.
	want := "docker exec --interactive --tty --env TERM=xterm-256color " + app.Container + " sh -c " + shellPick
	if st.line(0) != want {
		t.Fatalf("the terminal runs %q\nwant              %q", st.line(0), want)
	}
	if got := term.size(0); got != [2]int{120, 40} {
		t.Fatalf("size %v", got)
	}

	// Typed bytes go in as they are; what the shell prints comes back.
	ws.binary("ls -la\r")
	if got := string(ws.until(wsBinary)); got != "echo:ls -la\r" {
		t.Fatalf("printed %q", got)
	}
	// A message in pieces is one message.
	ws.frame(wsBinary, true, []byte("ec"))
	ws.frame(wsContinuation, true, []byte("ho "))
	ws.frame(0x80|wsContinuation, true, []byte("hi\r"))
	if got := string(ws.until(wsBinary)); got != "echo:echo hi\r" {
		t.Fatalf("a message in three frames printed %q", got)
	}
	// A ping is answered with what it carried.
	ws.frame(0x80|wsPing, true, []byte("are you there"))
	if got := string(ws.until(wsPong)); got != "are you there" {
		t.Fatalf("pong %q", got)
	}
	// A text message is a new size, and nothing else.
	ws.text(`{"cols":90,"rows":28}`)
	ws.text(`{"cols":-5,"rows":0}`)
	ws.text(`not json`)
	ws.text(`{"csrf":"again","cols":0}`)
	waitFor(t, "the resize", func() bool { term.mu.Lock(); defer term.mu.Unlock(); return len(term.sizes) == 2 })
	if got := term.size(1); got != [2]int{90, 28} {
		t.Fatalf("resized to %v", got)
	}
	// 64 KB at once is the most a browser may send.
	ws.binary(strings.Repeat("x", wsMaxMessage))
	if got := ws.until(wsBinary); len(got) < 5 {
		t.Fatalf("a message of the largest size was not passed on: %d bytes back", len(got))
	}

	// The browser leaves: the shell is hung up.
	ws.frame(0x80|wsClose, true, binary.BigEndian.AppendUint16(nil, wsNormal))
	waitFor(t, "the terminal being closed", term.isClosed)

	// The shell ends: the page is told, and the connection closed.
	_, ws = a.dialWS(path, nil)
	ws.text(hi(csrf))
	waitFor(t, "a second terminal", func() bool { return st.count() == 2 })
	close(st.term(1).out)
	if note := string(ws.until(wsText)); !strings.Contains(note, `"exit"`) {
		t.Fatalf("note %q", note)
	}
	if code := ws.closeCode(); code != wsNormal {
		t.Fatalf("closed with %d", code)
	}
	// Both places are given back.
	waitFor(t, "the terminals' places being given back", func() bool { return len(a.server.terminals) == 0 })
}

func TestTerminalIsRefused(t *testing.T) {
	a, appID, csrf, st := terminalApp(t)
	path := a.appPath(appID) + "/terminal/ws"
	started := st.count

	// Before the connection is taken over.
	for name, c := range map[string]struct {
		header http.Header
		want   int
	}{
		"a page of another site":       {http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
		"a sister domain":              {http.Header{"Origin": {"http://app." + strings.TrimPrefix(a.url, "http://")}}, http.StatusForbidden},
		"no origin at all":             {http.Header{"Origin": nil}, http.StatusForbidden},
		"an origin that is not one":    {http.Header{"Origin": {"null"}}, http.StatusForbidden},
		"signed out":                   {http.Header{"Cookie": nil}, http.StatusSeeOther},
		"another protocol version":     {http.Header{"Sec-Websocket-Version": {"8"}}, http.StatusUpgradeRequired},
		"no key":                       {http.Header{"Sec-Websocket-Key": nil}, http.StatusBadRequest},
		"a key of the wrong length":    {http.Header{"Sec-Websocket-Key": {"c2hvcnQ="}}, http.StatusBadRequest},
		"a plain request for the page": {http.Header{"Upgrade": nil, "Connection": {"keep-alive"}}, http.StatusBadRequest},
		"an upgrade to something else": {http.Header{"Upgrade": {"h2c"}}, http.StatusBadRequest},
	} {
		res, ws := a.dialWS(path, c.header)
		if ws != nil || res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", name, res.StatusCode, c.want)
		}
	}
	// After: the first message must hold the session's token.
	for name, first := range map[string]func(ws *wsClient){
		"no token":                 func(ws *wsClient) { ws.text(`{"cols":80,"rows":24}`) },
		"a wrong token":            func(ws *wsClient) { ws.text(hi(strings.Repeat("a", len(csrf)))) },
		"a token that is a prefix": func(ws *wsClient) { ws.text(hi(csrf[:20])) },
		"the token typed":          func(ws *wsClient) { ws.binary(hi(csrf)) },
		"not JSON":                 func(ws *wsClient) { ws.text(csrf) },
	} {
		_, ws := a.dialWS(path, nil)
		if ws == nil {
			t.Fatalf("%s: the handshake was refused", name)
		}
		first(ws)
		if code := ws.closeCode(); code != wsPolicy {
			t.Errorf("%s: closed with %d, want %d", name, code, wsPolicy)
		}
	}
	// What a browser never sends ends the connection.
	for name, c := range map[string]struct {
		send func(ws *wsClient)
		code int
	}{
		"a frame that is not masked": {func(ws *wsClient) { ws.frame(0x80|wsText, false, []byte(hi(csrf))) }, wsProtocol},
		"a reserved bit":             {func(ws *wsClient) { ws.frame(0xC0|wsText, true, []byte(hi(csrf))) }, wsProtocol},
		"an unknown kind of frame":   {func(ws *wsClient) { ws.frame(0x80|0x3, true, nil) }, wsProtocol},
		"a continuation of nothing":  {func(ws *wsClient) { ws.frame(0x80|wsContinuation, true, []byte("x")) }, wsProtocol},
		"a ping in pieces":           {func(ws *wsClient) { ws.frame(wsPing, true, nil) }, wsProtocol},
		"a message over the limit":   {func(ws *wsClient) { ws.binary(strings.Repeat("x", wsMaxMessage+1)) }, wsTooBig},
		"a message that says it is huge": {func(ws *wsClient) {
			// The header alone: nothing may be set aside for it.
			ws.conn.Write(append([]byte{0x80 | wsBinary, 0x80 | 127}, binary.BigEndian.AppendUint64(nil, 1<<40)...))
		}, wsTooBig},
	} {
		_, ws := a.dialWS(path, nil)
		c.send(ws)
		if code := ws.closeCode(); code != c.code {
			t.Errorf("%s: closed with %d, want %d", name, code, c.code)
		}
	}
	if started() != 0 {
		t.Fatalf("%d terminals were started by requests that should have been refused", started())
	}
	// Pieces that add up to more than the limit, inside a real session.
	_, ws := a.dialWS(path, nil)
	ws.text(hi(csrf))
	waitFor(t, "a terminal", func() bool { return started() == 1 })
	ws.frame(wsBinary, true, []byte(strings.Repeat("x", wsMaxMessage-10)))
	ws.frame(0x80|wsContinuation, true, []byte(strings.Repeat("x", 20)))
	if code := ws.closeCode(); code != wsTooBig {
		t.Fatalf("pieces over the limit: closed with %d", code)
	}
	waitFor(t, "its terminal being closed", st.term(0).isClosed)
}

func TestTerminalLimitsAndOtherResources(t *testing.T) {
	a, appID, csrf, st := terminalApp(t)
	ctx := context.Background()
	path := a.appPath(appID) + "/terminal/ws"

	// Only so many at once.
	var open []*wsClient
	for i := 0; i < maxTerminals; i++ {
		_, ws := a.dialWS(path, nil)
		if ws == nil {
			t.Fatalf("terminal %d was refused", i)
		}
		ws.text(hi(csrf))
		open = append(open, ws)
	}
	waitFor(t, "all terminals", func() bool { return st.count() == maxTerminals })
	if res, ws := a.dialWS(path, nil); ws != nil || res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("one terminal too many: %d", res.StatusCode)
	}
	// A browser that just goes away gives its place back.
	open[0].conn.Close()
	waitFor(t, "the place being given back", func() bool { return len(a.server.terminals) == maxTerminals-1 })
	waitFor(t, "its terminal being closed", st.term(0).isClosed)
	if _, ws := a.dialWS(path, nil); ws == nil {
		t.Fatal("no terminal after one was closed")
	}

	// Nothing is running: nothing to open a terminal in.
	projectID, env := a.project("Other")
	idle := a.newApp(projectID, env, "idle", false, nil)
	if _, page := a.get(a.appPath(idle) + "/terminal"); !strings.Contains(page, "Nothing is running") || strings.Contains(page, "data-terminal") {
		t.Fatal("an app that is not running offers a terminal")
	}
	if res, ws := a.dialWS(a.appPath(idle)+"/terminal/ws", nil); ws != nil || res.StatusCode != http.StatusConflict {
		t.Fatalf("a terminal in nothing: %d", res.StatusCode)
	}

	// Another team's app is not there, for a page or for a terminal.
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, _ := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-app", Image: "nginx", Port: 80})
	a.db.SetAppRuntime(ctx, other.ID, db.AppRunning, "musdash-secret", 20009, "nginx")
	for _, path := range []string{a.appPath(other.ID) + "/terminal", a.databasePath(other.ID) + "/terminal", a.servicePath(other.ID) + "/terminal"} {
		if res, _ := a.get(path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, res.StatusCode)
		}
		if res, ws := a.dialWS(path+"/ws", nil); ws != nil || res.StatusCode != http.StatusNotFound {
			t.Errorf("a terminal at %s: %d, want 404", path, res.StatusCode)
		}
	}
}

func TestTerminalThatCannotBeOpened(t *testing.T) {
	a, appID, csrf, _ := terminalApp(t)
	a.fake.Term = func(string, runner.Cmd, int, int) (runner.Terminal, error) {
		return nil, errors.New("docker: permission denied while trying to connect to /var/run/docker.sock")
	}
	_, ws := a.dialWS(a.appPath(appID)+"/terminal/ws", nil)
	ws.text(hi(csrf))
	note := string(ws.until(wsText))
	// The page is told that it failed, not what the server said.
	if !strings.Contains(note, "could not be opened") || strings.Contains(note, "docker.sock") {
		t.Fatalf("note %q", note)
	}
	ws.closeCode()
}

func TestServiceTerminalIsOnlyInItsOwnContainers(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	app, _ := a.db.AppByID(ctx, appID)
	team := sessionTeam(a)
	svc, err := a.db.CreateService(ctx, team, db.Service{EnvironmentID: env.ID, ServerID: app.ServerID, Name: "stack", Template: "custom", Compose: "services: {}"})
	if err != nil {
		t.Fatal(err)
	}
	a.db.SetServiceMembers(ctx, svc.ID, []string{"web", "db"})
	a.db.Exec(`UPDATE services SET status = 'running' WHERE id = ?`, svc.ID)
	prev := a.fake.Handle
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker ps --all") {
			return "stack-web-1\trunning\tservice\t" + svc.ID + "\t\tUp\n" +
				"stack-db-1\trunning\tservice\t" + svc.ID + "\t\tUp\n" +
				"stack-init-1\texited\tservice\t" + svc.ID + "\t\tExited (0)\n" +
				app.Container + "\trunning\tapp\t" + appID + "\td1\tUp\n" +
				"--privileged\trunning\tservice\t" + svc.ID + "\t\tUp\n", nil
		}
		return prev(line, c)
	}
	var started []string
	var mu sync.Mutex
	a.fake.Term = func(line string, _ runner.Cmd, _, _ int) (runner.Terminal, error) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, line)
		return newFakeTerminal(), nil
	}
	base := a.servicePath(svc.ID) + "/terminal"
	_, page := a.get(base + "?container=stack-db-1")
	m := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `data-terminal="`+base+`/ws?container=stack-db-1"`) || !strings.Contains(page, "stack-web-1") {
		t.Fatalf("the service's terminal page:\n%s", page)
	}
	// Neither a container that stopped, nor one of something else, nor a
	// name that is no name is offered.
	for _, not := range []string{"stack-init-1", app.Container, "--privileged"} {
		if strings.Contains(page, not) {
			t.Errorf("the page offers %q", not)
		}
	}
	// A name that is not on the list falls back to the first that is.
	if _, page := a.get(base + "?container=" + app.Container); !strings.Contains(page, `ws?container=stack-web-1"`) {
		t.Error("an unknown container was not replaced by the first of the service's own")
	}
	for _, name := range []string{app.Container, "stack-init-1", "--privileged", "", "nosuch"} {
		if res, ws := a.dialWS(base+"/ws?container="+url.QueryEscape(name), nil); ws != nil || res.StatusCode != http.StatusConflict {
			t.Errorf("a terminal in %q through the service: %d", name, res.StatusCode)
		}
	}
	_, ws := a.dialWS(base+"/ws?container=stack-db-1", nil)
	if ws == nil {
		t.Fatal("a terminal in the service's own container was refused")
	}
	ws.text(hi(m[1]))
	waitFor(t, "the terminal", func() bool { mu.Lock(); defer mu.Unlock(); return len(started) == 1 })
	if !strings.Contains(started[0], " stack-db-1 sh -c ") {
		t.Fatalf("started %q", started[0])
	}
}

// A shell running something that reads nothing takes what is typed only
// until its buffer is full; after that, typing waits. A browser that leaves
// then must still get the terminal closed and its place given back.
func TestTerminalStuckTypingIsStillClosed(t *testing.T) {
	a, appID, csrf, st := terminalApp(t)
	a.server.pingEvery = 30 * time.Millisecond
	st.make = func() *fakeTerminal {
		term := newFakeTerminal()
		term.deaf = true
		return term
	}
	_, ws := a.dialWS(a.appPath(appID)+"/terminal/ws", nil)
	ws.text(hi(csrf))
	waitFor(t, "a terminal", func() bool { return st.count() == 1 })
	ws.binary("typed into a program that does not read")
	time.Sleep(50 * time.Millisecond)
	// The tab is closed without a word.
	ws.conn.Close()
	waitFor(t, "the terminal being closed", st.term(0).isClosed)
	waitFor(t, "its place being given back", func() bool { return len(a.server.terminals) == 0 })
}
