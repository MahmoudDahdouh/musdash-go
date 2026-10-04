package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
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

func (localProbe) TCP(ctx context.Context, port int) error {
	var dialer net.Dialer
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	return conn.Close()
}

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
		case err == nil && !st.Running && st.Status != "created":
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
