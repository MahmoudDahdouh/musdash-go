package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MahmoudDahdouh/musdash-go/internal/netguard"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"net"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// S3 storage is reached with rclone, run in a container for the length of
// one command. Nothing stays resident, and musdash carries no S3 client.
// The keys reach rclone through an env file that Docker's command line
// reads and that is removed afterwards; they are in no argument list.

// EnvFilePrefix starts the name of the file that holds a storage's keys
// while a command runs. One left behind by a process that died is removed
// at start-up by this prefix.
const EnvFilePrefix = ".rclone-"

// RcloneImage is the image the S3 commands run in.
const RcloneImage = "rclone/rclone:1"

// S3 is one S3-compatible bucket, with its keys opened.
type S3 struct {
	Endpoint  string // "https://s3.eu-central-1.amazonaws.com"; empty means AWS's default
	Region    string
	Bucket    string
	Prefix    string // folder inside the bucket; may be empty
	AccessKey string
	SecretKey string

	// Lookup resolves the endpoint's host name; nil uses the system's
	// resolver. Tests set it.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// AllowLocal lets the endpoint be the server itself. Tests against a
	// storage server on this machine set it.
	AllowLocal bool
}

// pin checks the address the endpoint leads to and returns the Docker
// options that make rclone connect to exactly that address.
//
// The endpoint is typed by a team member. Without the check it could name
// the server's own services, a container's private address or a cloud
// metadata service, and rclone would send signed requests there. The name
// is resolved here and handed to the container already resolved, so it
// cannot answer differently a moment later.
func (s S3) pin(ctx context.Context) ([]string, error) {
	if s.AllowLocal {
		return []string{"--add-host", "host.docker.internal:host-gateway"}, nil
	}
	if s.Endpoint == "" {
		return nil, nil // Amazon's own addresses
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, errors.New("the endpoint is not an address")
	}
	host := u.Hostname()
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p, err := strconv.Atoi(u.Port()); err == nil {
		port = p
	}
	var policy netguard.Policy
	if ip, err := netip.ParseAddr(host); err == nil {
		return nil, policy.Check(ip, port)
	}
	lookup := s.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("the endpoint's name %s could not be looked up", host)
	}
	chosen := addrs[0].Unmap()
	for _, a := range addrs {
		a = a.Unmap()
		if err := policy.Check(a, port); err != nil {
			return nil, err
		}
		// Docker's own bridge network has no IPv6 unless it was set up.
		if a.Is4() && !chosen.Is4() {
			chosen = a
		}
	}
	target := chosen.String()
	if chosen.Is6() {
		target = "[" + target + "]"
	}
	return []string{"--add-host", host + ":" + target}, nil
}

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRE = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
	prefixRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
	objectRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,200}$`)
)

// Validate checks a storage's settings as a person typed them.
func (s S3) Validate() error {
	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("the endpoint must be an address such as https://s3.eu-central-1.amazonaws.com, without a path")
		}
	}
	if !regionRE.MatchString(s.Region) {
		return errors.New("the region may contain lowercase letters, numbers and hyphens, such as eu-central-1")
	}
	if !bucketRE.MatchString(s.Bucket) || strings.Contains(s.Bucket, "..") {
		return errors.New("the bucket name must be 3 to 63 lowercase letters, numbers, dots or hyphens")
	}
	if s.Prefix != "" && (!prefixRE.MatchString(s.Prefix) || strings.Contains(s.Prefix, "..") || len(s.Prefix) > 200) {
		return errors.New("the folder may contain letters, numbers, dots, hyphens and underscores, with / between its parts")
	}
	for name, v := range map[string]string{"access key": s.AccessKey, "secret key": s.SecretKey} {
		if v == "" || strings.ContainsAny(v, "\r\n\x00") || len(v) > 500 {
			return fmt.Errorf("the %s is missing or not on one line", name)
		}
	}
	return nil
}

// remote is the rclone path of an object, or of the folder when name is
// empty.
func (s S3) remote(name string) string {
	return "s3:" + path.Join(s.Bucket, s.Prefix, name)
}

// envFile is rclone's configuration as environment variables.
func (s S3) envFile() string {
	vars := map[string]string{
		"RCLONE_CONFIG_S3_TYPE":              "s3",
		"RCLONE_CONFIG_S3_PROVIDER":          "Other",
		"RCLONE_CONFIG_S3_ACCESS_KEY_ID":     s.AccessKey,
		"RCLONE_CONFIG_S3_SECRET_ACCESS_KEY": s.SecretKey,
		// Do not try to create the bucket: the key may not be allowed to,
		// and a mistyped name should be an error, not a new bucket.
		"RCLONE_S3_NO_CHECK_BUCKET": "true",
	}
	if s.Endpoint != "" {
		vars["RCLONE_CONFIG_S3_ENDPOINT"] = strings.TrimRight(s.Endpoint, "/")
	} else {
		vars["RCLONE_CONFIG_S3_PROVIDER"] = "AWS"
	}
	if s.Region != "" {
		vars["RCLONE_CONFIG_S3_REGION"] = s.Region
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + vars[k] + "\n")
	}
	return b.String()
}

// run executes one rclone command. workDir is a private directory on the
// server for the env file; mount, when set, is a directory shown read-only
// at /backup.
func (s S3) run(ctx context.Context, r runner.Runner, workDir, mount string, args ...string) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	pinned, err := s.pin(ctx)
	if err != nil {
		return nil, err
	}
	// A name of its own for each run: two runs in one directory, such as an
	// upload and a delete, must not remove or read each other's keys.
	envPath := path.Join(workDir, EnvFilePrefix+secret.RandomID()+".env")
	if err := r.WriteFile(ctx, envPath, 0o600, strings.NewReader(s.envFile())); err != nil {
		return nil, fmt.Errorf("write the storage keys: %w", err)
	}
	defer func() {
		// Also when the command was cancelled.
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		r.RemoveAll(clean, envPath)
	}()
	full := append([]string{"run", "--rm", "--env-file", envPath}, pinned...)
	if mount != "" {
		full = append(full, "--mount", "type=bind,source="+mount+",target=/backup,readonly")
	}
	full = append(full, RcloneImage)
	full = append(full, args...)
	full = append(full, "--retries", "2", "--low-level-retries", "3", "--contimeout", "15s", "--timeout", "60s")
	out, err := r.Output(ctx, runner.Cmd{Name: "docker", Args: full})
	if err != nil {
		var ee *runner.ExitError
		if errors.As(err, &ee) && strings.TrimSpace(ee.Stderr) != "" {
			return nil, errors.New(rcloneError(ee.Stderr))
		}
		return nil, err
	}
	return out, nil
}

// rcloneError keeps the last lines of rclone's complaints.
func rcloneError(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	msg := strings.Join(lines, " ")
	if len(msg) > 600 {
		msg = msg[len(msg)-600:]
	}
	return msg
}

// Upload copies a backup file from dir on the server to the storage under
// the same name.
func (s S3) Upload(ctx context.Context, r runner.Runner, dir, name string) error {
	if !objectRE.MatchString(name) {
		return fmt.Errorf("bad backup file name %q", name)
	}
	if !path.IsAbs(dir) || path.Clean(dir) != dir || strings.ContainsAny(dir, ",\n") {
		return fmt.Errorf("bad backup directory %q", dir)
	}
	_, err := s.run(ctx, r, dir, dir, "copyto", "/backup/"+name, s.remote(name))
	if err != nil {
		return fmt.Errorf("upload to %s: %w", s.Bucket, err)
	}
	return nil
}

// Object is one file in the storage.
type Object struct {
	Name string
	Size int64
}

// List returns the files in the storage's folder, by name.
func (s S3) List(ctx context.Context, r runner.Runner, workDir string) ([]Object, error) {
	out, err := s.run(ctx, r, workDir, "", "lsjson", "--files-only", "--no-modtime", "--no-mimetype", s.remote(""))
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", s.Bucket, err)
	}
	var raw []struct {
		Name string
		Size int64
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("list %s: unexpected answer from rclone: %w", s.Bucket, err)
	}
	objects := make([]Object, 0, len(raw))
	for _, o := range raw {
		objects = append(objects, Object{Name: o.Name, Size: o.Size})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	return objects, nil
}

// Delete removes one file from the storage. A file that is not there is not
// an error.
func (s S3) Delete(ctx context.Context, r runner.Runner, workDir, name string) error {
	if !objectRE.MatchString(name) {
		return fmt.Errorf("bad backup file name %q", name)
	}
	_, err := s.run(ctx, r, workDir, "", "deletefile", s.remote(name))
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		return fmt.Errorf("delete %s from %s: %w", name, s.Bucket, err)
	}
	return nil
}

// Check lists the folder once, which fails for a wrong endpoint, bucket or
// key.
func (s S3) Check(ctx context.Context, r runner.Runner, workDir string) error {
	_, err := s.List(ctx, r, workDir)
	return err
}
