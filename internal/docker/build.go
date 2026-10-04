package docker

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// BuildSpec describes an image build.
type BuildSpec struct {
	Tag        string // image reference to tag the result with
	ContextDir string // absolute path of the build context on the server
	Dockerfile string // absolute path of the Dockerfile
	// BuildArgs are passed by name only (--build-arg NAME). Docker then
	// takes each value from its own environment, which keeps secrets off the
	// command line where `ps` would show them.
	BuildArgs map[string]string
}

var argNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Cmd builds the `docker build` command: the argument vector and the
// environment that carries the build-arg values.
func (s BuildSpec) Cmd() (runner.Cmd, error) {
	if !ValidImage(s.Tag) {
		return runner.Cmd{}, fmt.Errorf("%q is not a valid image tag", s.Tag)
	}
	if !ValidMountPath(s.ContextDir) || !ValidMountPath(s.Dockerfile) {
		return runner.Cmd{}, fmt.Errorf("build paths must be absolute")
	}
	args := []string{"build", "--progress", "plain", "--tag", s.Tag, "--file", s.Dockerfile, "--label", ManagedLabel + "=true"}
	env := []string{"DOCKER_BUILDKIT=1"}
	names := make([]string, 0, len(s.BuildArgs))
	for name := range s.BuildArgs {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		value := s.BuildArgs[name]
		if !argNameRE.MatchString(name) {
			return runner.Cmd{}, fmt.Errorf("build argument name %q is not valid", name)
		}
		if strings.ContainsAny(value, "\x00") {
			return runner.Cmd{}, fmt.Errorf("build argument %s contains a NUL byte", name)
		}
		args = append(args, "--build-arg", name)
		env = append(env, name+"="+value)
	}
	// "--" ends the options, so the context path is never read as one.
	args = append(args, "--", s.ContextDir)
	return runner.Cmd{Name: "docker", Args: args, Env: env}, nil
}

// Build builds an image, streaming the build output to log.
func (c Client) Build(ctx context.Context, spec BuildSpec, log io.Writer) error {
	cm, err := spec.Cmd()
	if err != nil {
		return err
	}
	cm.Stdout, cm.Stderr = log, log
	return c.R.Run(ctx, cm)
}

// ImageTags returns the tags of a repository's local images, newest first.
func (c Client) ImageTags(ctx context.Context, repository string) ([]string, error) {
	if !ValidImage(repository) || strings.ContainsAny(repository, ":@") {
		return nil, fmt.Errorf("%q is not a valid image repository", repository)
	}
	// `docker images` lists newest first.
	out, err := c.R.Output(ctx, cmd("images", "--format", "{{.Tag}}", repository))
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && line != "<none>" {
			tags = append(tags, line)
		}
	}
	return tags, nil
}

// RemoveImage deletes a local image tag. An image still used by a container
// is left alone, and a missing image is not an error.
func (c Client) RemoveImage(ctx context.Context, ref string) error {
	if !ValidImage(ref) {
		return fmt.Errorf("%q is not a valid image name", ref)
	}
	_, err := c.R.Output(ctx, cmd("rmi", ref))
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no such image") || strings.Contains(msg, "is being used") || strings.Contains(msg, "is using its referenced image") {
		return nil
	}
	return err
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
