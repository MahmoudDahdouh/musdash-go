# musdash

A self-hosted platform for deploying apps, databases and services with Docker, written in Go and built to idle in very little memory.

One static binary runs as two processes:

- `musdash server` is the control plane: the web UI, webhooks, background jobs and a SQLite database.
- `musdash proxy` is the edge proxy that routes each domain to its container and issues HTTPS certificates.

The full design is in [docs/spec.md](docs/spec.md). Each phase has an implementation plan in [docs/plans](docs/plans).

## Status

| Phase | Scope | State |
|---|---|---|
| 0 | Skeleton: accounts, projects, job queue, design system, memory test | Done |
| 1 | Deploy Docker images with domains and HTTPS | Planned |
| 2 | Deploy from Git, including private GitHub repositories | Planned |
| 3 | Databases | Planned |
| 4 | One-click services | Planned |
| 5–9 | Backups, multi-server, previews, teams, extras | Planned |

## Build

You need Go 1.27 or newer. Nothing else is required: the generated templates and the stylesheet are committed.

```bash
make build
```

```bash
make build-linux
```

`make build` writes `bin/musdash` for your machine. `make build-linux` writes `dist/musdash-linux-amd64` and `dist/musdash-linux-arm64` for servers.

## Run

```bash
./bin/musdash server -data ./data -listen 127.0.0.1:8000
```

Open `http://127.0.0.1:8000`. The first visit asks you to create the owner account; setup closes once it exists.

| Command | What it does |
|---|---|
| `musdash server` | Runs the control plane |
| `musdash proxy` | Runs the edge proxy |
| `musdash migrate` | Applies database migrations and exits |
| `musdash reset-password <email>` | Prints a one-time link to choose a new password |
| `musdash version` | Prints the version |

### Settings

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-data` | `MUSDASH_DATA` | `/var/lib/musdash` | Directory for the database, keys, logs and backups |
| `-listen` | `MUSDASH_LISTEN` | `:8000` | Address the UI listens on |
| `-dev` | `MUSDASH_DEV=1` | off | Request logging and the component gallery at `/_ui` |
| `-pprof` | | off | Serves `/debug/pprof` to loopback clients only |
| | `MUSDASH_MASTER_KEY` | | Base64 of the 32-byte key that encrypts stored secrets. When unset, the key is created at `<data>/master.key` |
| | `MUSDASH_URL` | `http://localhost:8000` | Address used in links printed by `reset-password` |

Back up `<data>/master.key` together with the database. Without the key, stored secrets cannot be decrypted.

## Memory

Targets when idle: under 30 MB for `server` and under 20 MB for `proxy`. A test builds the release binary, starts both processes, uses them briefly and fails if either is above its target.

```bash
make rss
```

```bash
make rss-linux
```

`make rss-linux` runs the same test inside a Linux container, which is the figure that counts for a server.

The binary sets its own soft memory limit (48 MiB for `server`, 32 MiB for `proxy`) and `GOGC=50`. Set `GOMEMLIMIT` or `GOGC` to override them.

## Develop

```bash
make dev
```

```bash
make generate
```

```bash
make test
```

- `make dev` runs the server against `./data` with the gallery on.
- `make generate` rebuilds the templ components and the stylesheet after you change a `.templ` file or `internal/web/assets/input.css`. It downloads the Tailwind standalone CLI into `bin/` on first use.
- Throwaway sign-in values for local testing are in [dev.example.env](dev.example.env).

### Front end

Pages are rendered on the server with [templ](https://templ.guide) and updated with [htmx](https://htmx.org). There is no Node toolchain and no front-end framework: interactive behaviour is a 4 KB script driven by `data-` attributes, which lets the Content-Security-Policy forbid inline script and style.

The design system lives in two places:

- Tokens and component styles: `internal/web/assets/input.css`
- Components: `internal/web/ui`

Icons follow the [Lucide](https://lucide.dev) set (ISC licence).

## Layout

```
cmd/musdash        subcommands
internal/config    data directory layout, master key
internal/secret    AES-GCM sealing, random ids and tokens
internal/db        SQLite, migrations, queries
internal/auth      passwords, login limiter
internal/runner    run commands on a server (local now, SSH in phase 6)
internal/jobs      persistent job queue
internal/proxy     edge proxy
internal/web       handlers, pages, components, static assets
migrations         numbered SQL files, embedded in the binary
test               whole-binary tests
```
