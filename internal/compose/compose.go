// Package compose runs stacks described by Docker Compose files.
//
// musdash does not parse YAML. `docker compose config --format json` turns
// a Compose file into a normalised document: every short form expanded,
// every path absolute, every variable filled in. That document is what this
// package reads, checks, amends and finally hands back to Compose to start.
//
// Loading a Compose file reads other files: include, extends and env_file
// name them. On the server those could be any file musdash can read, such
// as its master key or another app's variables, and their content would
// surface in an error message or in a container's environment. So the
// loading happens inside a throwaway container that can see the stack's own
// directory and nothing else; see Config.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// Project is a normalised Compose document. It is kept as generic JSON so
// that what is written back is what Compose produced, changed only where
// musdash changes it on purpose.
type Project struct {
	doc map[string]any
}

// Parse reads the output of `docker compose config --format json`.
func Parse(raw []byte) (Project, error) {
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as written: a port or a size must not pass through a
	// float on its way back to Compose.
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return Project{}, fmt.Errorf("read the Compose configuration: %w", err)
	}
	if doc == nil {
		return Project{}, errors.New("the Compose configuration is empty")
	}
	return Project{doc: doc}, nil
}

// Marshal writes the document in a form `docker compose -f` reads: JSON is
// YAML.
func (p Project) Marshal() ([]byte, error) {
	return json.MarshalIndent(p.doc, "", "  ")
}

// Name is the Compose project name.
func (p Project) Name() string {
	name, _ := p.doc["name"].(string)
	return name
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// Services returns the names of the project's services, sorted.
func (p Project) Services() []string {
	services := asMap(p.doc["services"])
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p Project) service(name string) map[string]any {
	return asMap(asMap(p.doc["services"])[name])
}

// Image is a service's image, or "" when it has none.
func (p Project) Image(service string) string {
	image, _ := p.service(service)["image"].(string)
	return image
}

// FirstPort is the first container port a service declares, through expose
// or ports, or 0.
func (p Project) FirstPort(service string) int {
	svc := p.service(service)
	for _, e := range asList(svc["expose"]) {
		if n := toInt(e); n > 0 {
			return n
		}
	}
	for _, e := range asList(svc["ports"]) {
		switch port := e.(type) {
		case map[string]any:
			if n := toInt(port["target"]); n > 0 {
				return n
			}
		case string:
			// The short form, as kept by --no-interpolate: the container
			// port is the last number.
			spec, _, _ := strings.Cut(port, "/")
			if n := toInt(spec[strings.LastIndexByte(spec, ':')+1:]); n > 0 {
				return n
			}
		}
	}
	return 0
}

func toInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil && fmt.Sprint(i) == n {
			return i
		}
	}
	return 0
}

// Mentions reports whether a service's definition contains the text, in a
// key or a value. With a document loaded without interpolation this finds
// which services use a variable.
func (p Project) Mentions(service, text string) bool {
	raw, err := json.Marshal(p.service(service))
	return err == nil && bytes.Contains(raw, []byte(text))
}

// SandboxImage is the image whose Compose loads files for musdash: the
// Docker command line of the server's own major version, so what it prints
// is what the server's Compose reads.
func SandboxImage(dockerVersion string) string {
	major, _, _ := strings.Cut(strings.TrimSpace(dockerVersion), ".")
	for _, c := range major {
		if c < '0' || c > '9' {
			major = ""
			break
		}
	}
	if major == "" {
		return "docker:cli"
	}
	return "docker:" + major + "-cli"
}

// ResolvedFile is the file in a stack's directory that the stack is started
// from: the normalised document, checked and amended. It holds the stack's
// secrets and is kept private.
const ResolvedFile = "compose.resolved.json"

// ConfigOptions describes one load of a Compose file.
type ConfigOptions struct {
	// Image is the sandbox image; see SandboxImage.
	Image string
	// Project is the Compose project name.
	Project string
	// Dir is where relative paths in the file start from: the stack's own
	// directory on the server. It is not shown to the sandbox unless it
	// lies inside Mount, so a path in the file can be resolved against it
	// but nothing in it can be read.
	Dir string
	// Source is the Compose file's text, fed on standard input. A file a
	// person pasted, or one from the catalogue, needs nothing else.
	Source []byte
	// Mount and File are used instead of Source for a stack that lives in
	// a Git repository: Mount is the checkout, shown read-only at the same
	// path as on the server so that paths in the result are the server's,
	// and File is the Compose file inside Dir.
	Mount string
	File  string
	// EnvFile is a file on the server holding the variables to fill in, in
	// the format of `docker run --env-file`. Docker's command line reads
	// it; the sandbox does not see the file itself.
	EnvFile string
	// Raw keeps variable references as written instead of filling them in.
	Raw bool
	// User is the "uid:gid" the sandbox runs as: whoever owns the checkout.
	User string
}

func cleanAbs(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, ",\n\x00")
}

// ConfigCmd is the command that loads a Compose file inside the sandbox.
// The container has no network, no Docker socket, no capabilities and a
// read-only filesystem.
func ConfigCmd(o ConfigOptions) (runner.Cmd, error) {
	if o.Image == "" || o.Project == "" || o.User == "" {
		return runner.Cmd{}, errors.New("compose: image, project and user are required")
	}
	if !cleanAbs(o.Dir) || !cleanAbs(o.EnvFile) {
		return runner.Cmd{}, fmt.Errorf("compose: bad directory %q or variables file %q", o.Dir, o.EnvFile)
	}
	args := []string{
		"run", "--rm", "--interactive",
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		// A file written to make Compose work hard gets little to do it
		// with.
		"--memory", "256m",
		"--pids-limit", "128",
		"--user", o.User,
		"--tmpfs", "/tmp",
		"--workdir", "/tmp",
		"--env-file", o.EnvFile,
		// After the variables file, so a variable of the same name cannot
		// point Compose's own bookkeeping somewhere else.
		"--env", "HOME=/tmp",
		"--env", "DOCKER_CONFIG=/tmp/.docker",
	}
	file := "-"
	if o.Mount != "" {
		if !cleanAbs(o.Mount) || (o.Dir != o.Mount && !strings.HasPrefix(o.Dir, o.Mount+"/")) {
			return runner.Cmd{}, fmt.Errorf("compose: %q is not inside the checkout %q", o.Dir, o.Mount)
		}
		if f := o.File; f == "" || path.IsAbs(f) || path.Clean(f) != f || f == ".." || strings.HasPrefix(f, "../") {
			return runner.Cmd{}, fmt.Errorf("compose: bad file name %q", o.File)
		}
		// Absolute, so it does not depend on where the sandbox happens to
		// start, and cannot be read as an option.
		file = path.Join(o.Dir, o.File)
		args = append(args, "--mount", "type=bind,source="+o.Mount+",target="+o.Mount+",readonly")
	} else if len(o.Source) == 0 {
		return runner.Cmd{}, errors.New("compose: nothing to load")
	}
	args = append(args, o.Image,
		"compose", "--project-name", o.Project, "--project-directory", o.Dir, "--file", file,
		"config", "--format", "json")
	if o.Raw {
		args = append(args, "--no-interpolate")
	}
	cmd := runner.Cmd{Name: "docker", Args: args}
	if o.Mount == "" {
		cmd.Stdin = bytes.NewReader(o.Source)
	}
	return cmd, nil
}

const configTimeout = 3 * time.Minute

// Config loads a Compose file inside the sandbox and returns the normalised
// document. An error from Compose is returned as Compose worded it: it is
// what tells a person which line of their file is wrong.
func Config(ctx context.Context, r runner.Runner, o ConfigOptions) (Project, error) {
	cmd, err := ConfigCmd(o)
	if err != nil {
		return Project{}, err
	}
	// Long enough for the sandbox image to be fetched the first time.
	ctx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	out, err := r.Output(ctx, cmd)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Project{}, fmt.Errorf("reading the Compose file took longer than %s", configTimeout)
		}
		var ee *runner.ExitError
		if errors.As(err, &ee) && strings.TrimSpace(ee.Stderr) != "" {
			return Project{}, errors.New(cleanError(ee.Stderr))
		}
		return Project{}, fmt.Errorf("load the Compose file: %w", err)
	}
	return Parse(out)
}

// cleanError keeps the part of Compose's output that explains a failure:
// the last few lines, without the progress lines Docker prints when it has
// to fetch the sandbox image first.
func cleanError(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	msg := strings.Join(lines, "\n")
	if len(msg) > 1500 {
		msg = msg[len(msg)-1500:]
	}
	return msg
}
