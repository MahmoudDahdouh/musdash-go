package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
)

// A terminal is a shell in one of the team's containers, typed into from a
// page. It is the one place where what a person types is run, so it is
// narrow on purpose:
//
//   - The shell runs in the container, never on the server. The command
//     that starts it is fixed; nothing from the request is part of it but
//     the container's name, which comes from the database or, for a
//     service, from the server's own list of that service's containers.
//   - A page opens it over a WebSocket. That request carries the session
//     cookie whichever page it comes from, so it must come from the
//     dashboard's own origin, and the first message must hold the
//     session's form token.
//   - Only so many are open at once, and one that nothing passes through
//     for a while is closed.

const (
	// maxTerminals bounds the terminals open across the dashboard. Each
	// holds a connection and, on its server, a `docker exec`.
	maxTerminals = 8
	// terminalIdle is how long a terminal may carry nothing in either
	// direction before it is closed.
	terminalIdle = 30 * time.Minute
	// terminalHello is how long a browser has to send its first message.
	terminalHello = 10 * time.Second
	// terminalPing keeps a quiet connection from being cut by whatever
	// stands between the browser and the dashboard, and finds a browser
	// that went away without saying so.
	terminalPing = 30 * time.Second
)

// shellPick starts bash where the image has it and sh otherwise. It is the
// whole of what a terminal runs.
const shellPick = `if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi`

// terminalCmd is the command that opens a shell in a container.
func terminalCmd(container string) (runner.Cmd, error) {
	if !docker.ValidName(container) {
		return runner.Cmd{}, errors.New("bad container name")
	}
	return runner.Cmd{Name: "docker", Args: []string{"exec", "--interactive", "--tty", "--env", "TERM=xterm-256color", container, "sh", "-c", shellPick}}, nil
}

// sameOrigin reports whether a request was made by a page of the dashboard
// itself. A browser sends Origin with every WebSocket handshake and a page
// cannot forge it; a request without one is not from a page at all.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// hello is the first message of a terminal's connection; a later message
// of the same shape with only a size is a resize.
type hello struct {
	CSRF string `json:"csrf"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// say sends the browser a note about the terminal: that it could not be
// opened, or that the shell has ended.
func say(ws *wsConn, key, text string) {
	raw, _ := json.Marshal(map[string]string{key: text})
	ws.Write(wsText, raw)
}

// serveTerminal turns the request into a terminal in the named container
// of the server. The caller has loaded the resource the container belongs
// to for the signed-in team.
func (s *Server) serveTerminal(w http.ResponseWriter, r *http.Request, serverID, container, what string) {
	if !sameOrigin(r) {
		http.Error(w, "A terminal can only be opened from the dashboard itself.", http.StatusForbidden)
		return
	}
	cmd, err := terminalCmd(container)
	if container == "" || err != nil {
		http.Error(w, "Nothing is running.", http.StatusConflict)
		return
	}
	select {
	case s.terminals <- struct{}{}:
		defer func() { <-s.terminals }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Too many terminals are open. Close one and try again.", http.StatusServiceUnavailable)
		return
	}
	server, err := s.DB.ServerByID(r.Context(), serverID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	run, err := s.Pool.Runner(r.Context(), server)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sess := sessionFrom(r)
	ws, err := acceptWS(w, r)
	if err != nil {
		return
	}
	// From here on the connection is ours: the request's context no longer
	// ends when the browser leaves. Reading notices that; shutting down
	// closes the connection so that reading returns.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if s.Closing != nil {
		stop := context.AfterFunc(s.Closing, func() { ws.Close(wsGoingAway, "musdash is restarting") })
		defer stop()
	}
	defer ws.Close(wsNormal, "")

	// Nothing is started for a page that cannot show the session's token.
	kind, raw, err := ws.Read(time.Now().Add(terminalHello))
	var first hello
	if err != nil || kind != wsText || json.Unmarshal(raw, &first) != nil ||
		len(sess.CSRFToken) < 32 || subtle.ConstantTimeCompare([]byte(first.CSRF), []byte(sess.CSRFToken)) != 1 {
		ws.Close(wsPolicy, "this page may not open a terminal")
		return
	}

	term, err := run.Terminal(ctx, cmd, first.Cols, first.Rows)
	if err != nil {
		s.Log.Warn("open a terminal", "resource", what, "err", err)
		say(ws, "error", "The terminal could not be opened on the server.")
		return
	}
	s.Log.Info("terminal opened", "resource", what, "user", sess.User.Email)
	defer s.Log.Info("terminal closed", "resource", what, "user", sess.User.Email)

	// Whichever side ends first takes the other with it, from whichever
	// goroutine notices. Hanging up the shell is also what frees this
	// goroutine when it is stuck typing into a program that does not read:
	// then it cannot see the browser leave, and somebody else must.
	var once sync.Once
	end := func() {
		once.Do(func() {
			term.Close()
			ws.Close(wsNormal, "")
		})
	}
	defer end()

	// One that carries nothing for a while is closed. Both directions
	// count: somebody may be watching output without typing.
	var idleMu sync.Mutex
	idle := time.AfterFunc(terminalIdle, func() {
		say(ws, "error", "Closed after "+terminalIdle.String()+" without activity.")
		end()
	})
	defer idle.Stop()
	active := func() {
		idleMu.Lock()
		idle.Reset(terminalIdle)
		idleMu.Unlock()
	}

	// A ping that cannot be sent is how a browser that vanished is found
	// when nothing else is being written to it.
	go func() {
		t := time.NewTicker(s.pingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if ws.Write(wsPing, nil) != nil {
					end()
					return
				}
			}
		}
	}()

	// What the shell prints goes to the browser as it comes. Nothing is
	// kept: a browser that does not read holds the shell up, as a slow
	// terminal would.
	go func() {
		defer end()
		buf := make([]byte, 16<<10)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				active()
				if ws.Write(wsBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				say(ws, "exit", "The shell has ended.")
				return
			}
		}
	}()

	// What is typed goes to the shell; a text message is a new size.
	for {
		kind, raw, err := ws.Read(time.Time{})
		if err != nil {
			return
		}
		active()
		switch kind {
		case wsBinary:
			if _, err := term.Write(raw); err != nil {
				return
			}
		case wsText:
			var size hello
			if json.Unmarshal(raw, &size) == nil && size.Cols > 0 && size.Rows > 0 {
				term.Resize(size.Cols, size.Rows)
			}
		}
	}
}

func (s *Server) appTerminal(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.render(w, r, http.StatusOK, pages.AppTerminal(s.appShell(w, r, v), v))
	}
}

func (s *Server) appTerminalWS(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.serveTerminal(w, r, v.App.ServerID, v.App.Container, "app "+v.App.ID)
	}
}

func (s *Server) databaseTerminal(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.render(w, r, http.StatusOK, pages.DatabaseTerminal(s.databaseShell(w, r, v), v))
	}
}

func (s *Server) databaseTerminalWS(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.serveTerminal(w, r, v.DB.ServerID, v.DB.Container, "database "+v.DB.ID)
	}
}

// serviceRunning lists the names of a service's running containers, as the
// server reports them.
func (s *Server) serviceRunning(ctx context.Context, svc db.Service) ([]string, error) {
	if svc.Members == "" || svc.Status == db.AppStopped {
		return nil, nil
	}
	server, err := s.DB.ServerByID(ctx, svc.ServerID)
	if err != nil {
		return nil, err
	}
	dk, err := s.Pool.Docker(ctx, server)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, readingTimeout)
	defer cancel()
	return serviceContainers(svc.ID)(ctx, dk)
}

func (s *Server) serviceTerminal(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	names, err := s.serviceRunning(r.Context(), v.Service)
	if err != nil {
		// The page still opens; it says there is nothing to connect to.
		s.Log.Warn("list a service's containers", "service", v.Service.ID, "err", err)
	}
	chosen := r.URL.Query().Get("container")
	found := false
	for _, name := range names {
		found = found || name == chosen
	}
	if !found {
		chosen = ""
		if len(names) > 0 {
			chosen = names[0]
		}
	}
	s.render(w, r, http.StatusOK, pages.ServiceTerminal(s.serviceShell(w, r, v), v, names, chosen))
}

// serviceTerminalWS opens a terminal in one container of a service. The
// name in the request is only a choice among the service's own running
// containers, as the server lists them now.
func (s *Server) serviceTerminalWS(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	names, err := s.serviceRunning(r.Context(), v.Service)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	chosen := ""
	for _, name := range names {
		if name == r.URL.Query().Get("container") {
			chosen = name
		}
	}
	s.serveTerminal(w, r, v.Service.ServerID, chosen, "service "+v.Service.ID)
}
