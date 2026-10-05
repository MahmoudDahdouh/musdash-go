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
| Refused outright | `privileged`, host `network_mode` / `pid` / `ipc` / `uts` / `userns_mode` / `cgroup`, `devices`, `cap_add` beyond the phase 2 safe list, `security_opt` other than `no-new-privileges`, non-`net.` `sysctls`, `volumes_from`, `container_name`, `provider`, `build` (except for a Git service, inside its checkout), external volumes and networks, volume or network names outside the project, bind mounts outside the service's directory unless they pass `docker.CheckBindSource`, `musdash.` labels | Each one reaches the host, another team's data, or musdash's own bookkeeping |
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
- [ ] A service of kind `git`: repository, branch, Compose file path, access as for Git apps; clone → config in the sandbox with the checkout mounted → `docker compose build` → up.
- [ ] Push webhooks and the deploy token work for it as for a Git app.

### Task 6 — End to end
- [x] With `MUSDASH_DOCKER_TEST=1`: Uptime Kuma (one container) and WordPress (two containers, generated database password) are installed from the catalogue, answer on their loopback ports, survive a redeploy with their data, and are deleted with their volumes.

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
