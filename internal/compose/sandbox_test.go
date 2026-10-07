package compose

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// sandbox prepares a stack directory and the options to load from it with
// the real Docker daemon.
func sandbox(t *testing.T) (dir string, opt ConfigOptions, r runner.Runner) {
	t.Helper()
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	// Resolved: on macOS the temporary directory is reached through a
	// symlink, and Docker mounts the real path.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r = runner.NewLocal()
	version, err := docker.Client{R: r}.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opt = ConfigOptions{
		Image: SandboxImage(version), Dir: dir, EnvFile: filepath.Join(dir, "sandbox.env"), Project: "musdash-test",
		User: strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
	}
	write(t, dir, "sandbox.env", "")
	return dir, opt, r
}

// load runs the sandbox on a Compose file's text.
func load(t *testing.T, r runner.Runner, opt ConfigOptions, compose string) (Project, error) {
	t.Helper()
	opt.Source = []byte(compose)
	return Config(context.Background(), r, opt)
}

// write replaces a file the way musdash's Runner does: a new file moved
// into place, never an existing one rewritten.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

// A Compose file names other files to read. Inside the sandbox those are
// not the server's, so nothing of the server can come back.
func TestSandboxCannotReadTheServer(t *testing.T) {
	dir, opt, r := sandbox(t)
	ctx := context.Background()
	// A file of the server, outside the stack's directory, as the master
	// key or another app's variables would be.
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const marker = "TOP_SECRET_OF_THE_SERVER"
	write(t, outside, "secret.env", "KEY="+marker+"\n")
	write(t, outside, "secret.yml", "services:\n  leaked:\n    image: "+marker+"\n")
	// A file of the stack's own directory is out of reach too: a pasted
	// file is all there is to a pasted stack.
	write(t, dir, "own.env", "KEY="+marker+"\n")

	for name, compose := range map[string]string{
		"include":         "include:\n  - " + outside + "/secret.yml\nservices:\n  a:\n    image: alpine\n",
		"env_file":        "services:\n  a:\n    image: alpine\n    env_file: " + outside + "/secret.env\n",
		"extends":         "services:\n  a:\n    extends:\n      file: " + outside + "/secret.yml\n      service: leaked\n",
		"relative climb":  "services:\n  a:\n    image: alpine\n    env_file: ../" + filepath.Base(outside) + "/secret.env\n",
		"yaml escapes":    "\"\\u0069nclude\":\n  - " + outside + "/secret.yml\nservices:\n  a:\n    image: alpine\n",
		"flow style":      "{include: [" + outside + "/secret.yml], services: {a: {image: alpine}}}\n",
		"env file as doc": "include:\n  - path: " + outside + "/secret.yml\n    env_file: " + outside + "/secret.env\nservices:\n  a:\n    image: alpine\n",
		"own directory":   "services:\n  a:\n    image: alpine\n    env_file: ./own.env\n",
	} {
		p, err := load(t, r, opt, compose)
		if err == nil {
			raw, _ := p.Marshal()
			if strings.Contains(string(raw), marker) {
				t.Errorf("%s: the server's file was read into the configuration:\n%s", name, raw)
			} else {
				t.Errorf("%s: loaded without an error", name)
			}
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: the error quotes the server's file: %v", name, err)
		}
	}

	// Variables come from the file Docker's command line reads, and paths
	// are resolved against the stack's directory without it being visible.
	write(t, dir, "sandbox.env", "GREETING=hello\nHOME=/nowhere\n")
	p, err := load(t, r, opt, "services:\n  a:\n    image: alpine\n    environment:\n      - SAY=${GREETING}\n      - ESCAPED=$$HOME\n    volumes:\n      - ./conf:/conf\n")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := p.Marshal()
	for _, want := range []string{`"SAY": "hello"`, `"ESCAPED": "$$HOME"`, `"source": "` + dir + `/conf"`, `"name": "musdash-test"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the configuration is missing %s:\n%s", want, raw)
		}
	}

	// A mistake in the file is reported in Compose's words.
	if _, err := load(t, r, opt, "services:\n  a:\n    image: alpine\n    ports: nonsense\n"); err == nil || !strings.Contains(err.Error(), "ports") {
		t.Errorf("an invalid file: %v", err)
	}

	// A stack from a repository may read the files of its checkout, and
	// nothing beside it.
	checkout := filepath.Join(dir, "src")
	os.MkdirAll(filepath.Join(checkout, "deploy"), 0o755)
	write(t, checkout, "shared.env", "FROM_REPO=yes\n")
	write(t, filepath.Join(checkout, "deploy"), "compose.yaml", "services:\n  a:\n    image: alpine\n    env_file: ../shared.env\n    build: ..\n")
	git := opt
	git.Mount, git.Dir, git.File = checkout, filepath.Join(checkout, "deploy"), "compose.yaml"
	p, err = Config(ctx, r, git)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = p.Marshal()
	for _, want := range []string{`"FROM_REPO": "yes"`, `"context": "` + checkout + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("git: the configuration is missing %s:\n%s", want, raw)
		}
	}
	// What comes back names no file to read later: it can be started from
	// as it is.
	if strings.Contains(string(raw), "env_file") || strings.Contains(string(raw), "shared.env") {
		t.Errorf("the normalised configuration still refers to a file:\n%s", raw)
	}
	write(t, filepath.Join(checkout, "deploy"), "leak.yaml", "services:\n  a:\n    image: alpine\n    env_file: ../../own.env\n")
	git.File = "leak.yaml"
	if p, err := Config(ctx, r, git); err == nil || strings.Contains(err.Error(), marker) {
		raw, _ := p.Marshal()
		t.Errorf("git: a file beside the checkout was read: %v\n%s", err, raw)
	}
}

var escapedMagic = regexp.MustCompile(`\$\{?SERVICE_(FQDN|URL|HTTPS|USER|PASSWORD|BASE64|HEX)_[A-Z0-9_]+`)

// Every template of the catalogue loads, passes validation and yields the
// endpoints it is meant to have.
func TestCatalogueLoadsInTheSandbox(t *testing.T) {
	dir, opt, r := sandbox(t)
	ctx := context.Background()
	wantEndpoints := map[string]string{
		"n8n":         "N8N=n8n:5678",
		"wordpress":   "WORDPRESS=wordpress:80",
		"ghost":       "GHOST=ghost:2368",
		"uptime-kuma": "UPTIME_KUMA=uptime-kuma:3001",
		"minio":       "CONSOLE=minio:9001 MINIO=minio:9000",
		"cloudflared": "",
	}
	for _, listed := range catalog.Services() {
		t.Run(listed.Key, func(t *testing.T) {
			// Several hundred templates, two runs of the sandbox each.
			t.Parallel()
			tpl, _ := catalog.Service(listed.Key)
			vars := catalog.ScanMagic(tpl.Compose)
			var env strings.Builder
			for _, v := range vars {
				value, generated := catalog.Generate(v)
				if !generated {
					value = catalog.AddressValue(v, strings.ToLower(v.ID)+".example.com", true)
				}
				env.WriteString(v.Name + "=" + value + "\n")
			}
			for _, v := range catalog.ScanVariables(tpl.Compose) {
				if v.Required {
					env.WriteString(v.Name + "=entered-by-the-person\n")
				}
			}
			// Each template gets its own variables file: Docker Desktop is
			// slow to notice a file replaced under the same name.
			opt := opt
			opt.EnvFile = filepath.Join(dir, tpl.Key+".env")
			write(t, dir, tpl.Key+".env", env.String())
			opt.Source = []byte(tpl.Compose)

			rawOpt := opt
			rawOpt.Raw = true
			raw, err := Config(ctx, r, rawOpt)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			endpoints, err := Endpoints(raw, vars)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range endpoints {
				got = append(got, e.Name+"="+e.Service+":"+strconv.Itoa(e.Port))
			}
			// The templates written for musdash are held to the endpoints
			// they were written to have; an imported one to having worked
			// out a port for each.
			if want, ours := wantEndpoints[tpl.Key]; ours && strings.Join(got, " ") != want {
				t.Errorf("endpoints %q, want %q", strings.Join(got, " "), want)
			} else if len(endpoints) == 0 && !tpl.ConnectEnv {
				t.Errorf("no endpoint, and not connected to its environment: nothing can reach it")
			} else if ours != (tpl.Source == "") {
				t.Errorf("a template written for musdash that this test does not know, or the other way round (source %q)", tpl.Source)
			}

			resolved, err := Config(ctx, r, opt)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			err = resolved.Validate(ValidateOptions{Dir: dir, ValidPort: func(int) bool { return false }})
			if err != nil {
				t.Fatalf("the template does not pass musdash's own rules:\n%v", err)
			}
			// No variable is left unfilled, and every service has an image.
			out, _ := resolved.Marshal()
			// What is left with a dollar sign in front of it was escaped
			// in the file ("$$"), which is right for a container's own
			// shell and wrong for a variable musdash was meant to fill.
			if m := escapedMagic.FindString(string(out)); m != "" {
				t.Errorf("a magic variable is escaped, so it is never filled in: %s", m)
			}
			// Compose reads a config with empty content when it loads the
			// file and refuses it when it starts the stack.
			for name, c := range asMap(resolved.doc["configs"]) {
				if cfg := asMap(c); cfg["file"] == nil && cfg["environment"] == nil && cfg["content"] == "" {
					t.Errorf("the config %s has no content, which Compose refuses when the stack is started", name)
				}
			}
			for _, s := range resolved.Services() {
				if resolved.Image(s) == "" {
					t.Errorf("service %s has no image", s)
				}
			}
		})
	}
}

// A volume with a one-letter name, mounted in short syntax: Compose takes
// the letter and the colon for a Windows drive, so the mount it hands on has
// no volume and a target that is not a path, and the volume's own entry
// (with its "external") is gone because nothing uses it. That must be
// refused here, in words about the file, not later by Docker.
func TestSandboxOneLetterVolume(t *testing.T) {
	dir, opt, r := sandbox(t)
	rules := ValidateOptions{Dir: dir, ValidPort: func(int) bool { return false }}
	stack := func(volume string) string {
		return "services:\n  a:\n    image: alpine\n    volumes:\n      - " + volume + ":/x\nvolumes:\n  " + volume + ":\n    external: true\n"
	}
	p, err := load(t, r, opt, stack("v"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(rules); err == nil || !strings.Contains(err.Error(), "one-letter name") {
		raw, _ := p.Marshal()
		t.Errorf("a one-letter volume: %v\n%s", err, raw)
	}
	// With a name Compose reads as a name, the volume is there and what is
	// wrong with it is said.
	p, err = load(t, r, opt, stack("vol"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(rules); err == nil || !strings.Contains(err.Error(), `"name" and "external" are not allowed`) {
		t.Errorf("an external volume: %v", err)
	}
}
