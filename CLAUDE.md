# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

musdash is a Coolify-style self-hosted PaaS written in Go, designed around one constraint: the control plane and proxy together idle under 50 MB RAM (server under 30 MB, proxy under 20 MB). It ships as one static binary with no Node, Postgres, Redis or separate proxy product.

Two documents drive the work; read them before designing anything:

- `docs/spec.md` — the full product spec: architecture, feature list by phase (0–9), data model, RAM rules, security checklist. It still uses the working name `litepaas`; the real name is `musdash` everywhere (binary, module, data dir, `MUSDASH_*` env vars).
- `docs/plans/phase-N-*.md` — the implementation plan for each phase, with task checklists, the file map, and a table of decisions that override or extend the spec. Where plan and spec disagree, the plan wins.

The build proceeds phase by phase, task by task. `git log` and the plan's task list show where it stands; packages named in the spec's layout (`auth`, `docker`, `deploy`, `proxy`, …) do not exist until their phase.

## Commands

```sh
go vet ./... && go test -short ./...                   # required green before every commit
go test ./internal/jobs -run TestLockKeySerialises -v  # one test

make generate     # templ → *_templ.go, and input.css → static/app.css (run after editing either)
make build        # release build into bin/musdash
make build-linux  # dist/musdash-linux-{amd64,arm64}
make dev          # server against ./data with the component gallery at /_ui
make rss          # idle-memory test on this machine
make rss-linux    # the same test in a Linux container: the authoritative figure
```

- `bin/` is git-ignored. `bin/tailwindcss` is the Tailwind v4 standalone CLI, downloaded by `make tools`; there is no Node toolchain.
- Generated files (`*_templ.go`, `internal/web/static/app.css`) are committed so a plain `go build` needs only Go. Regenerate both after editing any `.templ` file, `input.css`, or class names in `app.js`. `internal/web/static` embeds `app.css` by name, so the package does not compile until that file exists.
- Subcommand flags fall back to env vars: `--data` / `MUSDASH_DATA`, `--dev` / `MUSDASH_DEV=1`. `MUSDASH_MASTER_KEY` (base64, 32 bytes) overrides `<data>/master.key`.

## Hard constraints

These come from the spec and shape every change:

- **Three external modules only**: `modernc.org/sqlite`, `golang.org/x/crypto`, `github.com/a-h/templ`. Everything else is the standard library (`net/http` mux, `log/slog`, `flag`, `embed`). Spec section 3 lists what is deliberately avoided (routers, Docker SDK, YAML, JWT, cron and session libraries). Do not add a dependency without the user's say.
- **Shell out, don't import SDKs**: `docker`, `docker compose` and `git` are run as processes with `--format json` where output is parsed.
- **RAM rules (spec section 8)**: stream with `io.Copy`, never buffer command output, logs or uploads; no in-memory caches of database rows; no goroutine per container; small fixed worker pool; bcrypt, not argon2; `CGO_ENABLED=0`.
- **Injection safety**: local commands are argument vectors, never shell strings. Anything that reaches a shell (SSH) goes through `runner.Quote` / `runner.QuoteJoin`. User values passed as arguments are guarded with `--` against option injection.
- **Secrets at rest** are AES-256-GCM sealed with the master key (`secret.Box`). Session and API tokens are stored only as SHA-256 hashes (`secret.HashToken`).

## Architecture

One binary, two long-running processes: `musdash server` (UI, API, webhooks, jobs, SQLite) and `musdash proxy` (edge reverse proxy with autocert). They are separate so restarting the control plane never drops app traffic; the server rewrites `<data>/proxy/routes.json` atomically and signals the proxy with `SIGHUP`. Containers publish to a loopback port musdash assigns (20000–29999) and the proxy routes `Host` to that port; containers of one environment also share a Docker network and reach each other by app name. Remote servers are agentless, driven over SSH.

How the existing packages fit together:

- `cmd/musdash` — subcommand dispatch with plain `flag` sets. `commonFlags` registers the shared flags; `openDB` creates the data directory tree and returns a migrated database.
- `internal/config` — owns the data directory layout (every path under `DataDir` is a method here; add new paths here rather than joining strings elsewhere) and loads or creates the master key, refusing a key file readable by group or others.
- `internal/secret` — `Box` (seal/open) plus all random generation. `RandomID()` is the id for every row: 12 chars of `[a-z2-7]` starting with a letter, safe as a URL segment, container name and DNS label. Named `secret`, not `crypto`, to avoid shadowing the stdlib.
- `internal/db` — `Open` sets the pragmas the RAM budget depends on (WAL, 2 MB cache, 2 connections, `_txlock=immediate` so two connections cannot deadlock on a write upgrade). `Migrate` applies numbered files from the `migrations` package once each, in a transaction. Queries are hand-written methods on `*DB`, one file per area.
  - Every query for a team-owned row takes `teamID` and filters on it; a miss returns `db.ErrNotFound` whether the row is absent or belongs to another team, so handlers answer 404 either way.
  - Writes go through the `affected(...)` helper to turn zero rows into `ErrNotFound`; multi-statement writes use `d.Tx`.
  - Tables are `STRICT`, timestamps are Unix seconds in `INTEGER`, text columns default to `''` rather than NULL.
- `migrations/` — a tiny Go package exposing an `embed.FS` of `NNNN_name.sql` files (`go:embed` cannot reach parent directories). Add a new numbered file; never edit an applied one.
- `internal/runner` — the `Runner` interface is the only way any package touches a server: commands (`Run` streams, `Output` is capped at 1 MiB and errors rather than truncating) and file operations (`WriteFile` is atomic). `LocalRunner` uses `os/exec` with its own process group so cancelling a context also kills children. `SSHRunner` arrives in phase 6; code written against `Runner` must not assume it is local.
- `internal/jobs` — a persistent queue on the `jobs` table. One dispatcher goroutine claims work with a single `UPDATE … RETURNING` and runs up to N handlers. Jobs sharing a `lock_key` never overlap (this is how "one build per server" is enforced). Errors retry with back-off; wrap with `jobs.Permanent` to fail at once. Jobs left `running` by a crash are requeued in `Start`; a job interrupted by shutdown is requeued without spending an attempt.
- `internal/servers` — `Pool.Runner(server)` is where every package gets its Runner; only this package knows whether a server is local or SSH.
- `internal/docker` — builds argument vectors for the docker CLI. `RunSpec.Args()` validates every value (image names against the reference grammar, mounts, labels) so nothing a person typed can be read as a flag. Bind-mount sources go through `CheckBindSource`, which refuses the Docker socket, system directories and the musdash data directory.
- `internal/deploy` — the deploy job (`pipeline.go`): pull → network → env file → start on a fixed loopback port → health check → `SyncRoutes` → stop the old container. A failure before the switch removes the new container and leaves the old one serving. `monitor.go` holds one `docker events` stream per server and updates app status through `db.SetAppStatusIf`, which ignores containers that are not the app's serving one. `routes.go` regenerates a server's whole `routes.json` from the database and signals the proxy.
  - Health-check URLs are built by `HealthURL` with a fixed loopback host; never concatenate a user path onto a URL.
  - Unit tests script the server with `runner/runnertest.Fake` to inject failures; `test/deploy_test.go` runs the same flow against real Docker when `MUSDASH_DOCKER_TEST=1`.
  - `build.go` is the Git path: clone → `git ls-tree` symlink check → `docker build`. Credentials reach git and docker through their environment (`GIT_CONFIG_*`, `GIT_SSH_COMMAND`, `--build-arg NAME`), never an argument or URL. Clone and build are bounded by `cloneTimeout` / `buildTimeout`. `options.go` is the allow-list for a person's extra `docker run` options: add a flag only if it can neither reach the host nor loosen a limit musdash sets.
  - `database.go` is a database's whole lifecycle. One container per database (`musdash-db-<id>`), replaced in place: stop the old one, then start the new one on the same volume, never two at once. The password reaches the container only through a `0600` env file; engines without a password variable read it from their own environment in a `sh -c` command. Start is a job (`JobDatabase`, lock key `database:<id>`); stop and destroy run in the request under the same per-id lock as apps.
- `internal/catalog` — what musdash can run out of the box: the database engine templates and the service templates (`services/*.yaml`, Compose files with a `# key: value` header). `magic.go` is the `SERVICE_*` variable convention shared with Coolify's templates: address variables get their value from an endpoint's domain, the others are generated once and stored sealed. A template is data (image, env, command, health command, URL format); adding an engine should not need code elsewhere. `TestEveryEngineStartsWithDocker` is what proves a template against its real image.
  - A service from a Git repository (`db.TemplateGit`) goes through the same steps with three differences: the Compose text comes from a fresh clone, the sandbox gets the checkout mounted read-only (so `include` and friends can read the repository and nothing else), and `build:` is allowed inside it. A service has two checkout directories, `src-a` and `src-b`; each deployment clones into the one the running stack does not use, because the stack may have files of its checkout mounted. Those mounts are forced read-only and every checkout path the stack reads is checked with `git ls-tree` for symlinks: a writable or linked path would let one container redirect another's mount out of the checkout.
  - `service.go` deploys a Compose stack: variables file → sandboxed load twice (once raw, to see where variables are used; once filled in) → `Validate` → host ports for endpoints → `Apply` → `compose.resolved.json` (0600) → `docker compose pull` and `up --wait` against that file only. The person's own text is never given to Compose on the server.
- `internal/compose` — Compose without a YAML parser. `Config` runs `docker compose config --format json` in a container with no network, no mounts and no socket (the file on stdin), because loading a Compose file reads other files (`include`, `extends`, `env_file`) and on the server those could be any file musdash can read. `Validate` is an allow-list over the normalised JSON: a key it does not know is refused, since Compose keeps adding keys and some act on the host. When allowing a new key, ask what its *values* can reach. `Apply` adds only what musdash needs (labels, loopback ports, restart policy, the environment network).
- `internal/ops` — what musdash does on its own. `Run` ticks once a minute: schedules that are due are claimed in one `UPDATE … RETURNING` (their `next_run` becomes `-1` until the real next time is stored), and a job is queued for each. The jobs: backup (dump → optional upload → retention), restore, scheduled task (`docker exec` in the app's serving container), daily Docker clean-up. Backup, restore and a database's start share the lock key `database:<id>`.
  - A backup or run row is created `running` when it is queued. `Recover` (before the queue starts) fails the ones a dead process left, and each job returns at once when its row is no longer `running`: that is what makes a requeued job harmless.
  - `Notify` only queues an event; one goroutine delivers. Producers set `Event.URL` to a dashboard path and `deliver` makes it absolute. `deploy` reports through the `Deployer.Notify` hook so it does not import this package.
- `internal/backup` — `Dump` and `Restore` stream between `docker exec` and a gzip file through the Runner; `S3` runs `rclone` in a container on demand with the keys in an env file that is removed afterwards.
- `internal/cron` — the five-field parser and `Next`, in UTC.
- `internal/notify` — one function per channel kind over `net/http` and `net/smtp`. `Dialer` refuses loopback, link-local and unspecified addresses at connection time, so a redirect or a DNS answer cannot get around the check.
- `internal/source` — everything about a Git host that is not a process: `ParseRepo` / `ValidBranch` / `ValidRelPath` (the only validators for values that reach git), the GitHub App client, `ReadPush` (streaming signature check plus the four values a push needs), deploy-key generation.
- `internal/proxy` — `Table` is an immutable, validated route set swapped atomically; targets must be loopback. Port 80 serves ACME challenges, redirects TLS hosts and proxies HTTP-only ones. Certificates are requested only for routed hosts with `tls: true`.
- `internal/web` — templ + HTMX (SSE extension) with assets embedded in the binary; no JSON API for the UI.
  - `static/` serves embedded assets at `/static/{name}?v=<content hash>` with immutable caching and lazy gzip. Use `static.URL(name)` in templates; a new asset must be added to the `go:embed` line.
  - `static/app.js` is driven entirely by `data-*` attributes (`data-open`, `data-close`, `data-match`, `data-copy`, `data-autodismiss`, `data-follow`, `data-nav-toggle`) so pages carry no inline script and the CSP can forbid it.
  - `ui/` holds the reusable design-system components (props structs + templ); `pages/` holds pages built from them.
  - Handlers are wrapped in `s.authed(...)` or `s.anon(...)`, which load the session and check CSRF. A handler for a team-owned resource starts with a loader (`loadProject`, `loadApp`) that answers 404 itself when the resource is not the team's.
  - Apps, databases and services share one name namespace per environment (`db.nameTaken`): the name is the resource's address on the environment's Docker network. A service that joined that network also holds the names of its Compose services there.
  - A service's endpoint is what a domain points at: `domains` rows with `resource_kind = 'service'` carry an endpoint id, not a service id.
  - Tables shared across resource kinds (`domains`, `env_vars`, `storages`) are keyed by `(resource_kind, resource_id)`; their queries do not check the team, so only call them with a resource a loader returned.
  - `handlers_webhooks.go` holds the three endpoints other machines call (`/webhooks/github/{id}`, `/webhooks/git/{id}`, `/api/v1/deploy`). They have no session or CSRF; each authenticates by signature or bearer token and is rate-limited by address. Unknown id, missing secret and bad signature must stay indistinguishable.
  - Live logs are Server-Sent Events (`sse.go`): output is HTML-escaped, streams are capped at 16, and the producing process dies with the request context.

## UI design system

Defined in `internal/web/assets/input.css` and the "Design system" section of the phase 0 plan:

- Light mode only, blue brand, no web fonts, no icon font (icons are inline SVG via `ui.Icon`).
- Tailwind's default palette, type scale, radii and shadows are disabled (`--color-*: initial` etc.) and replaced with project tokens in `@theme`. Only token names exist as utilities (`brand-600`, `ink`, `ink-soft`, `canvas`, `paper`, `sunk`, `line`, `ok`/`warn`/`danger`/`idle` and their `-soft` tints). Never write a raw colour, size or radius.
- Controls use component classes from `@layer components` (`.btn .btn-primary`, `.field`, `.input`, `.card`, …) so markup carries one class per control, not a utility pile. Extend these and the `ui` components rather than styling per page.
- Machine facts (domains, image tags, ports, hashes, logs) use the mono stack. State is always written as text as well as colour.
- Tailwind scans only `internal/web/ui`, `internal/web/pages` and `static/app.js` (`source(none)` plus explicit `@source` lines); classes used elsewhere will not be generated.

## Workflow conventions

- Each plan task ends with `go vet ./... && go test ./...` green, a self-review of the diff, and a commit to `main` (remote `origin`). Commit subjects follow `Phase N task M: …`.
- New behaviour gets tests alongside it, written first where the plan says so. Tests use real SQLite files in `t.TempDir()` and real processes, not mocks.
- Comments in this codebase explain why (the constraint or failure being avoided), not what; match that.
