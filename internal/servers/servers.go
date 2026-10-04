// Package servers hands out the Runner for a server. Everything that touches
// a machine asks here, so the rest of the code never needs to know whether a
// server is this machine or one reached over SSH.
package servers

import (
	"context"
	"fmt"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// Pool returns runners for servers.
type Pool struct {
	local runner.Runner
}

// New returns a pool that can reach the local machine.
func New() *Pool {
	return &Pool{local: runner.NewLocal()}
}

// Runner returns the Runner for a server.
func (p *Pool) Runner(_ context.Context, s db.Server) (runner.Runner, error) {
	switch s.Kind {
	case db.ServerLocal:
		return p.local, nil
	}
	return nil, fmt.Errorf("server %s: kind %q is not supported yet", s.Name, s.Kind)
}

// Docker returns a docker client for a server.
func (p *Pool) Docker(ctx context.Context, s db.Server) (docker.Client, error) {
	r, err := p.Runner(ctx, s)
	if err != nil {
		return docker.Client{}, err
	}
	return docker.Client{R: r}, nil
}

// IsLocal reports whether the server is the machine musdash runs on.
func IsLocal(s db.Server) bool { return s.Kind == db.ServerLocal }
