// Package docker drives the docker CLI through a Runner. It imports no Docker
// SDK: the CLI is already on every server, costs memory only while a command
// runs, and behaves the same locally and over SSH.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// ManagedLabel marks every container musdash creates. The status monitor
// filters on it, so containers a person runs by hand are never touched.
const ManagedLabel = "musdash.managed"

// Label keys that tie a container back to its resource.
const (
	LabelKind       = "musdash.kind"
	LabelResource   = "musdash.resource"
	LabelDeployment = "musdash.deployment"
)

// Client runs docker commands on one server.
type Client struct {
	R runner.Runner
}

func cmd(args ...string) runner.Cmd { return runner.Cmd{Name: "docker", Args: args} }

// Pull downloads an image, streaming progress to log.
func (c Client) Pull(ctx context.Context, image string, log io.Writer) error {
	if !ValidImage(image) {
		return fmt.Errorf("%q is not a valid image name", image)
	}
	cm := cmd("pull", image)
	cm.Stdout, cm.Stderr = log, log
	return c.R.Run(ctx, cm)
}

// ErrPortTaken is returned by Run when the host port is already in use.
var ErrPortTaken = errors.New("host port is already in use")

// Run starts a container in the background and returns its name.
func (c Client) Run(ctx context.Context, spec RunSpec) error {
	args, err := spec.Args()
	if err != nil {
		return err
	}
	_, err = c.R.Output(ctx, cmd(args...))
	var ee *runner.ExitError
	if errors.As(err, &ee) && (strings.Contains(ee.Stderr, "port is already allocated") || strings.Contains(ee.Stderr, "address already in use")) {
		return ErrPortTaken
	}
	return err
}

// State is the part of `docker inspect` the deploy pipeline needs.
type State struct {
	Status   string // created, running, restarting, exited, dead
	Running  bool
	ExitCode int
	Health   string // "", starting, healthy, unhealthy
}

// ErrNoContainer is returned when the named container does not exist.
var ErrNoContainer = errors.New("no such container")

// State inspects a container.
func (c Client) State(ctx context.Context, container string) (State, error) {
	out, err := c.R.Output(ctx, cmd("inspect", "--type", "container", "--format", "{{json .State}}", container))
	if err != nil {
		var ee *runner.ExitError
		if errors.As(err, &ee) && strings.Contains(strings.ToLower(ee.Stderr), "no such") {
			return State{}, ErrNoContainer
		}
		return State{}, err
	}
	var raw struct {
		Status   string
		Running  bool
		ExitCode int
		Health   *struct{ Status string }
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return State{}, fmt.Errorf("docker inspect: %w", err)
	}
	st := State{Status: raw.Status, Running: raw.Running, ExitCode: raw.ExitCode}
	if raw.Health != nil {
		st.Health = raw.Health.Status
	}
	return st, nil
}

// Stop asks a container to exit, killing it after the grace period. A
// container that is already gone is not an error.
func (c Client) Stop(ctx context.Context, container string, grace time.Duration) error {
	_, err := c.R.Output(ctx, cmd("stop", "--time", strconv.Itoa(int(grace.Seconds())), container))
	return ignoreMissing(err)
}

// Remove deletes a container, running or not. A container that is already
// gone is not an error.
func (c Client) Remove(ctx context.Context, container string) error {
	_, err := c.R.Output(ctx, cmd("rm", "--force", container))
	return ignoreMissing(err)
}

func ignoreMissing(err error) error {
	var ee *runner.ExitError
	if errors.As(err, &ee) && strings.Contains(strings.ToLower(ee.Stderr), "no such") {
		return nil
	}
	return err
}

// EnsureNetwork creates a bridge network unless it already exists.
func (c Client) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := c.R.Output(ctx, cmd("network", "inspect", "--format", "{{.Name}}", name)); err == nil {
		return nil
	}
	_, err := c.R.Output(ctx, cmd("network", "create", "--label", ManagedLabel+"=true", name))
	var ee *runner.ExitError
	if errors.As(err, &ee) && strings.Contains(ee.Stderr, "already exists") {
		return nil // created by a concurrent deploy
	}
	return err
}

// RemoveVolume deletes a volume and the data in it. A volume that does not
// exist is not an error.
func (c Client) RemoveVolume(ctx context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("bad volume name %q", name)
	}
	_, err := c.R.Output(ctx, cmd("volume", "rm", name))
	return ignoreMissing(err)
}

// Logs streams a container's output to w. With follow it returns only when
// ctx is cancelled or the container stops.
func (c Client) Logs(ctx context.Context, container string, tail int, follow bool, w io.Writer) error {
	args := []string{"logs", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "--follow")
	}
	cm := cmd(append(args, container)...)
	cm.Stdout, cm.Stderr = w, w
	return c.R.Run(ctx, cm)
}

// Events streams lifecycle events of managed containers to w, one JSON
// object per line, until ctx is cancelled.
func (c Client) Events(ctx context.Context, w io.Writer) error {
	cm := cmd("events", "--filter", "type=container", "--filter", "label="+ManagedLabel+"=true", "--format", "{{json .}}")
	cm.Stdout = w
	return c.R.Run(ctx, cm)
}

// Exec runs a command inside a container and reports whether it exited 0.
func (c Client) Exec(ctx context.Context, container string, argv ...string) error {
	_, err := c.R.Output(ctx, cmd(append([]string{"exec", container}, argv...)...))
	return err
}

// Version returns the Docker server version, which doubles as a check that
// the daemon is reachable.
func (c Client) Version(ctx context.Context) (string, error) {
	out, err := c.R.Output(ctx, cmd("version", "--format", "{{.Server.Version}}"))
	return strings.TrimSpace(string(out)), err
}

// Listed is one row of List.
type Listed struct {
	Name       string
	State      string // running, exited, …
	Kind       string
	Resource   string
	Deployment string
}

// List returns every managed container, running or not.
func (c Client) List(ctx context.Context) ([]Listed, error) {
	format := "{{.Names}}\t{{.State}}\t{{.Label \"" + LabelKind + "\"}}\t{{.Label \"" + LabelResource + "\"}}\t{{.Label \"" + LabelDeployment + "\"}}"
	out, err := c.R.Output(ctx, cmd("ps", "--all", "--filter", "label="+ManagedLabel+"=true", "--format", format))
	if err != nil {
		return nil, err
	}
	var list []Listed
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			continue
		}
		list = append(list, Listed{Name: f[0], State: f[1], Kind: f[2], Resource: f[3], Deployment: f[4]})
	}
	return list, nil
}
