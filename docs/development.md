# Developing musdash

[← All documentation](../README.md#documentation)

For people who build musdash from source or change it. The full design is in [spec.md](spec.md). Each phase has an implementation plan in [plans](plans).

One static binary runs as two processes:

- `musdash server` is the control plane: the web UI, webhooks, background jobs and a SQLite database.
- `musdash proxy` is the edge proxy that routes each domain to its container and issues HTTPS certificates.

## Status

| Phase | Scope | State |
|---|---|---|
| 0 | Skeleton: accounts, projects, job queue, design system, memory test | Done |
| 1 | Deploy Docker images with domains, HTTPS, storage, live logs and rolling updates | Done |
| 2 | Deploy from Git: Dockerfile and static builds, GitHub App, GitLab token, deploy keys, push webhooks, deploy token | Done |
| 3 | Databases: PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly, ClickHouse | Done |
| 4 | Services: Docker Compose stacks from a catalogue of more than 600 (the services Coolify and Dokploy offer and ones written for musdash, by category), your own file, or a Git repository | Done |
| 5 | Operations: scheduled database backups with S3 copies, retention and restore; scheduled commands; notifications; Docker clean-up | Done |
| 6 | More servers: deploy to machines reached over SSH, each with its own proxy; build on one server and run on another | Done |
| 7 | Deploy polish: rollback, domains by path, a password in front of a domain, a preview for every pull request | Done |
| 8 | Access: members with roles, invitations, two-step sign-in, shared variables, tags, API tokens and an API | Done |
| 9 | Extras: a terminal in a container, what servers and containers use, Nixpacks and Railpack builds, webhooks from GitLab, Bitbucket and Gitea | Done |

## Build

You need Go 1.27 or newer. Nothing else is required: the generated templates and the stylesheet are committed.

```bash
make build
```

```bash
make build-linux
```

`make build` writes `bin/musdash` for your machine. `make build-linux` writes `dist/musdash-linux-amd64` and `dist/musdash-linux-arm64` for servers.

## Releasing

A release is four files: the two Linux binaries, `checksums.txt`, and `install.sh` with the release's version written into it, which is what the one-line install command downloads. Pushing a tag makes one:

```bash
git tag v0.2.0
```

```bash
git push origin v0.2.0
```

The workflow in `.github/workflows/release.yml` then runs vet and the tests, runs the installer's own tests in a container, builds the four files with `make release` and publishes them. A tag is `v` and three numbers; the installer refuses any other shape, and so does the workflow. When a step fails nothing is published: fix it and push the next version, since the install command asks for the latest release whatever its number. Started by hand from the Actions page, the workflow builds and tests everything and publishes nothing.

```bash
make release VERSION=v0.2.0
```

makes the same four files in `dist/` on your machine.

## Run locally

```bash
./bin/musdash server -data ./data -listen 127.0.0.1:8000
```

```bash
./bin/musdash proxy -data ./data -http 127.0.0.1:8080 -https=
```

Open `http://127.0.0.1:8000`. The first visit asks you to create the owner account; setup closes once it exists. The second command runs the proxy on port 8080 without HTTPS, so a deployed app answers at `http://<its domain>:8080`.

The commands and settings are listed in [Installing musdash](install.md#commands).

## Tests

```bash
make test
```

```bash
MUSDASH_DOCKER_TEST=1 go test ./test -run TestDeployWithDocker -v
```

The second command runs the end-to-end test against your local Docker: it deploys `nginx:alpine`, fetches it through the proxy, redeploys while sending requests continuously and fails if any request is dropped. It then rolls back to the first deployment's kept image, and asks the proxy for a path of a second domain with its prefix removed and a password in front.

```bash
MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestGitDeployWithDocker -v
```

This one clones a local repository with the real `git`, builds it with Docker and serves two commits in turn. It then previews a branch as a pull request would: built and served next to the app, with its own variables, and removed again.

```bash
MUSDASH_DOCKER_TEST=1 go test ./internal/deploy -run TestBuildersWithDocker -v -timeout 40m
```

Builds a small app that has no Dockerfile with the real Nixpacks and the real Railpack, serves it and asks it what it was given. The first run downloads the builders and their base images.

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

Loads Compose files in the sandbox (including ones that try to read files of the server, and every catalogue template, which takes several minutes: there are more than 600), then runs a two-container stack through deploy, redeploy, stop and delete.

```bash
MUSDASH_DOCKER_TEST_SERVICES=1 go test ./internal/deploy -run TestCatalogueWithDocker -v -timeout 90m
```

Installs the six templates written for musdash for real, fetches each web address, redeploys and deletes. `MUSDASH_SERVICES=wordpress,umami` installs just those, which is how one of the imported templates is tried. `MUSDASH_SERVICE_FILES=/tmp/app.yaml` installs a template that is not in the catalogue yet, from its file; `MUSDASH_SERVICE_START=5m` is how long a stack may take to come up, for trying many (it is ten minutes otherwise).

```bash
cd tools/catalog && go run . -coolify ~/src/coolify -dokploy ~/src/dokploy-templates \
    -icons ~/src/selfhst-icons -dashboard ~/src/dashboard-icons -svgl ~/src/svgl -simple ~/src/simple-icons
```

Makes the catalogue again, all but the six templates with no `source` line, from checkouts of Coolify, Dokploy's templates and four icon collections (it also needs Chrome or Chromium, which draws the logos that are pictures) and from `tools/catalog/templates`. It is a Go module of its own (it needs a YAML parser, which musdash does without) and no part of the binary. Run the sandbox test afterwards; what it refuses goes into `tools/catalog/rejected.txt`.

`tools/catalog/templates` holds the templates written for musdash for a service neither catalogue has: a Compose file under the five header lines of a catalogue file (`name`, `about`, `docs`, `website`, `categories`), in musdash's own words (its magic variables, named volumes, no `ports` for what the proxy carries). The converter holds one to the rules it holds the others to, declares its volumes, finds its logo and writes it with `# source: musdash`; one it cannot convert stops the run with the reason. A logo no collection has goes beside the template under its name (`templates/app.svg`, or `.png`). To add a service:

```bash
cd tools/catalog
go run . -check templates/app.yaml > /tmp/app.yaml      # what it becomes, or why it cannot be one
cd ../.. && MUSDASH_DOCKER_TEST_SERVICES=1 MUSDASH_SERVICE_FILES=/tmp/app.yaml \
    go test ./internal/deploy -run TestCatalogueWithDocker -v   # started for real
```

A template is added once that passes. It gives the app's first account a generated name and password where the app can take them, and an app that would start with a sign-in everybody knows, or with none while it holds something private, gets no template: every service has a public address.

Two more lists beside it are written by hand, and the converter stops when a line of one names nothing:

- `links.txt` has the two links of every card: the website, and the page to read before running the service. Coolify's templates name no website, and either catalogue's addresses go out of date, so a new template gets a line here; `TestServiceCatalogue` fails for a template with one link.
- `ended.txt` has the projects that are over (archived, given up, replaced). A key and the reason: neither catalogue's template of it is taken, and the reason is what `docs/catalogue-left-out.md` says. This is how a service is taken out of the catalogue; deleting its file would only last until the next run.

```bash
go test ./test -run TestInstall -v
```

```bash
MUSDASH_DOCKER_TEST=1 go test ./test -run TestInstall -v
```

The installer (`install/install.sh`). The first command calls its functions and its drawing from a shell that sourced it, under `sh`, `dash` and `bash`. The second also runs whole installs in a Debian container: a first install, the same again (nothing restarts), an upgrade, a download that does not match its checksum, a processor musdash is not built for, a server without Docker, and the list as it is drawn on a terminal. A container has no systemd, so `systemctl` and `docker` are stand-ins there; the dashboard that the last step waits for is the real binary.

## Memory

Targets when idle: under 30 MB for `server` and under 20 MB for `proxy`. A test builds the release binary, starts both processes, uses them briefly and fails if either is above its target. The proxy is given fifty hosts, some routed by path and some behind a password, and is asked for each, with right and wrong passwords.

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
- Throwaway sign-in values for local testing are in [dev.example.env](../dev.example.env).

### Front end

Pages are rendered on the server with [templ](https://templ.guide) and updated with [htmx](https://htmx.org). There is no Node toolchain and no front-end framework: interactive behaviour is one script of about 5 KB compressed, driven by `data-` attributes, which lets the Content-Security-Policy forbid inline script and style.

How the pages are laid out:

- The bar at the top is where you are: the project, its environment and the resource, each a switcher to the others beside it. The address says the same: `/projects/<project>/env/<environment>` for an environment and `/projects/<project>/env/<environment>/app/<id>` (or `database`, `service`) for what is in it; a project's own address, `/projects/<project>`, is its page. A project has two tabs: **Environments**, its environments as cards, each with what is in it counted, and **Add environment**; and **Settings**, with the project's name and description, its shared variables and deleting it. An environment's page lists what runs in it, and its own **Settings** hold its name, its shared variables and deleting it. The short address of a resource, `/apps/<id>`, still leads to its page: it is what a notification's link holds.
- Projects, and what is in an environment, are tiles. Apps, databases and services are all resources and are added from one page, **Add resource**.
- A form of up to five fields is a dialog, opened from a button beside what it changes. Anything that stops, removes or replaces something asks first; deleting a project, an app, a database or a service asks for its name.
- Keys, tokens and webhook secrets of the whole team are on one page, **Keys & tokens**, in two tabs: **Private Keys** (SSH key pairs) and **API Tokens** (a person's API tokens, deploy tokens and webhook secrets, in one table). No other page holds them or points at them. An app's domains are on its own **Domains** tab.

The design system lives in two places:

- Tokens and component styles: `internal/web/assets/input.css`
- Components: `internal/web/ui`

Icons are from the free [Hugeicons](https://hugeicons.com) set, Stroke Rounded (MIT licence, `internal/web/ui/icons.LICENSE`). The logos of the notification channels and of what the Add resource page offers are from the [selfh.st icon collection](https://selfh.st/icons) (CC BY 4.0), but for Ghost's ([Simple Icons](https://simpleicons.org), CC0) and KeyDB's and Dragonfly's (the outlines [Coolify](https://github.com/coollabsio/coolify) ships, Apache 2.0). `internal/web/static/logos.LICENSE` says which is which and what was changed; each is a trademark of its owner.

## Layout

```
cmd/musdash        subcommands
internal/config    data directory layout, master key
internal/secret    AES-GCM sealing, random ids and tokens
internal/db        SQLite, migrations, queries
internal/auth      passwords, login limiter, TOTP
internal/runner    run commands on a server, local or over SSH; terminals
internal/servers   server id → Runner
internal/docker    validated argument vectors for the docker CLI
internal/deploy    deploy pipeline, health checks, routes, status monitor
internal/metrics   what a server and its containers use, parsed from their answers
internal/jobs      persistent job queue
internal/proxy     edge proxy
internal/web       handlers, pages, components, static assets
migrations         numbered SQL files, embedded in the binary
install            the installer: one script, with the two systemd units in it
test               whole-binary tests
```
