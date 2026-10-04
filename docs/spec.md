# Plan: a Coolify-style PaaS in Go, optimised for minimum RAM

Working name: `litepaas` (rename freely; do not ship under the Coolify name or logo).

Feature list checked against the Coolify docs on 2026-10-05. RAM figures are targets and estimates, not measurements; phase 0 adds a test that measures them.

---

## 1. Goals and limits

**Goals**

- Deploy apps from Git (including private GitHub repos), Docker images, databases and one-click services, with domains and automatic HTTPS.
- Control plane + proxy idle at **under 50 MB RAM combined** (Coolify recommends 2 GB and runs five containers: app, helper, realtime, Postgres, Redis, plus Traefik).
- One static binary, no Node, no Postgres, no Redis, no separate proxy product.

**What the rewrite cannot shrink**

- The Docker daemon (`dockerd` + `containerd`): roughly 100–200 MB.
- Builds: a Node/Next.js image build can spike to 1–2 GB. Mitigations are in section 8.
- The deployed apps and databases themselves.

**Non-goals for now:** Docker Swarm (deprecated in Coolify too), cloud server provisioning (Hetzner/DigitalOcean/Vultr), log drains, MCP server, a separate CLI.

---

## 2. Architecture

```
                       ┌──────────────────────────── server A (control plane host) ─┐
 browser ──HTTPS──▶    │  litepaas proxy   (:80/:443, autocert, routes.json)        │
                       │        │ 127.0.0.1:<port>                                   │
                       │        ▼                                                    │
                       │  app / db / service containers  ◀── docker CLI ──┐         │
                       │                                                   │         │
                       │  litepaas server  (UI, API, webhooks, jobs, SQLite)         │
                       └───────────────────────────┬─────────────────────────────────┘
                                                   │ SSH (agentless)
                       ┌───────────────────────────▼──── server B, C, … ────────────┐
                       │  litepaas proxy + containers (same binary, pushed over SSH) │
                       └─────────────────────────────────────────────────────────────┘
```

Key decisions:

1. **One binary, two processes.** `litepaas server` is the control plane; `litepaas proxy` is the edge proxy. They are separate processes so restarting or upgrading the control plane never drops app traffic.
2. **Agentless.** Remote servers are driven over SSH running the `docker`, `git` and shell commands already on the machine. Nothing runs on a remote server except the proxy and the user's containers.
3. **Shell out instead of importing SDKs.** `docker`, `docker compose` and `git` are called as processes. They cost RAM only while running, and the binary stays small.
4. **One `Runner` interface** with two implementations: `LocalRunner` (`os/exec`) and `SSHRunner` (`x/crypto/ssh`). Every other package talks to servers only through it.
5. **Host-process proxy, loopback ports.** Containers publish to `127.0.0.1:<random port>`; the proxy routes `Host` → that port. No shared Docker network to manage, and rolling updates become "start new container, health check, swap the port, stop old container".
6. **Everything streams.** Build output, container logs and backups are piped, never held in memory.

---

## 3. Packages (the complete dependency list)

**External modules: three required, two optional.**

| Module | Used for | Phase |
|---|---|---|
| `modernc.org/sqlite` | Database (pure Go, no cgo, static binary) | 0 |
| `golang.org/x/crypto` | `ssh` (remote servers, deploy keys), `acme/autocert` (Let's Encrypt), `bcrypt` (passwords) | 0–1 |
| `github.com/a-h/templ` | Type-checked HTML components | 0 |
| `github.com/coder/websocket` (optional) | Web terminal only | 9 |
| `github.com/creack/pty` (optional) | Web terminal on the local server only | 9 |

**Everything else is the standard library:**

| Need | Standard library | Deliberately avoided |
|---|---|---|
| HTTP + routing | `net/http` (Go 1.22+ mux: methods and path params) | Gin, Echo, Fiber, chi |
| Reverse proxy | `net/http/httputil.ReverseProxy` (handles WebSocket upgrades) | Traefik, Caddy |
| Docker | `os/exec` → `docker` CLI with `--format json` | Docker Go SDK |
| Compose parsing | `docker compose config --format json` → `encoding/json` | A YAML library |
| Git | `os/exec` → `git clone --depth 1` | `go-git` |
| GitHub App JWT (RS256) | `crypto/rsa`, `crypto/sha256`, `encoding/base64` (~30 lines) | A JWT library |
| Webhook signatures | `crypto/hmac` | — |
| Secrets at rest | `crypto/aes` + `crypto/cipher` (AES-256-GCM) | Vault, KMS clients |
| 2FA (TOTP) | `crypto/hmac` + `crypto/sha1` (~40 lines) | An OTP library |
| Sessions, CSRF | Random token in SQLite + cookie | A session framework |
| Background jobs | Goroutine pool + `jobs` table | Redis, Asynq, River |
| Scheduling | `time.Ticker` + own 5-field cron parser (~100 lines) | A cron library |
| Live updates | Server-Sent Events via `http.Flusher` | Soketi, WebSockets |
| Email | `net/smtp` | A mail library |
| Chat notifications | `net/http` POST (they are all webhooks) | Per-service SDKs |
| S3 upload | `docker run --rm minio/mc` on demand | AWS SDK |
| Logging, config | `log/slog`, `flag`, environment variables | zap, viper, cobra |
| Static assets, migrations, templates | `embed` | — |

**Front end:** templ + HTMX (with its SSE extension) + Alpine.js + Tailwind standalone CLI. All assets are embedded in the binary; there is no Node toolchain and no JSON API needed for the UI.

---

## 4. Feature list: Coolify → this project

Phase numbers refer to section 7. "Skip" means out of scope unless you later need it.

### 4.1 Applications

| Coolify feature | Phase | How |
|---|---|---|
| Deploy a prebuilt Docker image | 1 | `docker pull` + `docker run` |
| Dockerfile build pack | 2 | `docker build` on the target or build server |
| Static site build pack | 2 | Generated Dockerfile serving files with a tiny static server image |
| Docker Compose build pack | 4 | `docker compose up -d` with a generated override file |
| Nixpacks | 9 | Shell out to the `nixpacks` binary, installed on first use |
| Railpack (beta in Coolify) | 9 | Same approach as Nixpacks |
| Build-time and runtime environment variables | 1 | Encrypted in SQLite; written to a `0600` env file, passed with `--env-file` / `--build-arg` |
| Custom start/build commands, custom `docker run` options | 2 | Fields on the app; options validated against an allow-list |
| Persistent storage: volumes, bind mounts, single-file mounts | 1 | `-v` flags; file mounts written over the Runner |
| CPU and memory limits | 1 | `--memory`, `--cpus` |
| Health checks (HTTP path or command) | 1 | Polled by the deploy pipeline before switching traffic |
| Rolling update with minimal downtime | 1 | New container → health check → swap route → stop old |
| Rollback to a previous image | 7 | Keep last N tags `lp/<app>:<sha>`; rollback re-runs an old tag |
| Pull-request preview deployments | 7 | PR webhook → deploy on `pr-<n>.<domain>`, comment on the PR, destroy on close |
| Deployment logs, runtime logs | 1 | Build log to a capped file; runtime via `docker logs -f` streamed over SSE |
| Container terminal | 9 | xterm.js + WebSocket + PTY (SSH `RequestPty` or `creack/pty`) |
| Deploy webhook for external CI | 2 | `POST /api/v1/deploy?uuid=…` with a bearer token |
| Tag-based bulk deploys | 8 | Tags table; deploy all resources with a tag |

### 4.2 Git sources

| Coolify feature | Phase | How |
|---|---|---|
| Public repository (HTTPS) | 2 | `git clone --depth 1 --branch <b>` |
| **Private repo via GitHub App** | 2 | See section 6.1 |
| Private repo via deploy key | 2 | Generate ed25519 key, show public key to paste into GitHub, clone with `GIT_SSH_COMMAND` |
| Auto-deploy on push | 2 | GitHub webhook, HMAC-verified |
| GitLab, Bitbucket, Gitea, generic Git | 9 | Deploy key + per-provider webhook parser |

### 4.3 Databases

| Coolify feature | Phase | How |
|---|---|---|
| PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly, ClickHouse | 3 | One embedded template per engine (section 6.2) |
| Generated credentials, connection strings | 3 | `crypto/rand`; internal and (optional) public URL shown in the UI |
| Persistent volume, resource limits, health check | 3 | From the template |
| Public port toggle | 3 | Off by default; when on, publish to `0.0.0.0:<port>` |
| SSL/TLS for database connections | Skip | Add only if databases are exposed publicly |
| Scheduled backups to local disk or S3, retention, restore | 5 | Section 6.4 |

### 4.4 One-click services

| Coolify feature | Phase | How |
|---|---|---|
| 300+ Compose templates (n8n, WordPress, Ghost, Plausible, Uptime Kuma, MinIO, …) | 4 | Compatible template format, so Coolify's templates can be reused (section 6.3) |
| "Docker Compose Empty" (paste your own) | 4 | Same pipeline with user-supplied YAML |
| Magic variables (`SERVICE_PASSWORD_*`, `SERVICE_USER_*`, `SERVICE_FQDN_*`, `SERVICE_URL_*`, `SERVICE_BASE64_*`, `SERVICE_HEX_*`) | 4 | Regex scan + generated values stored encrypted, written to `.env` |

### 4.5 Servers and networking

| Coolify feature | Phase | How |
|---|---|---|
| Localhost server | 1 | `LocalRunner` |
| Remote servers over SSH, SSH key management | 6 | `SSHRunner`; one pooled connection per server, closed after idle |
| Server validation and Docker install | 6 | Scripted checks over the Runner |
| Build server | 6 | Build on server X, push to a registry, pull on server Y |
| Reverse proxy with automatic HTTPS | 1 | Section 6.5 |
| Custom domains, multiple domains per app, `www` redirect | 1 | Rows in `domains` → `routes.json` |
| Auto-generated domains | 1 | `<random>.<server-ip>.sslip.io` |
| Path-based routing, basic auth | 7 | Fields on the route |
| Wildcard certificates / DNS challenge | Skip | `autocert` cannot do DNS-01; would need `go-acme/lego` |
| Cloudflare Tunnel | 4 | Ship `cloudflared` as a one-click service |
| Automated Docker cleanup | 5 | Scheduled `docker image prune` / `builder prune` with a disk threshold |
| Server patching, cloud provisioning, Swarm | Skip | — |

### 4.6 Operations

| Coolify feature | Phase | How |
|---|---|---|
| Scheduled tasks (cron inside a container), history | 5 | `docker exec` on a schedule; output to capped log files |
| Notifications: email, Discord, Telegram, Slack, Mattermost, Pushover, webhook | 5 | `net/smtp` and `net/http` |
| Events: deploy status, backup result, task result, container down, disk usage | 5 | One `notify.Send(event)` entry point |
| Server and container metrics (Coolify's Sentinel) | 9 | On demand: `docker stats --no-stream` and `/proc` over the Runner. Optional 60 s sampling into a ring table, off by default |
| Container status monitoring | 1 | One long-lived `docker events` stream per server, no polling |
| Log drains (Axiom, New Relic, Fluent Bit) | Skip | — |

### 4.7 Accounts and access

| Coolify feature | Phase | How |
|---|---|---|
| Projects → environments → resources | 0 | Core data model |
| Login, sessions, password reset | 0 | bcrypt + cookie session |
| Teams, invitations, roles (Owner / Admin / Member) | 8 | `team_members.role` checked in middleware |
| Shared variables (team, project, environment, server scope) | 8 | Resolved at deploy time |
| Two-factor authentication | 8 | TOTP |
| API tokens, REST API, rate limiting | 8 | Hashed tokens; same handlers as the UI with JSON responses |
| OAuth login (GitHub, GitLab, Google, …), SSO | Skip | Add GitHub OAuth later if needed; it is two HTTP calls |
| CLI, MCP server | Skip | The REST API covers scripting |

---

## 5. Data model (SQLite)

Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`, `cache_size=-2000` (2 MB), `busy_timeout=5000`, `foreign_keys=ON`. Pool: `SetMaxOpenConns(2)`. Migrations are numbered `.sql` files embedded with `embed`.

| Table | Purpose |
|---|---|
| `users`, `sessions`, `teams`, `team_members`, `api_tokens` | Accounts and access |
| `servers`, `ssh_keys` | Targets and credentials (keys encrypted) |
| `projects`, `environments` | Grouping |
| `apps` | Source, build pack, ports, limits, health check, current image tag |
| `databases` | Engine, version, credentials (encrypted), volume, public port |
| `services` | Compose YAML, generated values |
| `domains` | Host/path → resource, TLS state |
| `env_vars` | Key, encrypted value, build/runtime flag, scope |
| `storages` | Volumes, bind mounts, file mounts |
| `git_sources` | GitHub App id, encrypted private key and webhook secret, installation ids |
| `deployments` | Status, commit, log file path, timings |
| `jobs` | Queue: type, payload, status, attempts, run-after |
| `scheduled_tasks`, `task_runs` | Cron tasks |
| `backup_configs`, `backups`, `s3_storages` | Backups |
| `notification_channels` | Type, encrypted config, subscribed events |

Logs are files on disk (capped in size), never SQLite rows.

---

## 6. How the main features work

### 6.1 Private GitHub repositories (GitHub App)

1. **Create the app with the manifest flow.** The UI posts a manifest to GitHub; GitHub redirects back with a code; exchanging it returns the App ID, private key, client secret and webhook secret. Store all of them encrypted.
2. **User installs the app** on their account or organisation and picks repositories. Save the `installation_id`.
3. **List repos and branches** for the UI with an installation token.
4. **Per deploy, mint a fresh token:** sign a JWT (RS256, `iss` = App ID, 10-minute expiry) with `crypto/rsa`, then `POST /app/installations/{id}/access_tokens`. Tokens last one hour and are never stored.
5. **Clone** with `git -c http.extraHeader="Authorization: Basic <base64(x-access-token:TOKEN)>" clone --depth 1 --branch <b> <url>`, so the token never lands in the URL, `.git/config` or logs.
6. **Auto-deploy:** `POST /webhooks/github` verifies `X-Hub-Signature-256`, matches repo + branch to apps, and enqueues a deploy job. `pull_request` events drive previews.

Deploy keys are the fallback: generate an ed25519 key pair, show the public key, clone over SSH.

### 6.2 Database templates

One Go struct per engine, embedded in the binary:

```go
type DBTemplate struct {
    Engine      string            // "postgres"
    Image       string            // "postgres:17-alpine"
    Port        int               // 5432
    Env         map[string]string // POSTGRES_USER={{.User}} …
    VolumePath  string            // /var/lib/postgresql/data
    HealthCmd   string            // pg_isready -U {{.User}}
    DumpCmd     string            // pg_dump -U {{.User}} -Fc {{.DB}}
    RestoreCmd  string
    URLFormat   string            // postgres://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/{{.DB}}
}
```

Creating a database is: generate credentials → `docker run -d` with volume, limits and health check → show the connection string. Prefer `-alpine` image variants where the engine offers them.

### 6.3 One-click services (n8n, WordPress, …)

1. Templates are Compose files in `templates/services/*.yaml`, embedded. Coolify's own templates live in its repo under `templates/compose/`; if its licence permits (check `LICENSE`, believed Apache-2.0), reuse them with attribution, since this project implements the same magic variables.
2. Scan the YAML text with a regex for `SERVICE_<TYPE>_<ID>` and generate each value once (same name → same value across services).
3. Run `docker compose config --format json` to let Docker parse the file, then read which service owns each `SERVICE_FQDN_*` / `SERVICE_URL_*` and its port.
4. Write `.env` plus a generated `docker-compose.override.yml` that publishes those ports to `127.0.0.1` and adds resource limits and labels.
5. `docker compose -p <id> up -d`, then add the routes.

### 6.4 Backups

- **Dump:** `docker exec <db> <DumpCmd> | gzip > /var/lib/litepaas/backups/<db>/<timestamp>.gz`, run entirely on the target server so nothing passes through the control plane's memory.
- **Upload to S3-compatible storage:** `docker run --rm -v <dir>:/b minio/mc cp …`. Zero RAM while idle and no SDK.
- **Retention:** keep last N or last D days, locally and remotely.
- **Restore:** stream the file back into `docker exec -i <db> <RestoreCmd>`.

### 6.5 Proxy

- `httputil.ReverseProxy` with one shared, tuned `http.Transport`.
- Route table loaded from `routes.json` into an `atomic.Pointer`; the control plane rewrites the file atomically and sends `SIGHUP`.
- `autocert.Manager` with `DirCache` and a `HostPolicy` that only allows hosts in the route table. Port 80 serves ACME HTTP-01 and redirects everything else to HTTPS.
- Access logging off by default.
- On remote servers the same binary is copied over SSH and run under systemd.

### 6.6 Deploy pipeline

```
enqueue → fetch source (shallow clone to a temp dir)
        → build image lp/<app>:<sha>      (log → file → SSE)
        → docker run -d -p 127.0.0.1::<port> --env-file … --memory … --restart unless-stopped
        → read host port (docker port)
        → health check until healthy or timeout
        → rewrite routes.json + SIGHUP proxy
        → stop and remove the old container
        → prune images beyond the last N → notify
```

A failed health check removes the new container and leaves the old one serving.

---

## 7. Phases

Each phase ends with something usable.

| Phase | Delivers | Done when |
|---|---|---|
| **0. Skeleton** | Binary with `server` and `proxy` subcommands, SQLite + migrations, login, projects/environments, templ layout, job queue, `Runner` interface, RSS test | A user can log in and create a project; idle RSS test passes |
| **1. Image deploys** | Deploy a Docker image on localhost with env vars, volumes, limits, health check, domain, auto-HTTPS, live logs, rolling update | `nginx` image reachable on a real domain over HTTPS; redeploy has no dropped requests |
| **2. Git deploys** | Public repos, GitHub App private repos, deploy keys, Dockerfile and static build packs, auto-deploy on push, deploy webhook | Pushing to a private repo redeploys the app |
| **3. Databases** | The eight engines with credentials, volumes, connection strings, public-port toggle | An app from phase 2 connects to a Postgres created in the UI |
| **4. Services** | Compose pipeline, magic variables, initial catalogue (n8n, WordPress, Ghost, Uptime Kuma, MinIO, cloudflared), paste-your-own Compose | n8n and WordPress each install in one click with working domains |
| **5. Operations** | Backups + S3 + restore, scheduled tasks, notifications, Docker cleanup | A nightly Postgres backup lands in S3 and a Discord message confirms it |
| **6. Multi-server** | SSH servers, validation, remote proxy install, build server | The same app deploys to a second VPS from the UI |
| **7. Deploy polish** | Rollback, PR previews, path routing, basic auth | Opening a PR creates a preview URL; closing it removes it |
| **8. Access** | Teams, roles, invitations, shared variables, 2FA, API tokens + REST API, tags | A Member cannot delete a server; API can trigger a deploy |
| **9. Extras** | Web terminal, metrics, Nixpacks/Railpack, GitLab/Bitbucket/Gitea | As needed |

Phases 0–4 are the product you described: private GitHub repos, databases and one-click services. Phases 5–9 are independent of each other and can be reordered.

---

## 8. RAM rules

**Targets (idle, to be measured in phase 0)**

| Process | Target RSS |
|---|---|
| `litepaas server` | under 30 MB |
| `litepaas proxy` | under 20 MB |

**Runtime settings**

- `GOMEMLIMIT=48MiB` for the server, `32MiB` for the proxy; `GOGC=50`.
- Build with `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`.

**Code rules**

1. Stream with `io.Copy`; never read command output, logs or uploads fully into memory.
2. One `docker events` stream per server instead of polling; no goroutine per container.
3. Metrics and log views fetch on demand and stop when the browser disconnects (use the request context).
4. No in-memory caches of database rows; SQLite is fast enough to query each time.
5. SSH connections are opened lazily and closed after a few minutes idle.
6. Job workers: a small fixed pool (for example 4), with **one build at a time per server**.
7. Use bcrypt, not argon2 (argon2 allocates about 64 MB per hash by default).
8. A `pprof` endpoint behind a flag, and a test that starts the server, waits, and fails if RSS exceeds the target.

**Keeping builds from exhausting the server**

- Serialise builds per server (rule 6).
- Offer a build server, or build in GitHub Actions and deploy the resulting image through the deploy webhook. With either, the app server can be a 512 MB–1 GB VPS.
- Recommend swap on small servers in the install script.

---

## 9. Project layout

```
cmd/litepaas/main.go        subcommands: server, proxy, migrate
internal/
  db/          SQLite open, pragmas, embedded migrations, queries
  crypto/      AES-GCM seal/open, random generators
  auth/        users, sessions, CSRF, TOTP, roles, API tokens
  runner/      Runner interface, LocalRunner, SSHRunner, shell quoting
  docker/      thin wrappers over the docker / compose CLI
  source/      GitHub App, deploy keys, webhook handlers
  deploy/      pipeline, health checks, rollback, previews
  catalog/     database templates, service templates, magic variables
  proxy/       reverse proxy, route table, autocert
  jobs/        queue, workers, scheduler, cron parser
  backup/      dump, upload, retention, restore
  notify/      email + webhook channels
  web/         handlers, templ components, SSE, embedded static assets
  api/         REST handlers
templates/services/*.yaml
migrations/*.sql
```

Install target: a single binary plus two systemd units, data in `/var/lib/litepaas`.

---

## 10. Security checklist

- Access to the Docker socket is root on the host; the control plane must run as a dedicated user in the `docker` group and never expose raw command input.
- **Shell quoting:** local commands use argument arrays (`exec.Command(name, args...)`), never a shell string. Every argument sent over SSH goes through one `shellQuote` helper. This is the main injection risk in a design that shells out.
- Master key from a `0600` file or environment variable; all secrets (env vars, SSH keys, GitHub App keys, tokens) AES-GCM encrypted.
- Verify every webhook signature; reject unsigned requests.
- Cookies `HttpOnly`, `Secure`, `SameSite=Lax`; CSRF token on every state-changing form; rate-limit login.
- API tokens stored hashed, shown once.
- Databases are not exposed publicly unless the user turns the toggle on.

---

## 11. Open decisions

1. **Compose parsing:** the plan relies on `docker compose config --format json`. If that proves limiting, add a YAML module.
2. **Wildcard certificates:** not supported with `autocert`. Decide in phase 7 whether to add DNS-01 through `go-acme/lego`.
3. **Reusing Coolify's service templates:** confirm the licence and attribution requirements before copying them.
4. **Front end:** if the existing Bun project already has a UI worth keeping, embed it as static files and expose a JSON API instead of templ + HTMX.
