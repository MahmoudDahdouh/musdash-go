# Phase 4 — Services: implementation plan

**Goal:** run a multi-container stack from a Docker Compose file — one from the built-in catalogue (n8n, WordPress, Ghost, Uptime Kuma, MinIO, cloudflared), one a person pastes, or one in a Git repository — with generated secrets, domains and HTTPS.

**Spec:** `docs/spec.md` sections 4.1 (Compose build pack), 4.4 and 6.3. Builds on phases 0–3.

**Done when:** n8n and WordPress each install in one click with working domains.

## Global constraints

- Earlier constraints hold. No new modules: Compose files are parsed by `docker compose config --format json`, never by a YAML library.
- A Compose file is as powerful as `docker run`. Everything phases 1 and 2 refuse for an app (privileged mode, host namespaces, bind mounts of the host, the Docker socket, capabilities, options that lift limits) is refused for a service too.
- Generated values are stored sealed and reach Docker only through `0600` files.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| Parsing a file a person wrote | `docker compose config` runs inside a throwaway container (`docker:<major>-cli`, no network, no Docker socket, no capabilities, read-only). The file is fed on standard input and the variables through `docker run --env-file`; nothing of the server is mounted. A stack from a Git repository gets its checkout, read-only, and nothing else | Loading a Compose file reads other files: `include`, `extends`, `env_file`. On the server those could name any file musdash can read (the master key, another app's env file) and leak it through an error message or into the container's environment. Inside the sandbox there is nothing to read |
| Files a pasted stack may name | None: `env_file`, `include` and `extends` from a file fail in the sandbox | A pasted stack is one file plus its variables. Feeding it on standard input also avoids Docker Desktop's slow file sharing, which showed a rewritten file stale for a moment |
| What is started | The normalised document the sandbox printed, after validation, written to `compose.resolved.json`; `docker compose -f compose.resolved.json up` | What runs is what was checked, plus only what musdash itself adds (labels, loopback ports, the restart policy). The original file is never loaded on the server |
| Validation | An allow-list over the normalised JSON: known-safe service keys only; anything else is refused by name | Compose keeps gaining keys (`provider` runs a program on the server); a deny-list would fall behind |
| Bind mounts of the stack's own directory | Refused, as are `secrets` and `configs` read from a file there | That directory holds `compose.resolved.json`, the file musdash starts the stack from. A container that could write to it could swap in a document that was never checked, or leave a symlink for the next deployment to mount. A pasted stack has no files of its own anyway: named volumes and `configs.content` cover it |
| Refused outright | `privileged`, host `network_mode` / `pid` / `ipc` / `uts` / `userns_mode` / `cgroup`, `devices`, `cap_add` beyond the phase 2 safe list, `security_opt` other than `no-new-privileges`, non-`net.` `sysctls`, `volumes_from`, `container_name`, `provider`, `build` (except for a Git service, inside its checkout), external volumes and networks, volume or network names outside the project, `ipam` settings and fixed container addresses, bind mounts outside the service's directory unless they pass `docker.CheckBindSource`, `musdash.` labels | Each one reaches the host, another team's data, or musdash's own bookkeeping |
| Published ports | A `ports:` entry is allowed when its host port is 1024–65535 and outside 20000–29999 | The same rule as a database's public port: publishing is a choice, not an escape |
| Web ports | Named by a magic variable `SERVICE_FQDN_<NAME>_<PORT>` (or `SERVICE_URL_…`); musdash publishes that container port on a loopback port it picks and routes the domain to it | Templates stay free of host ports, so two copies of one template never collide |
| `SERVICE_FQDN_*` and `SERVICE_URL_*` | `FQDN` is the bare host name, `URL` is `https://host` (or `http://` for a generated sslip.io address). A `_<PORT>` suffix names the container port and is not part of the value | Current Coolify semantics, so its templates can be pasted |
| Which Compose service owns a domain | The service whose name matches `<NAME>` (case-insensitive, `-` and `_` equal); otherwise the one service that mentions the variable | Read from `docker compose config --no-interpolate`, which keeps the references |
| Other magic variables | `SERVICE_USER_<ID>` 16 lowercase letters and digits; `SERVICE_PASSWORD_<ID>` 32 letters and digits, `SERVICE_PASSWORD_64_<ID>` 64; `SERVICE_BASE64_<ID>` 32 random bytes in base64, `_64_` and `_128_` likewise; `SERVICE_HEX_<ID>` 32 hex characters, `_64_` likewise. Generated once per service and kept | Same name → same value in every container of the stack, stable across redeploys |
| Networks | A stack runs on its own Compose network. Joining the environment's network is a switch on the service, off by default except where a template needs it (cloudflared) | Compose gives every container its service name as a DNS alias. Two stacks that both have a `db` service would answer for each other on a shared network. With the switch on, musdash refuses names already used in the environment |
| Catalogue | Six templates written for this project, embedded from `internal/catalog/services/*.yaml`, each with a small header of metadata in comments | Coolify's templates are Apache-2.0 and would be usable with attribution, but its own differ in details musdash does not implement; writing six avoids carrying a format we only partly support. The variable convention is the same, so a Coolify template can be pasted as "your own Compose" |
| Compose build pack | A service whose Compose file lives in a Git repository: cloned like a Git app, `build:` allowed for contexts inside the checkout, built with `docker compose build` under the server's build lock | The spec lists it under apps; it is the same pipeline as a pasted file with one more source |
| Status | A service is `running` when every container of its project is, `degraded` when only some are, `exited` when none is | One line per stack on the project page |
| Restart policy | Services without `restart:` get `unless-stopped` | A stack should come back after a reboot, as apps and databases do |

## Data model (migration `0008_services.sql`)

| Table | Columns |
|---|---|
| `services` | `id`, `environment_id`, `server_id`, `name`, `template` (catalogue key or `custom`), `compose` (the YAML text), `variables` (sealed JSON: generated and entered values), `connect_env` (join the environment network), `members` (Compose service names of the last deployment), `status`, `last_error`, `created_at`, `updated_at`; unique `(environment_id, name)` |
| `service_endpoints` | `id`, `service_id`, `name` (the `N8N` of `SERVICE_FQDN_N8N_5678`), `compose_service`, `port`, `host_port`; unique `(service_id, name)` |
| `domains` | unchanged: an endpoint's domain is the row with `resource_kind = 'service'` and the endpoint's id. Each endpoint has exactly one |

Names share the environment namespace with apps and databases.

## File map

```
migrations/0008_services.sql
internal/catalog/services.go          template list, metadata header parsing
internal/catalog/services/*.yaml      n8n, wordpress, ghost, uptime-kuma, minio, cloudflared
internal/catalog/magic.go             scan, generate, render .env
internal/compose/compose.go           sandboxed config, model types
internal/compose/validate.go          the allow-list
internal/compose/override.go          labels, loopback ports, network, restart policy applied to the model
internal/db/services.go
internal/deploy/service.go            deploy, stop, destroy job
internal/deploy/routes.go, monitor.go routes and status for services
internal/web/handlers_services.go
internal/web/pages/services.templ
```

## Interfaces

```go
// internal/catalog
type ServiceTemplate struct{ Key, Name, About, Compose string; ConnectEnv bool }
func Services() []ServiceTemplate
func Service(key string) (ServiceTemplate, bool)

type MagicVar struct{ Name, Kind, ID string; Port int }      // Kind: FQDN, URL, USER, PASSWORD, BASE64, HEX
func ScanMagic(compose string) []MagicVar                     // sorted, de-duplicated
func Generate(v MagicVar) (string, bool)                      // false for FQDN / URL, which come from domains

// internal/compose
type Project struct{ Name string; Services map[string]Service; Volumes, Networks map[string]Resource; raw map[string]json.RawMessage }
func Config(ctx, r runner.Runner, dir, file string, interpolate bool) (Project, []byte, error) // sandboxed
func Validate(p Project, opt ValidateOptions) error          // opt: project dir, checkout dir, data dir
func Apply(p *Project, o Override)                           // labels, endpoints, env network, restart
func (p Project) Marshal() ([]byte, error)

// internal/deploy
func (d *Deployer) EnqueueService(ctx, s db.Service) error
func (d *Deployer) StopService(ctx, id string) error
func (d *Deployer) DestroyService(ctx, id string, deleteData bool) error
```

## Tasks

### Task 1 — Magic variables and the catalogue
- [x] `ScanMagic`: finds `SERVICE_<KIND>[_<N>]_<ID>[_<PORT>]` in `${…}`, `$…` and bare-key form; ignores look-alikes (`MY_SERVICE_URL`); table test.
- [x] `Generate`: lengths and alphabets per kind; two calls differ.
- [x] Six templates with metadata headers; test: every template's variables are recognised, and every `FQDN`/`URL` variable names a service that exists in the file.

### Task 2 — Compose: sandboxed config, validation, override
- [x] `Config`: builds the `docker run … docker:<major>-cli compose config` argument vector (no network, read-only, no socket, no mount; file on standard input); errors carry Compose's message.
- [x] `Validate`: allow-list. Table test with one refusal per rule above and a file that uses every allowed key.
- [x] `Apply`: labels on every service, endpoint ports on loopback, optional environment network, default restart policy.
- [x] With `MUSDASH_DOCKER_TEST=1`: `include: /etc/hostname`, `env_file` outside the directory and `extends` from an absolute path are all refused without their content appearing in the error.

### Task 3 — Lifecycle
- [x] Job: write the variables file (`0600`) → sandboxed config twice (references, then resolved) → validate → endpoints and host ports → apply → write `compose.resolved.json` (`0600`) → `docker compose pull` → `up -d --remove-orphans --wait` with a timeout → routes.
- [x] Stop (`compose stop`), destroy (`compose down`, with `--volumes` only when asked), under the per-id lock.
- [x] Monitor: an event for `kind=service` recomputes the stack's status from one `docker ps`.
- [x] Routes include service endpoints with a domain.
- [x] Tests with the scripted Runner: order of commands; no secret on a command line; a failed `up` reports Compose's output and leaves the previous containers; destroy keeps volumes unless asked.

### Task 4 — UI
- [x] Project page lists services; "New service" with the catalogue and "Your own Compose file".
- [x] Service page: status and actions, endpoints with their domains, containers, logs (`docker compose logs`), the Compose file and variables (editable), settings, delete.
- [x] Tests: validation messages reach the form, team scoping, generated secrets are shown only on the service's own page.

### Task 5 — Compose from Git
- [x] A service of kind `git`: repository, branch, Compose file path, access as for Git apps; clone → config in the sandbox with the checkout mounted → `docker compose build` → up.
- [x] Push webhooks and the deploy token work for it as for a Git app.

### Task 6 — End to end
- [x] With `MUSDASH_DOCKER_TEST=1`: Uptime Kuma (one container) and WordPress (two containers, generated database password) are installed from the catalogue, answer on their loopback ports, survive a redeploy with their data, and are deleted with their volumes.

## Outcome

- `TestServiceWithDocker`: a two-container stack is deployed, reached on its loopback port, redeployed with its data, refused when its file asks for a privileged container (and left running), stopped and deleted with its volume.
- `TestCatalogueWithDocker`: WordPress, Uptime Kuma, Ghost, n8n and MinIO install, answer, redeploy and delete. Cloudflare Tunnel loads and validates but cannot be started without a real tunnel token.
- `TestSandboxCannotReadTheServer`: `include`, `env_file` and `extends` naming files of the server fail without their content appearing anywhere.
- MinIO stopped publishing its own image; the template uses Chainguard's build of MinIO's source (`cgr.dev/chainguard/minio`), which takes the same command and variables.
- Idle memory on Linux after this phase: server 23.6 MB, proxy 17.6 MB (phase 3: 23.5 and 17.2).
- Task 5, stacks from a Git repository (migration `0010_service_git.sql`): `TestGitServiceWithDocker` clones a real repository, builds its Dockerfile with `docker compose build`, serves the built page and a file mounted from the checkout, shows that the container cannot write to that file, redeploys from a second commit, refuses a mounted path that is a symlink out of the repository while the stack from before keeps serving, and deletes the stack with its image.
  - Every deployment clones into a directory of its own, and the others are removed once a stack is up from the new one. The first version cloned into one directory every time; on a redeployment that pulled the mounted files out from under the running containers. Found by the real-Docker test.
  - Files of the checkout are mounted read-only, and every checkout path the stack reads (mounts, build contexts, Dockerfiles, secret and config files) is checked for symlinks in the repository. A container that could write to the checkout could otherwise leave a link for another mount to follow out of it.
  - The commit and checkout are recorded when the stack is running, not when it was cloned; the Compose text is recorded as read, so a deployment that fails can still be explained.
  - Only the Compose file itself is scanned for `SERVICE_…` variables.

### Found by the independent review of task 5, and fixed

1. **The symlink check could be made to look at another file.** git reads a path that starts with `:` as pathspec "magic", so for a link named `:(top)plain.txt` it answered about `plain.txt`. git is now told to take paths literally (`GIT_LITERAL_PATHSPECS`), and a path of the checkout that the Compose file mounts or builds from must pass the same validation as the Compose file's own path. `TestRefuseSymlinksAsksAboutTheExactPath` runs real git against such names.
2. **A checkout could be removed under running containers** when a stack failed to come up after some containers had been replaced: the next attempt reused the directory. Every deployment now has its own, and old ones are removed only after a successful start.
3. **A path the repository does not have was created by Docker, as root, inside the checkout.** It is an error now.
4. **A build argument without a value was read from musdash's environment.** Dropped like such environment variables.
5. **Saving the Compose tab of a Git service wrote back the file's text the page was loaded with**, which could undo what a running deployment had just recorded. The tab saves variables and the network switch only.
6. **How long a large webhook request took told an id with a secret from one without.** The body is hashed either way.

Left as it is: a stack from Git holds its server's build lock until it is up, at most the ten minutes a stack gets to start. Releasing it after the build would need a second kind of lock for one job.

### Found by the independent review of tasks 1 to 4, and fixed

1. **The stack's own directory could be mounted.** A container given `.:/s` could overwrite `compose.resolved.json` between the check and `docker compose up`, or leave a symlink that a later bind mount followed to anywhere on the server. Bind mounts, secrets and configs inside that directory are now refused.
2. **Bind mounts reached directories the server acts on as root** (`/var/spool/cron`, `/var/lib/cloud`, `/var/backups`, `/var/log`). The deny-list now covers `/var/lib`, `/var/spool`, `/var/backups` and `/var/log`. It remains a deny-list, as decided in phase 1: mounting a directory of the server is for people trusted with the server. `/home` and `/opt` are still allowed. Phase 8 (roles) should reserve bind mounts for administrators.
3. **A variable named without a value was filled in from musdash's own environment** when the stack was started on the server. Such variables are dropped after the sandbox has had its chance to give them a value.
4. **A network could choose its own subnet**, which becomes a route of the server and could capture its traffic to public addresses. `ipam` and fixed addresses are refused.
5. **A failed or running redeployment took a healthy stack's domains away.** A service is now routed unless it was never started or was stopped; after a failed deployment its status is what its containers make it, with the failure shown as a warning.
6. **The retry after a taken port loaded the file again without checking it.** Each attempt now starts from a copy of the checked document.
7. **A container that ran once and ended well** (a migration, an init step) made the stack "degraded" for good. It is no longer counted.
8. **Stop could be undone by a deployment queued before it.** The job now gives way to a stop, and a deployment queued behind another one shows as deploying while it runs.
9. **The sandbox had no limits.** It now has 256 MB, 128 processes and three minutes.

## Review focus

1. **A Compose file that reads or mounts something outside its own directory** — sandbox tests and the validation table.
2. **A key Compose adds later** — the allow-list refuses unknown keys by default; test with a made-up key.
3. **Two stacks with a same-named service** — separate networks by default; collision check when joining the environment network.
4. **A secret in a command line or a world-readable file** — lifecycle tests assert file modes and argument vectors.
5. **A stack that half-starts** — `up` failure leaves a clear error and a state that Stop and Delete can clean up.

## Self-review of this plan

- Spec coverage for phase 4 rows: Compose templates (tasks 1, 4), paste your own (2, 3, 4), magic variables (1), Docker Compose build pack (5), Cloudflare Tunnel (the cloudflared template, 1). "300+ templates" is not attempted: six are shipped, and the variable convention lets others be pasted.
- The sandbox needs the `docker:<major>-cli` image on the server; it is pulled on first use and its absence is reported as such.
- Interface names match across tasks.
- No placeholders.

**Approved for implementation.**
