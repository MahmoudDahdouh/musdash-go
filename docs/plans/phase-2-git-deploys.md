# Phase 2 — Git deploys: implementation plan

**Goal:** deploy an app from a Git repository — public, private through a GitHub App, or private through a deploy key — built with a Dockerfile or served as a static site, and redeployed automatically on push.

**Spec:** `docs/spec.md` sections 4.1, 4.2, 6.1, 6.6, 8. Builds on phases 0 and 1.

**Done when:** pushing to a private repository redeploys the app. The GitHub side (creating the App, receiving a real push) is verified on the VPS; here it is tested against a stand-in GitHub API and a local Git repository.

## Global constraints

- Phase 0 and 1 constraints hold. No new modules: RS256 JWTs use `crypto/rsa`, webhooks use `crypto/hmac`, SSH key generation uses `golang.org/x/crypto/ssh`.
- `git` and `docker build` run through the Runner, on the app's server.
- A token or key never appears in a URL, a command line, `.git/config` or a log.
- One build at a time per server (RAM rule 6).

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| Passing the GitHub token to git | Through `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_0` / `GIT_CONFIG_VALUE_0` in the environment, not `git -c http.extraHeader=…` | The spec's `-c` form puts the token on the command line, where `ps` shows it to every user on the server |
| Build arguments | `--build-arg NAME` with the value in the docker CLI's environment | Keeps build-time secrets off the command line for the same reason |
| Installation id | Looked up per deploy with `GET /repos/{owner}/{repo}/installation`, not stored | One GitHub App can be installed on several accounts; looking it up is always right and costs one request |
| Build lock | Git deployments use the job lock key `build:<serverID>` | Serialises builds per server; it also serialises deployments of one app, since an app lives on one server |
| Image tags | `musdash/<appID>:<commit, 12 chars>`; the newest 5 per app are kept | Phase 7 rolls back to them; older ones are pruned so the disk does not fill |
| Static build pack | A generated Dockerfile on `nginx:alpine` that copies the publish directory, with an optional single-page-app fallback | Small, well known, serves compressed files and range requests correctly |
| Custom `docker run` options | Parsed into flags and checked against an allow-list of flags that cannot widen the container's access to the host | Options such as `--privileged`, `--network host`, `--pid host`, `--device` or `-v` would undo the bind-mount restrictions from phase 1 |
| Deploy webhook | `POST /api/v1/deploy?uuid=<appID>` with `Authorization: Bearer <token>`; the token is generated per app, shown once, stored hashed | Team API tokens arrive in phase 8 and will be accepted by the same endpoint |
| Push webhooks | Two endpoints: `/webhooks/github/{sourceID}` for a GitHub App (secret from the manifest flow), and `/webhooks/git/{appID}` for a webhook a person adds to a repository by hand (secret generated per app) | Deploy-key and public repositories have no App to deliver events |
| Repository URL | `https://…` or SSH (`git@host:path`, `ssh://…`) only; passed after `--` | Blocks `ext::`, `file://` and option injection |

## Data model (migration `0003_git.sql`)

| Table | Change |
|---|---|
| `apps` | add `repo_url`, `branch`, `build_pack` (`dockerfile`/`static`), `dockerfile_path`, `base_dir`, `publish_dir`, `spa_fallback`, `start_command`, `docker_options`, `git_source_id`, `ssh_key_id`, `auto_deploy`, `webhook_secret` (sealed), `deploy_token_hash` |
| `git_sources` | `id`, `team_id`, `name`, `kind` (`github_app`), `app_id`, `slug`, `html_url`, `client_id`, `client_secret` (sealed), `private_key` (sealed), `webhook_secret` (sealed), `state` (pending manifest flows), `created_at` |
| `ssh_keys` | `id`, `team_id`, `name`, `public_key`, `private_key` (sealed), `created_at` |

## File map

```
migrations/0003_git.sql
internal/db/sources.go            git_sources, ssh_keys queries
internal/source/github.go         App JWT, manifest conversion, installation token, repositories
internal/source/webhook.go        signature check, push event parsing
internal/source/sshkey.go         ed25519 deploy key generation
internal/source/repo.go           repository URL and branch validation, owner/repo extraction
internal/deploy/build.go          clone, build, tag, prune
internal/deploy/options.go        docker run option allow-list
internal/docker/build.go          BuildSpec → argument vector; image list and removal
internal/web/handlers_sources.go  Sources page, GitHub App manifest flow, deploy keys
internal/web/handlers_webhooks.go /webhooks/github/{id}, /webhooks/git/{id}, /api/v1/deploy
internal/web/pages/sources.templ, and Git fields in apps.templ
```

## Interfaces

```go
// internal/source
type GitHub struct{ APIBase string; HTTP *http.Client }           // APIBase is overridden in tests
func AppJWT(appID int64, privateKeyPEM []byte, now time.Time) (string, error)
func (g *GitHub) ConvertManifest(ctx, code string) (ManifestResult, error)
func (g *GitHub) InstallationToken(ctx, appID int64, key []byte, owner, repo string) (string, error)
func (g *GitHub) Repositories(ctx, appID int64, key []byte) ([]Repo, error)
func VerifySignature(secret, body []byte, header string) bool     // X-Hub-Signature-256
func ParsePush(body []byte) (repo, branch, commit string, ok bool)
func GenerateDeployKey(comment string) (public string, privatePEM []byte, err error)
func ParseRepo(url string) (Repo, error)                           // Host, Owner, Name, SSH bool
func ValidBranch(name string) bool

// internal/deploy
type Credentials struct{ Env []string; Cleanup func() }           // how git authenticates
func (d *Deployer) build(ctx, r runner.Runner, app db.App, dep db.Deployment, log *Log) (image, commit string, err error)
func ParseRunOptions(s string) ([]string, error)                   // allow-listed flags as an argument vector
```

## Tasks

### Task 1 — Sources: GitHub App, deploy keys, validation
- [ ] `AppJWT`: RS256 over `{iat: now-60, exp: now+540, iss: appID}`. Test verifies the signature with the public key and the claims.
- [ ] `GitHub` client against `httptest`: manifest conversion, installation lookup, token minting, repository listing; non-2xx answers become errors that carry GitHub's message.
- [ ] `VerifySignature`: constant-time; rejects a missing header, a wrong prefix, a wrong digest, and an empty secret.
- [ ] `ParsePush`: branch pushes only; tag pushes and branch deletions are ignored.
- [ ] `GenerateDeployKey`: the public key parses with `ssh.ParseAuthorizedKey`, the private key with `ssh.ParsePrivateKey`.
- [ ] `ParseRepo` and `ValidBranch` table tests, including `ext::sh -c …`, `file:///etc`, `--upload-pack=…`, and a branch named `--force`.

### Task 2 — Build pipeline
- [ ] `docker.BuildSpec.Args()` with the same validation discipline as `RunSpec`; build args by name only.
- [ ] `ParseRunOptions`: quote-aware splitting; every flag checked against the allow-list; values checked per flag. Tests cover each allowed flag and the refusals (`--privileged`, `-v`, `--volume`, `--mount`, `--network`, `--pid`, `--ipc`, `--device`, `--cap-add SYS_ADMIN`, `--security-opt seccomp=unconfined`, `--user root` is allowed, a flag glued as `--init--privileged`).
- [ ] `build.go`: credentials → shallow clone into `<data>/work/<deploymentID>` → `git rev-parse HEAD` → build (Dockerfile, or the generated static Dockerfile) → tag → prune old tags → remove the work directory, also on failure.
- [ ] Pipeline: git apps build first and skip the pull; the start command and run options are applied to the container.
- [ ] Tests with the scripted Runner: command order; the token appears in no command line; a failed build leaves the old container serving and removes the work directory; pruning keeps the newest five; a build-time secret is not on the command line.
- [ ] Test with a real local Git repository and the real `git` binary: clone, commit detection, wrong branch error.

### Task 3 — Webhooks and the deploy endpoint
- [ ] `/webhooks/github/{sourceID}`: verify, then enqueue a deployment for each app on that source whose repository and branch match and that has auto-deploy on.
- [ ] `/webhooks/git/{appID}`: the same for one app with its own secret; accepts GitHub's signature header.
- [ ] `/api/v1/deploy`: bearer token, constant-time compare of hashes, JSON answer with the deployment id.
- [ ] All three are exempt from the session CSRF check and rate-limited per address.
- [ ] Tests: bad signature is 401 and queues nothing; a push to another branch queues nothing; a valid push queues exactly one deployment; a replayed delivery id queues nothing new; a wrong token is 401.

### Task 4 — UI
- [ ] Sources page: GitHub Apps (create through the manifest flow, install link, delete) and deploy keys (create, copy public key, delete).
- [ ] New app: choose Docker image or Git repository; for Git: repository (picker for a GitHub App, or URL), branch, build pack and its fields.
- [ ] App settings: Source card (repository, branch, build pack, start command, run options, auto-deploy), webhook URL and secret, deploy token.
- [ ] Environment tab: a second block for build-time variables.
- [ ] The manifest page alone allows `form-action https://github.com` in its Content-Security-Policy.
- [ ] Tests: form validation, team scoping of sources and keys, the manifest callback rejects a wrong `state`.

### Task 5 — End to end
- [ ] `test/deploy_test.go` gains a Git case (with `MUSDASH_DOCKER_TEST=1`): a local repository with a Dockerfile is deployed, a commit is pushed, the signed webhook is delivered, and the new content is served through the proxy.

## Review focus

1. **A token in a place it must not be**: command line, clone URL, `.git/config`, the deployment log — asserted in the build tests.
2. **A repository URL or branch that is really an option or a transport**: `ParseRepo` / `ValidBranch` tests.
3. **A webhook anyone can forge**: signature tests, including an app with no secret set.
4. **A build that fails halfway**: work directory removed, old container untouched.
5. **Run options that escape the container**: allow-list tests.

## Self-review of this plan

- Spec coverage for phase 2 rows: Dockerfile build pack (task 2), static build pack (2), build-time variables (2, 4), custom start/build commands and run options (2, 4), deploy webhook (3), public repository (2), GitHub App (1, 4), deploy key (1, 2, 4), auto-deploy on push (3). "Custom build command" applies to Nixpacks and is left to phase 9 with it; the Dockerfile and static packs have no separate build command.
- Interface names match across tasks.
- No placeholders.

**Approved for implementation.**
