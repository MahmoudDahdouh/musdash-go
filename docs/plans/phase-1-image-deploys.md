# Phase 1 — Image deploys: implementation plan

**Goal:** deploy a prebuilt Docker image on the local server with environment variables, storage, limits, a health check and a domain, served through the musdash proxy with automatic HTTPS, live logs and no dropped requests on redeploy.

**Spec:** `docs/spec.md` sections 4.1, 4.5, 4.6, 6.5, 6.6. Builds on `docs/plans/phase-0-skeleton.md`.

**Done when:** an `nginx` image is reachable on a domain through the proxy, and a redeploy under load drops no request. HTTPS issuance needs a public domain and is verified on the VPS.

## Global constraints

- Everything in phase 0's constraints still holds.
- Every Docker and file operation goes through `runner.Runner`. Nothing in `deploy` or `docker` calls `os/exec` directly.
- Output of `docker pull`, `docker logs` and `docker events` is streamed, never collected.
- A failed deploy leaves the previous container serving.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| Container-to-container traffic | One Docker bridge network per environment, `musdash-<envID>`. Each container joins it with its resource name as a network alias | The spec publishes only to the host's loopback, which other containers cannot reach. Apps must reach databases (phase 3) |
| Host port | musdash picks the host port itself from 20000–39999 and publishes `127.0.0.1:<port>:<containerPort>`. A new deployment gets a new port | The spec's `127.0.0.1::<port>` lets Docker pick, but Docker may pick a different port when the container restarts (crash, reboot), which would silently break the route |
| Proxy reload | The proxy writes `proxy.pid`; the control plane rewrites `routes.json` atomically, then runs `kill -HUP <pid>` through the Runner | Works the same locally and over SSH |
| Generated domains | `<random>.<server-ip>.sslip.io` is served over plain HTTP. Custom domains get HTTPS | Let's Encrypt rate-limits shared suffixes such as `sslip.io`; a failed issuance would look like a broken deploy |
| Health check | HTTP path: any status below 400 passes. Command: `docker exec` exits 0. Neither set: the container must stay running and accept a TCP connection on its port. Timeout 60 s by default | Covers the three cases without requiring anything inside the image |
| Dashboard domain | An instance setting routes a domain to the control plane itself | Gives the UI HTTPS through the same proxy |
| Environment variables | Edited as one `KEY=value` block per app | One small form instead of a row editor; matches how people paste `.env` files |
| Resource addressing | `env_vars`, `storages` and `domains` reference `(resource_kind, resource_id)` | The same tables serve apps now and databases and services later |

## Data model (migration `0002_apps.sql`)

| Table | Columns |
|---|---|
| `servers` | `id`, `team_id`, `name`, `kind` (`local`/`ssh`), `host`, `port`, `ssh_user`, `ip`, `created_at`. One `local` row is created at setup |
| `settings` | `key`, `value` — instance domain, ACME email |
| `apps` | `id`, `environment_id`, `server_id`, `name` (unique per environment, DNS-safe), `source` (`image`), `image`, `port`, `memory_mb`, `cpus`, `health_path`, `health_cmd`, `health_timeout`, `status`, `container`, `host_port`, `deployed_image`, `created_at`, `updated_at` |
| `domains` | `id`, `resource_kind`, `resource_id`, `host` (unique), `tls` (`auto`/`off`), `redirect_www`, `created_at` |
| `env_vars` | `id`, `resource_kind`, `resource_id`, `key`, `value` (sealed), `build_time`, unique `(resource_kind, resource_id, key)` |
| `storages` | `id`, `resource_kind`, `resource_id`, `kind` (`volume`/`bind`/`file`), `source`, `target`, `content` (sealed, file mounts only) |
| `deployments` | `id`, `app_id`, `status` (`queued`/`running`/`success`/`failed`), `trigger`, `image`, `commit_sha`, `log_path`, `error`, `created_at`, `started_at`, `finished_at` |

## File map

```
migrations/0002_apps.sql
internal/db/{servers,settings,apps,domains,envvars,storages,deployments}.go
internal/servers/servers.go     Pool: server id → Runner (local now, SSH in phase 6)
internal/docker/docker.go       typed wrappers that build runner.Cmd for the docker CLI
internal/docker/args.go         RunSpec → argument vector
internal/deploy/pipeline.go     the deploy job
internal/deploy/health.go       health probes
internal/deploy/routes.go       DB → routes.json → proxy reload
internal/deploy/monitor.go      docker events stream → app status; start-up reconcile
internal/deploy/logfile.go      capped deployment log writer and follower
internal/proxy/{proxy,table,tls}.go
internal/web/handlers_apps.go, handlers_servers.go, handlers_settings.go, sse.go
internal/web/pages/{apps,servers,settings}.templ
```

## Interfaces

```go
// internal/docker
type RunSpec struct {
    Name, Image, Network, Alias string
    HostPort, ContainerPort     int
    EnvFile                     string
    MemoryMB                    int
    CPUs                        float64
    Mounts                      []Mount           // Kind, Source, Target, ReadOnly
    Labels                      map[string]string
}
func (c Client) Pull(ctx, image string, log io.Writer) error
func (c Client) Run(ctx, spec RunSpec) (containerID string, err error)
func (c Client) State(ctx, container string) (State, error)   // Running, ExitCode, Health
func (c Client) Stop(ctx, container string, grace time.Duration) error
func (c Client) Remove(ctx, container string) error
func (c Client) EnsureNetwork(ctx, name string) error
func (c Client) Logs(ctx, container string, tail int, follow bool, w io.Writer) error
func (c Client) Events(ctx, w io.Writer) error                 // long-lived stream
func (c Client) Exec(ctx, container string, argv []string) error

// internal/proxy — routes.json
type Route struct {
    Host       string `json:"host"`
    Target     string `json:"target,omitempty"`      // "127.0.0.1:20417"
    TLS        bool   `json:"tls"`
    RedirectTo string `json:"redirect_to,omitempty"` // host to redirect to (www handling)
}
type File struct {
    Email  string  `json:"email,omitempty"`
    Routes []Route `json:"routes"`
}

// internal/deploy
func (d *Deployer) Enqueue(ctx, appID, trigger string) (deploymentID string, err error)
func (d *Deployer) Stop(ctx, appID string) error
func (d *Deployer) SyncRoutes(ctx, serverID string) error
```

## Tasks

Each task ends with vet and tests green, a self-review, an independent review at the end of the phase, and a commit pushed to `main`.

### Task 1 — Proxy
- [x] `table.go`: parse `routes.json`, validate, build a host map; `atomic.Pointer` swap; lookup is case-insensitive and ignores the port.
- [x] `proxy.go`: one `httputil.ReverseProxy` with a shared tuned `http.Transport`; preserves `Host`; sets `X-Forwarded-For/Proto/Host`; strips client-supplied `X-Forwarded-*`; 404 for unknown hosts, 502 page when the target is down; pid file; SIGHUP reload that keeps the old table when the new file is invalid.
- [x] `tls.go`: `autocert.Manager` with `DirCache` and a `HostPolicy` limited to routes with `tls: true`; port 80 serves ACME challenges, redirects TLS hosts to HTTPS and proxies HTTP-only hosts.
- [x] Tests: routing by host, unknown host, `www` redirect, HTTPS redirect, HTTP-only host proxied, forwarded headers, spoofed headers dropped, WebSocket upgrade passes, reload swaps routes, invalid file keeps old routes, `HostPolicy` refuses unknown hosts, 502 when the target is closed.

### Task 2 — Data model, Docker wrappers, deploy pipeline
- [x] Migration and queries, all team-scoped through environment → project.
- [x] `docker/args.go` with table tests for every flag; values are passed as separate arguments and image names are validated against the Docker reference grammar so a value can never be read as a flag.
- [x] Env file writer: `KEY=value` lines, mode `0600`; keys validated as `[A-Za-z_][A-Za-z0-9_]*`; values with newlines rejected (Docker's env-file format cannot carry them).
- [x] Pipeline as a job with lock key `deploy:<appID>`: pull → network → env file → file mounts → run on a fresh port → health → routes + reload → stop old → record.
- [x] Failure at any step removes the new container, leaves the old one and its route, and records the error.
- [x] Tests with a scripted fake Runner: happy path command order; failed pull; failed health check removes the new container and keeps the old route; port collision retries with another port; stop removes the route first.

### Task 3 — Monitor and logs
- [x] `monitor.go`: one `docker events` stream per server filtered by the `musdash.managed` label; `die`, `start` and `health_status` update `apps.status`; reconnect with back-off; reconcile from `docker ps -a` at start.
- [x] `logfile.go`: deployment log capped at 2 MB, with a "log truncated" line when the cap is hit.
- [x] SSE: deployment log follower and `docker logs -f` runtime stream; both stop when the browser disconnects; output is HTML-escaped.
- [x] Tests: event lines map to the right status; follower delivers appended lines and ends when the deployment finishes; cancelled request stops the `docker logs` process.

### Task 4 — UI
- [x] Project page lists the environment's apps with state rail and pill; "New app" form (name, image, port, domain prefilled with a generated one).
- [x] App page tabs: Overview (status, domains, actions Deploy / Stop / Restart), Deployments (list and live log), Logs, Environment, Storage, Settings (general, limits, health check, domains, delete).
- [x] Servers page (local server, public IP, Docker version, proxy state) and Instance settings (dashboard domain, ACME email).
- [x] Sidebar gains Servers and Settings.
- [x] Tests: validation of every form, team scoping of every new route, deploy button enqueues one job.

### Task 5 — End-to-end check with real Docker
- [x] `test/deploy_test.go` (runs only with `MUSDASH_DOCKER_TEST=1`): start server and proxy, deploy `nginx:alpine` with a generated domain, fetch it through the proxy, redeploy while a client sends requests continuously and assert none fail, stop the app and assert the route is gone, then clean up every `musdash-` container and network the test made.
- [x] RSS test still passes with the real proxy.

## Review focus

1. **Container restarts on its own** (crash, reboot): the route must still point at it — covered by fixed host ports and the monitor (tasks 2, 3).
2. **Two deploys of one app at once**: the second must wait, not race — lock key test (task 2).
3. **Image name or env value that looks like a flag** (`--privileged`, `-v /:/host`): must be refused or passed inert — args tests (task 2).
4. **Domain already used by another app**: refused with a clear message, not a silent route clash — unique constraint and form test (task 4).
5. **Proxy receives SIGHUP with a half-written or invalid file**: keeps serving the old table — reload test (task 1).

## Self-review of this plan

- Spec coverage for phase 1 rows: prebuilt image (task 2), env vars (2, 4), storage volumes/bind/file (2, 4), CPU and memory limits (2), health checks (2), rolling update (2, 5), deployment and runtime logs (3, 4), localhost server (2), reverse proxy with HTTPS (1), custom and multiple domains with `www` redirect (1, 4), generated domains (2, 4), container status monitoring (3). All covered.
- Names used across tasks match the Interfaces block.
- No placeholders.

**Approved for implementation.**

## Outcome

- `test/deploy_test.go` against real Docker: `nginx:alpine` is served through the proxy by host name; a redeploy under load sent 6,908 requests with none failed; an app set to the wrong port fails its deploy and is cleaned up; stopping removes the route and the container.
- Idle memory on Linux after this phase: server 22.5 MB, proxy 16.1 MB (phase 0: 19.9 and 14.1). About 9 MB of the proxy's figure is the shared binary's mapped pages: it is one binary, so the proxy process also carries the control plane's code and data.
- The memory test now enforces the targets exactly on Linux and applies a 30 % looser ceiling elsewhere; macOS read 20.3 MB for the proxy against the 20 MB Linux target.
- Not verifiable here, to check on the VPS: certificate issuance for a real domain, the systemd units and `install/install.sh`, and behaviour after a server reboot.
- Known limit: a new container joins the environment's network under the app's name as soon as it starts, so for the length of its health check other containers resolving that name may reach it before it is ready. Traffic from outside is not affected; it moves only after the health check.

### Fixes from the automated security review of the first commit

1. The health-check URL was built by joining strings, so a path such as `@other-host/` could send the probe to another machine. It is now built with a fixed loopback host (`HealthURL`), and the path is validated when saved.
2. Bind mounts accepted any server path. `CheckBindSource` now refuses the Docker socket, system directories, anything containing them, and the musdash data directory.

### Fixes from the independent review of the phase

1. **Health check passed with nothing listening.** Docker's port proxy accepts connections on its own. The TCP check now waits briefly after connecting and fails if the connection is dropped.
2. **A failed route publication was ignored.** The deploy now puts the app back on its previous container and fails, instead of stopping the container that is still receiving traffic. The switch itself can no longer be interrupted by shutdown. The proxy also re-reads its routes file every 3 seconds, so a missed or refused signal delays a change by seconds; routes are republished at start.
3. **A killed process left apps stuck in "deploying" and containers orphaned.** At start, such apps are reset, and each reconcile removes app containers nothing refers to (sparing deployments that are queued, running or just finished).
4. **One over-long domain could block all route publishing.** Such a domain is refused in the form, skipped (with a log line) when routes are built, and apps are limited to 20 domains.
5. **Stop, delete and domain changes ended when the browser disconnected.** They now run to completion under their own deadline. A stop withdraws the route, removes the container, and only then forgets the container's name, so a failed stop can be repeated.
6. **Concurrent route publications could overwrite each other.** They are serialised.
7. A crash-looping container (`restarting`) fails the deploy at once, with its output in the log.
8. Deploy, stop and delete of one app are serialised by a per-app lock; stop and delete answer "a deployment is in progress" instead of racing it. A deployment of an app deleted meanwhile removes its container.
9. Log stream: the write deadline is set before writing; a carriage return in log output can no longer forge stream fields.
10. Shutdown ends open log streams at once and gives running deployments their own time allowance.
11. The proxy's pid file is written atomically, and on Linux a pid is signalled only if it is a musdash process.
12. Finished deployments beyond the newest 50 are pruned with their logs; logs are deleted with their app; `NaN` is refused as a CPU limit; a domain taken during app creation is reported; only one local server can exist; long flash messages are truncated.
