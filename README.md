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
| 1 | Deploy Docker images with domains, HTTPS, storage, live logs and rolling updates | Done |
| 2 | Deploy from Git: Dockerfile and static builds, GitHub App, deploy keys, push webhooks, deploy token | Done |
| 3 | Databases: PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly, ClickHouse | Done |
| 4 | Services: Docker Compose stacks from a catalogue (n8n, WordPress, Ghost, Uptime Kuma, MinIO, Cloudflare Tunnel) or your own file | Done, except stacks built from a Git repository |
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

## Install on a server

You need a Linux server with systemd and Docker. Build the binary on your own machine, copy it over with the `install` folder, and run the installer:

```bash
make build-linux
```

```bash
scp -r dist/musdash-linux-amd64 install root@your-server:/root/
```

```bash
ssh root@your-server 'cd /root && ./install/install.sh ./musdash-linux-amd64'
```

The installer creates a `musdash` user, installs `/usr/local/bin/musdash` and two services (`musdash-server` and `musdash-proxy`), and starts them. Then:

1. Open `http://<server address>:8000` and create the owner account.
2. Check **Servers**: Docker and the proxy should both show as running, and the public IP address should be the server's.
3. Create a project, then **New app** with an image such as `nginx:alpine`. The generated `sslip.io` address works at once over HTTP.
4. For HTTPS, point a domain's DNS at the server and add it under the app's **Settings → Domains**. The certificate is issued on the first request.
5. To put the dashboard itself on a domain with HTTPS, set it under **Settings**.

Ports 80 and 443 must be reachable from the internet for certificates to be issued. Run the installer again with a newer binary to upgrade; apps keep serving while the control plane restarts.

Use `musdash-linux-arm64` on ARM servers.

## Run locally

```bash
./bin/musdash server -data ./data -listen 127.0.0.1:8000
```

```bash
./bin/musdash proxy -data ./data -http 127.0.0.1:8080 -https=
```

Open `http://127.0.0.1:8000`. The first visit asks you to create the owner account; setup closes once it exists. The second command runs the proxy on port 8080 without HTTPS, so a deployed app answers at `http://<its domain>:8080`.

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
| `-listen` | `MUSDASH_LISTEN` | `:8000` | Address the UI listens on (`server`) |
| `-http` | `MUSDASH_PROXY_HTTP` | `:80` | Address the proxy serves HTTP on (`proxy`) |
| `-https` | `MUSDASH_PROXY_HTTPS` | `:443` | Address the proxy serves HTTPS on; empty turns HTTPS off (`proxy`) |
| `-dev` | `MUSDASH_DEV=1` | off | Request logging and the component gallery at `/_ui` |
| `-pprof` | | off | Serves `/debug/pprof` to loopback clients only |
| | `MUSDASH_MASTER_KEY` | | Base64 of the 32-byte key that encrypts stored secrets. When unset, the key is created at `<data>/master.key` |
| | `MUSDASH_URL` | `http://localhost:8000` | Address used in links printed by `reset-password` |

Back up `<data>/master.key` together with the database. Without the key, stored secrets cannot be decrypted.

## How a deploy works

1. The image is pulled; output goes to the deployment's log, which the page streams live.
2. The app's variables are written to a private env file, and its file mounts to disk.
3. A new container starts on the environment's Docker network, published on a loopback port musdash picks.
4. musdash waits for the health check: the configured path, the configured command, or the port accepting connections.
5. The proxy's routes are rewritten and reloaded, moving traffic to the new container.
6. The previous container is stopped and removed.

If any step before 5 fails, the new container is removed and the previous one keeps serving.

Apps in one environment reach each other by name (`web:80`) on that network. From outside, the only way in is the proxy.

### Deploying from Git

An app can be built from a repository instead of pulling an image. Step 1 above becomes: clone the branch, then `docker build`. Builds run one at a time per server.

| Repository | How musdash reads it |
|---|---|
| Public | Its `https://` address, nothing to set up |
| Private, on GitHub | A GitHub App: create one under **Sources**, then install it on the repositories |
| Private, anywhere | A deploy key: generate one under **Sources** and add its public half to the repository |

Two build packs: **Dockerfile** (a path inside the repository) and **Static site** (a directory served by nginx, with an optional single-page-app fallback). Variables marked build-time on the Environment tab are passed as build arguments.

A push deploys the app when auto-deploy is on:

- Through a GitHub App, pushes arrive on their own; nothing to add.
- Otherwise add a webhook to the repository. The app's Settings page shows the address and the secret; use content type `application/json` and the push event.

A CI pipeline can start a deployment with the app's deploy token, created on the same page:

```bash
curl -X POST -H "Authorization: Bearer $MUSDASH_DEPLOY_TOKEN" "https://musdash.example.com/api/v1/deploy?uuid=APP_ID"
```

## Databases

**New database** on a project page offers eight engines. musdash generates a user and a 32-character password, creates a Docker volume for the data and starts the engine on the environment's network.

- Apps in the same environment connect by the database's name, for example `postgres://postgres:…@maindb:5432/postgres`. The Overview page has the connection string ready to copy into an app's variables.
- Nothing outside the environment can reach a database until you switch on its **public port** in Settings. It is then open on the server to anyone with the password. Docker publishes the port past ufw and firewalld, so only a firewall in front of the server, such as your provider's, can narrow who reaches it.
- Restart replaces the container and keeps the volume, so expect a few seconds of downtime. Saving a change in Settings (image tag, public port, limits) restarts a running database to apply it.
- An image already on the server is not downloaded again. To move to another version of the same major release, change the tag. A new major version usually cannot open the old data: create a new database and move the data.
- MongoDB does not start on servers whose Linux kernel is 6.19 or newer; that is MongoDB's own limitation, and its message is shown on the database's page.
- Deleting a database keeps its volume unless you tick "Also delete the data".

## Services

A service is a stack of containers described by a Docker Compose file. **New service** on a project page offers a small catalogue and "Your own Compose file".

- Give a service of the stack a web address by adding `SERVICE_FQDN_<NAME>_<PORT>` to its environment: `NAME` is the Compose service, `PORT` the port it listens on. musdash gives it a domain (a generated one at first; change it under Settings) and routes it.
- `SERVICE_URL_<NAME>` is the same address with its scheme, `SERVICE_HTTPS_<NAME>` is `true` or `false`.
- `SERVICE_PASSWORD_<ID>`, `SERVICE_USER_<ID>`, `SERVICE_BASE64_<ID>` and `SERVICE_HEX_<ID>` are filled with values generated once per service. They are listed on the service's Overview page.
- Any other `${NAME}` in the file is yours to set, in the Variables box.

These names follow Coolify's convention, so a template written for it can be pasted.

A stack is held to the same limits as an app. Its file is read inside a container that has no network and sees nothing of the server, then checked: privileged mode, the host's network or process namespaces, devices, extra capabilities, mounts of system directories or the Docker socket, and volumes or networks that belong to something else are refused, with a message naming the line. Files the Compose file names (`env_file`, `include`, `extends`) are not read: a stack is one file plus its variables. The first deployment on a server downloads the `docker:<version>-cli` image used for that check.

A stack runs on its own network. Tick "Connect to the environment's network" if its containers and the apps and databases of the environment need to reach each other by name; names must then be unique across them.

To publish a port that is not HTTP (a mail server, a game server), use an ordinary `ports:` entry with a host port from 1024 to 65535, outside 20000 to 29999.

## Tests

```bash
make test
```

```bash
MUSDASH_DOCKER_TEST=1 go test ./test -run TestDeployWithDocker -v
```

The second command runs the end-to-end test against your local Docker: it deploys `nginx:alpine`, fetches it through the proxy, redeploys while sending requests continuously and fails if any request is dropped.

```bash
MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestGitDeployWithDocker -v
```

This one clones a local repository with the real `git`, builds it with Docker and serves two commits in turn.

```bash
MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestDatabasesWithDocker -v
```

Starts PostgreSQL and Redis, connects from a second container by name, restarts them and checks the data is still there, opens a public port, then deletes them.

```bash
MUSDASH_DOCKER_TEST_ENGINES=1 go test ./internal/deploy -run TestEveryEngineStartsWithDocker -v -timeout 60m
```

Starts each of the eight engines in turn and waits for its health check. It downloads several gigabytes of images and removes the ones that were not there before.

```bash
MUSDASH_DOCKER_TEST=1 go test ./internal/compose ./internal/deploy -run 'Sandbox|Catalogue|TestServiceWithDocker' -v
```

Loads Compose files in the sandbox (including ones that try to read files of the server, and every catalogue template), then runs a two-container stack through deploy, redeploy, stop and delete.

```bash
MUSDASH_DOCKER_TEST_SERVICES=1 go test ./internal/deploy -run TestCatalogueWithDocker -v -timeout 90m
```

Installs the catalogue's templates for real, fetches each web address, redeploys and deletes. `MUSDASH_SERVICES=wordpress,minio` limits it to those.

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
internal/servers   server id → Runner
internal/docker    validated argument vectors for the docker CLI
internal/deploy    deploy pipeline, health checks, routes, status monitor
internal/jobs      persistent job queue
internal/proxy     edge proxy
internal/web       handlers, pages, components, static assets
migrations         numbered SQL files, embedded in the binary
install            systemd units and the install script
test               whole-binary tests
```
