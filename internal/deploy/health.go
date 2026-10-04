package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
)

// errExited means the new container stopped before it became healthy.
var errExited = errors.New("the container exited")

// Probe checks that a published port answers on the server's loopback. The
// default implementation connects from this process, which is correct for
// the local server; remote servers supply their own in phase 6.
type Probe interface {
	// HTTP passes when GET http://127.0.0.1:<port><path> answers below 400.
	HTTP(ctx context.Context, port int, path string) error
	// TCP passes when the port accepts a connection.
	TCP(ctx context.Context, port int) error
}

type localProbe struct{ client *http.Client }

func newLocalProbe() Probe {
	return localProbe{client: &http.Client{
		Timeout: 5 * time.Second,
		// A redirect is an answer; following it could leave the container.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableKeepAlives: true},
	}}
}

func (p localProbe) HTTP(ctx context.Context, port int, path string) error {
	u, err := HealthURL(port, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("the health check path answered %d", res.StatusCode)
	}
	return nil
}

// ValidHealthPath reports whether path can be used as a health check path:
// it must start with a single "/" and contain no whitespace or control
// characters.
func ValidHealthPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 512 {
		return false
	}
	for _, c := range path {
		if c <= ' ' || c == 0x7f || c == '\\' {
			return false
		}
	}
	return true
}

// HealthURL builds the probe URL for a container's published port. The host
// is fixed to loopback and only the path and query come from the app's
// settings. Joining strings instead would let a path such as
// "@other-host/" turn "127.0.0.1:<port>" into URL userinfo and send the
// request to another machine.
func HealthURL(port int, path string) (string, error) {
	if !ValidHealthPath(path) {
		return "", fmt.Errorf("health check path %q must start with a single /", path)
	}
	ref, err := url.ParseRequestURI(path)
	if err != nil {
		return "", fmt.Errorf("health check path %q is not valid", path)
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: ref.Path, RawPath: ref.RawPath, RawQuery: ref.RawQuery}
	if u.Hostname() != "127.0.0.1" || u.User != nil {
		return "", errors.New("health check URL left the loopback address")
	}
	return u.String(), nil
}

// TCP passes when something behind the published port is really listening.
//
// Connecting is not enough. Docker publishes ports through its own proxy
// process, which accepts every connection and only then tries the
// container; when nothing listens inside, it closes the connection again.
// So after connecting, the probe waits briefly: a closed connection means
// nothing is there, while silence (a server waiting for a request) or data
// (a server that speaks first) means the app is up.
func (localProbe) TCP(ctx context.Context, port int) error {
	var dialer net.Dialer
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(tcpSettle))
	_, err = conn.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		return nil
	}
	return errors.New("the connection was closed at once: nothing is listening on the app's port inside the container")
}

// tcpSettle is how long TCP waits to see whether a new connection is
// dropped.
const tcpSettle = 500 * time.Millisecond

// waitHealthy polls until the new container passes its check, exits, or the
// app's health timeout runs out.
//
//   - With a health path, the path must answer below 400.
//   - With a health command, the command must exit 0 inside the container.
//   - With neither, the container's port must accept a connection.
func (d *Deployer) waitHealthy(ctx context.Context, dk docker.Client, app db.App, container string, port int, log *Log) error {
	timeout := time.Duration(app.HealthTimeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	check := func() error { return d.Probe.TCP(ctx, port) }
	switch {
	case app.HealthPath != "":
		log.Step("Waiting for GET %s to answer (up to %s)", app.HealthPath, timeout)
		check = func() error { return d.Probe.HTTP(ctx, port, app.HealthPath) }
	case app.HealthCmd != "":
		log.Step("Waiting for the health command to pass (up to %s)", timeout)
		check = func() error { return dk.Exec(ctx, container, "sh", "-c", app.HealthCmd) }
	default:
		log.Step("Waiting for port %d to accept connections (up to %s)", app.Port, timeout)
	}

	ticker := time.NewTicker(d.healthEvery)
	defer ticker.Stop()
	var last error
	for {
		st, err := dk.State(ctx, container)
		switch {
		case err != nil && ctx.Err() == nil:
			return err
		// "restarting" is a container that exited and that Docker's restart
		// policy is bringing back: it crashed on start.
		case err == nil && st.Status != "created" && (!st.Running || st.Status == "restarting"):
			return fmt.Errorf("%w with status %d before it became healthy", errExited, st.ExitCode)
		case err == nil:
			if last = check(); last == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("the health check did not pass within %s: %v", timeout, last)
			}
			return fmt.Errorf("the health check did not pass within %s", timeout)
		case <-ticker.C:
		}
	}
}
