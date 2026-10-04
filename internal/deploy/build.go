package deploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// Build packs.
const (
	PackDockerfile = "dockerfile"
	PackStatic     = "static"
)

// keepImages is how many built images of an app stay on the server. Older
// ones are removed after each successful build so the disk does not fill;
// the ones kept are what a rollback can return to.
const keepImages = 5

// TokenSource mints short-lived repository tokens for a GitHub App.
type TokenSource interface {
	InstallationToken(ctx context.Context, appID int64, key []byte, owner, repo string) (string, error)
}

// ImageRepository is the local image name an app's builds are tagged under.
func ImageRepository(appID string) string { return "musdash/" + appID }

// gitEnv is the environment every git command runs with: never prompt, and
// ignore whatever git configuration exists on the server.
func gitEnv() []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
}

// cloneAccess works out the address to clone and how git authenticates.
//
// Credentials travel in git's environment. They are never put in the
// address or on the command line, where the process list, the deployment
// log or the checkout's .git/config would expose them.
func (d *Deployer) cloneAccess(ctx context.Context, r runner.Runner, app db.App, repo source.Repo, workDir string) (url string, env []string, err error) {
	env = gitEnv()
	switch {
	case app.GitSourceID != "":
		src, err := d.DB.GitSourceByID(ctx, app.GitSourceID)
		if err != nil {
			return "", nil, fmt.Errorf("the GitHub App this app deploys through no longer exists")
		}
		key, err := d.Box.Open(src.PrivateKey)
		if err != nil {
			return "", nil, fmt.Errorf("the GitHub App's key cannot be decrypted: was the master key changed?")
		}
		// The token is GitHub's; it is sent to GitHub and nowhere else,
		// whatever address the app was saved with.
		if repo.Host != "github.com" {
			return "", nil, fmt.Errorf("a GitHub App can only read repositories on github.com, not %s", repo.Host)
		}
		token, err := d.Tokens.InstallationToken(ctx, src.AppID, key, repo.Owner, repo.Name)
		if errors.Is(err, source.ErrNotInstalled) {
			return "", nil, fmt.Errorf("the GitHub App %q has no access to %s: install it on that repository", src.Name, repo.FullName())
		}
		if err != nil {
			return "", nil, err
		}
		// An App authenticates over HTTPS whatever form the address was
		// entered in. The header is scoped to the one host, so a redirect
		// elsewhere does not carry the token.
		base := "https://" + repo.Host + "/"
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+base+".extraHeader",
			"GIT_CONFIG_VALUE_0="+source.BasicAuthHeader(token),
		)
		return base + repo.FullName() + ".git", env, nil

	case app.SSHKeyID != "":
		key, err := d.DB.SSHKeyByID(ctx, app.SSHKeyID)
		if err != nil {
			return "", nil, fmt.Errorf("the deploy key this app clones with no longer exists")
		}
		private, err := d.Box.Open(key.PrivateKey)
		if err != nil {
			return "", nil, fmt.Errorf("the deploy key cannot be decrypted: was the master key changed?")
		}
		keyPath := path.Join(workDir, "deploy-key")
		if err := r.WriteFile(ctx, keyPath, 0o600, strings.NewReader(string(private))); err != nil {
			return "", nil, err
		}
		// GIT_SSH_COMMAND is read by a shell, so the paths are quoted. The
		// first connection to a host records its key; a later change of that
		// key fails the clone rather than being accepted.
		env = append(env, "GIT_SSH_COMMAND="+runner.QuoteJoin("ssh",
			"-i", keyPath,
			"-o", "IdentitiesOnly=yes",
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile="+path.Join(d.Cfg.DataDir, "known_hosts"),
		))
		return repo.URL, env, nil
	}
	if repo.SSH {
		return "", nil, errors.New("this repository address uses SSH; choose a deploy key for the app")
	}
	return repo.URL, append(env, d.extraGitEnv...), nil
}

// build clones the app's repository on its server and builds an image from
// it. It returns the image tag and the commit that was built. The checkout
// is removed afterwards whether the build worked or not.
func (d *Deployer) build(ctx context.Context, r runner.Runner, app db.App, dep db.Deployment, log *Log) (image, commit string, err error) {
	repo, err := source.ParseRepo(app.RepoURL)
	if err != nil {
		return "", "", err
	}
	if !source.ValidBranch(app.Branch) {
		return "", "", fmt.Errorf("%q is not a valid branch name", app.Branch)
	}
	for name, p := range map[string]string{"base directory": app.BaseDir, "Dockerfile path": app.DockerfilePath, "publish directory": app.PublishDir} {
		if !source.ValidRelPath(p) {
			return "", "", fmt.Errorf("the %s %q must be a path inside the repository", name, p)
		}
	}

	workDir := path.Join(d.Cfg.WorkDir(), dep.ID)
	checkout := path.Join(workDir, "src")
	if err := r.RemoveAll(ctx, workDir); err != nil {
		return "", "", err
	}
	if err := r.MkdirAll(ctx, workDir, 0o700); err != nil {
		return "", "", err
	}
	defer func() {
		// Also on failure and on shutdown: a checkout may hold a deploy key.
		clean := context.WithoutCancel(ctx)
		if rerr := r.RemoveAll(clean, workDir); rerr != nil {
			d.Log.Error("remove build directory", "dir", workDir, "err", rerr)
		}
	}()

	url, env, err := d.cloneAccess(ctx, r, app, repo, workDir)
	if err != nil {
		return "", "", err
	}

	log.Step("Cloning %s (branch %s)", repo.FullName(), app.Branch)
	clone := runner.Cmd{
		Name: "git",
		// "--" ends the options: the address and the directory are operands.
		Args:   []string{"clone", "--depth", "1", "--single-branch", "--no-tags", "--branch", app.Branch, "--", url, checkout},
		Env:    env,
		Stdout: log, Stderr: log,
	}
	// Bounded: a host that accepts the connection and then stalls would
	// otherwise hold the server's build lock for good.
	cloneCtx, cancelClone := context.WithTimeout(ctx, d.cloneTimeout)
	err = r.Run(cloneCtx, clone)
	cancelClone()
	if err != nil {
		if cloneCtx.Err() != nil && ctx.Err() == nil {
			return "", "", fmt.Errorf("clone %s: stopped after %s without finishing", repo.FullName(), d.cloneTimeout)
		}
		return "", "", fmt.Errorf("clone %s: %w", repo.FullName(), err)
	}

	head, err := r.Output(ctx, runner.Cmd{Name: "git", Args: []string{"-C", checkout, "rev-parse", "HEAD"}, Env: gitEnv()})
	if err != nil {
		return "", "", fmt.Errorf("read the checked-out commit: %w", err)
	}
	commit = strings.TrimSpace(string(head))
	if len(commit) < 12 || strings.Trim(commit, "0123456789abcdef") != "" {
		return "", "", fmt.Errorf("git reported an unexpected commit id %q", commit)
	}
	log.Step("Checked out %s", commit[:12])

	contextDir := path.Join(checkout, app.BaseDir)
	var dockerfile string
	switch app.BuildPack {
	case PackStatic:
		if err := d.refuseSymlinks(ctx, r, checkout, joinRel(app.BaseDir, app.PublishDir)); err != nil {
			return "", "", err
		}
		dockerfile = path.Join(workDir, "Dockerfile.static")
		if err := r.WriteFile(ctx, dockerfile, 0o600, strings.NewReader(StaticDockerfile(app.PublishDir, app.SPAFallback))); err != nil {
			return "", "", err
		}
		log.Step("Building a static site from %s", orRoot(joinRel(app.BaseDir, app.PublishDir)))
	default:
		rel := app.DockerfilePath
		if rel == "" {
			rel = "Dockerfile"
		}
		if err := d.refuseSymlinks(ctx, r, checkout, joinRel(app.BaseDir, rel)); err != nil {
			return "", "", err
		}
		dockerfile = path.Join(contextDir, rel)
		log.Step("Building with %s", joinRel(app.BaseDir, rel))
	}

	buildArgs, err := d.buildArgs(ctx, app)
	if err != nil {
		return "", "", err
	}
	image = ImageRepository(app.ID) + ":" + commit[:12]
	dk := docker.Client{R: r}
	// Bounded for the same reason as the clone: one build that never ends
	// would keep every other build on the server waiting.
	buildCtx, cancelBuild := context.WithTimeout(ctx, d.buildTimeout)
	err = dk.Build(buildCtx, docker.BuildSpec{Tag: image, ContextDir: contextDir, Dockerfile: dockerfile, BuildArgs: buildArgs}, log)
	cancelBuild()
	if err != nil {
		if buildCtx.Err() != nil && ctx.Err() == nil {
			return "", "", fmt.Errorf("build: stopped after %s without finishing", d.buildTimeout)
		}
		return "", "", fmt.Errorf("build: %w", err)
	}
	log.Step("Built %s", image)
	return image, commit, nil
}

// refuseSymlinks fails when rel, or any directory on the way to it, is a
// symbolic link in the repository. The build reads these paths as the
// musdash user: a repository whose "Dockerfile" or build directory links to
// a file or directory on the server would otherwise pull the server's own
// files (the master key, other apps' secrets) into the build log or image.
func (d *Deployer) refuseSymlinks(ctx context.Context, r runner.Runner, checkout, rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	// Only the path's own components are looked at, one entry each. Links
	// elsewhere in the repository are harmless: Docker copies a link inside
	// the build context as a link and does not follow it.
	parts := strings.Split(rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		out, err := r.Output(ctx, runner.Cmd{Name: "git", Args: []string{"-C", checkout, "ls-tree", "HEAD", "--", prefix}, Env: gitEnv()})
		if err != nil {
			return fmt.Errorf("inspect the repository: %w", err)
		}
		// The line is "<mode> <type> <object>\t<path>"; 120000 is a symlink.
		if strings.HasPrefix(string(out), "120000 ") {
			return fmt.Errorf("%s is a symbolic link in the repository, which a build may not follow", prefix)
		}
	}
	return nil
}

// buildArgs returns the app's build-time variables, opened.
func (d *Deployer) buildArgs(ctx context.Context, app db.App) (map[string]string, error) {
	sealed, err := d.DB.ListEnvVars(ctx, db.KindApp, app.ID)
	if err != nil {
		return nil, err
	}
	args := make(map[string]string)
	for _, v := range sealed {
		if !v.BuildTime {
			continue
		}
		plain, err := d.Box.OpenString(v.Value)
		if err != nil {
			return nil, fmt.Errorf("build variable %s cannot be decrypted: was the master key changed?", v.Key)
		}
		args[v.Key] = plain
	}
	return args, nil
}

// pruneImages removes an app's built images beyond the newest keepImages.
// It never fails a deployment: a leftover image costs disk, nothing else.
func (d *Deployer) pruneImages(ctx context.Context, dk docker.Client, appID, current string) {
	repo := ImageRepository(appID)
	tags, err := dk.ImageTags(ctx, repo)
	if err != nil {
		d.Log.Warn("list images to prune", "app", appID, "err", err)
		return
	}
	kept := 0
	for _, tag := range tags {
		ref := repo + ":" + tag
		if ref == current || kept < keepImages {
			kept++
			continue
		}
		if err := dk.RemoveImage(ctx, ref); err != nil {
			d.Log.Warn("remove old image", "image", ref, "err", err)
		}
	}
}

// StaticDockerfile is the Dockerfile of the static build pack: nginx serving
// the publish directory. With spa, unknown paths answer with index.html so
// a single-page app's own router can handle them.
func StaticDockerfile(publishDir string, spa bool) string {
	from := "."
	if publishDir != "" && publishDir != "." {
		from = publishDir
	}
	var b strings.Builder
	b.WriteString("# Generated by musdash for the static build pack.\n")
	b.WriteString("FROM nginx:alpine\n")
	b.WriteString("RUN rm -rf /usr/share/nginx/html/*\n")
	b.WriteString("COPY " + from + "/ /usr/share/nginx/html/\n")
	if spa {
		b.WriteString("COPY <<'EOF' /etc/nginx/conf.d/default.conf\n")
		b.WriteString("server {\n  listen 80;\n  root /usr/share/nginx/html;\n  index index.html;\n  location / {\n    try_files $uri $uri/ /index.html;\n  }\n}\n")
		b.WriteString("EOF\n")
	}
	return b.String()
}

// joinRel joins two repository-relative paths, either of which may be empty.
func joinRel(a, b string) string {
	if a == "" || a == "." {
		return b
	}
	if b == "" || b == "." {
		return a
	}
	return a + "/" + b
}

func orRoot(p string) string {
	if p == "" || p == "." {
		return "the repository root"
	}
	return p
}
