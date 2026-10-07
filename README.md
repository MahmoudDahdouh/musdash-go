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
| 2 | Deploy from Git: Dockerfile and static builds, GitHub App, GitLab token, deploy keys, push webhooks, deploy token | Done |
| 3 | Databases: PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly, ClickHouse | Done |
| 4 | Services: Docker Compose stacks from a catalogue (n8n, WordPress, Ghost, Uptime Kuma, MinIO, Cloudflare Tunnel), your own file, or a Git repository | Done |
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
3. Create a project, then **Add resource** and **Docker image**, with an image such as `nginx:alpine`. The generated `sslip.io` address works at once over HTTP.
4. For HTTPS, point a domain's DNS at the server and add it on the app's **Domains** tab. The certificate is issued on the first request.
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
| `musdash disable-2fa <email>` | Turns off an account's two-step sign-in, for somebody who lost their phone and their recovery codes |
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

### Rolling back

Every deployment's image stays on the server under a name of that deployment's own, the newest five of them. Open an earlier deployment and choose **Roll back**: that image runs again through the same health check and switch. Nothing is pulled or built, so a tag such as `nginx:latest` that has moved since does not matter. The app's settings and variables stay as they are today; only the image goes back.

### Domains, paths and passwords

A domain can be limited to a path: with `/api`, the app answers `app.example.com/api` and what is below it, and another app of yours on the same server can take the rest of the domain. The longest path that matches wins, on whole segments (`/api` is not `/apix`). The path can be removed before the request is passed on, for apps that expect to live at `/`.

A domain can also ask for a user name and password before anything reaches the app. The password is stored as a hash. Over plain HTTP it travels unencrypted, so use it with HTTPS. When one app is routed both openly and, under a path, behind a password, anything an app might read as that path asks for the password too: `/Admin` as well as `/admin`.

Both need a proxy of this version. A proxy that was running before the upgrade answers "nothing is deployed" for such a domain until it is restarted (`systemctl restart musdash-proxy`; the install script does it) or, on another server, installed again from the Servers page.

### Deploying from Git

An app can be built from a repository instead of pulling an image. Step 1 above becomes: clone the branch, then `docker build`. Builds run one at a time per server.

| Repository | How musdash reads it |
|---|---|
| Public | Its `https://` address, nothing to set up |
| Private, on GitHub | A GitHub App: create one under **Sources**, then install it on the repositories |
| Private, on GitLab | An access token: connect GitLab under **Sources** with a personal, group or project token that has the `read_api` and `read_repository` scopes. gitlab.com or your own instance, over HTTPS |
| Private, anywhere | A deploy key: generate one under **Sources** and add its public half to the repository |

Four build packs:

- **Dockerfile**: a path inside the repository.
- **Static site**: a directory served by nginx, with an optional single-page-app fallback.
- **Nixpacks** and **Railpack**: for a repository without a Dockerfile. The builder reads the code, works out how to build it, and the app listens on the port you gave it, which it is told as `PORT`. Their own settings go in as build-time variables (`NIXPACKS_START_CMD`, `RAILPACK_BUILD_CMD`, …) or in the repository's `nixpacks.toml` / `railpack.json`.

Variables marked build-time on the Environment tab are passed as build arguments (with Railpack, as build secrets). The tab lists variables by name: their values are sent to the page only when you choose **Show values**, and are changed in the tab's editor. Shared variables and a service's variables are kept the same way.

Neither builder is installed on a server. The first build that needs one makes a small image for it (`musdash/nixpacks:<version>`, `musdash/railpack:<version>`) from the project's release file, which Docker fetches from github.com and refuses unless it matches the checksum pinned in musdash. The builder then runs in a container that sees the checkout and nothing else of the server: no Docker socket, no capabilities, and for Nixpacks no network (Railpack asks the network which versions of a language exist). It needs Docker 24 or newer. The first build with either downloads its base images and takes minutes.

A push deploys the app when auto-deploy is on:

- Through a GitHub App, pushes arrive on their own; nothing to add.
- Through a GitLab source they do not: a token only reads. Add the webhook as below.
- Otherwise add a webhook to the repository. On the **Keys & tokens** page, under **API Tokens**, **Webhook secret** makes the secret for an app or a service; its row has the address and shows the secret, and the page says where each host wants them.

| Host | The secret goes in | Events to send |
|---|---|---|
| GitHub, Gitea, Forgejo | Secret, with content type `application/json` | Push (and pull requests, for previews) |
| GitLab | Secret token. A signing token is generated by GitLab and cannot be used here | Push events (and merge request events) |
| Bitbucket Cloud | Secret | Repository push (and pull request created, updated, merged, declined) |

Private repositories on these hosts are read with a deploy key (Bitbucket calls it an access key). Bitbucket Data Center is not supported.

A CI pipeline can start a deployment with the app's deploy token, made on the same page with **Deploy token**:

```bash
curl -X POST -H "Authorization: Bearer $MUSDASH_DEPLOY_TOKEN" "https://musdash.example.com/api/v1/deploy?uuid=APP_ID"
```

That token deploys this one app and nothing else. A person's API token does more: see [The API](#the-api).

### A preview for every pull request

Switch it on under the app's Settings. Each pull request into the app's branch then gets a deployment of its own, built from the pull request's branch, updated on every push to it and removed when it is closed.

- **Address.** Set a domain for previews, such as `preview.example.com`, and point `*.preview.example.com` at the server: pull request 12 of the app `web` is served at `pr-12-web.preview.example.com` over HTTPS. Without one, each preview gets a generated address over plain HTTP.
- **What it runs with.** The app's variables and files, as they are when the preview is deployed, plus `MUSDASH_PREVIEW=1` and `MUSDASH_PULL_REQUEST=<number>` so the app can tell. It gets none of the app's volumes or server directories. Unless the app looks at `MUSDASH_PREVIEW`, a preview talks to the same database as the app.
- **Who can get one.** Only pull requests whose branch is in the repository itself. One from a fork gets none: its code would be built and run with your variables. Anyone who can push a branch to the repository can read those variables through a preview.
- **Events.** Through a GitHub App they arrive on their own. A webhook added by hand must also send pull request events (merge request events on GitLab). The comment on the pull request is written on GitHub only.
- **The comment.** Through a GitHub App, the preview's address is written to the pull request. An App created before previews existed may lack the permission: grant "Pull requests: read and write" in the App's settings on GitHub.
- **Limit.** Ten previews per app at once.

## Databases

**Add resource** on a project page offers eight database engines. musdash generates a user and a 32-character password, creates a Docker volume for the data and starts the engine on the environment's network.

- Apps in the same environment connect by the database's name, for example `postgres://postgres:…@maindb:5432/postgres`. The Overview page has the connection string ready to copy into an app's variables.
- Nothing outside the environment can reach a database until you switch on its **public port** in Settings. It is then open on the server to anyone with the password. Docker publishes the port past ufw and firewalld, so only a firewall in front of the server, such as your provider's, can narrow who reaches it.
- Restart replaces the container and keeps the volume, so expect a few seconds of downtime. Saving a change in Settings (image tag, public port, limits) restarts a running database to apply it.
- An image already on the server is not downloaded again. To move to another version of the same major release, change the tag. A new major version usually cannot open the old data: create a new database and move the data.
- MongoDB does not start on servers whose Linux kernel is 6.19 or newer; that is MongoDB's own limitation, and its message is shown on the database's page.
- Deleting a database keeps its volume unless you tick "Also delete the data".

## Services

A service is a stack of containers described by a Docker Compose file. **Add resource** on a project page offers a small catalogue of services and "Your own Compose file".

- Give a service of the stack a web address by adding `SERVICE_FQDN_<NAME>_<PORT>` to its environment: `NAME` is the Compose service, `PORT` the port it listens on. musdash gives it a domain (a generated one at first; change it under Settings) and routes it.
- `SERVICE_URL_<NAME>` is the same address with its scheme, `SERVICE_HTTPS_<NAME>` is `true` or `false`.
- `SERVICE_PASSWORD_<ID>`, `SERVICE_USER_<ID>`, `SERVICE_BASE64_<ID>` and `SERVICE_HEX_<ID>` are filled with values generated once per service. They are listed on the service's Overview page.
- Any other `${NAME}` in the file is yours to set, in the Variables box.

These names follow Coolify's convention, so a template written for it can be pasted.

A stack is held to the same limits as an app. Its file is read inside a container that has no network and sees nothing of the server, then checked: privileged mode, the host's network or process namespaces, devices, extra capabilities, mounts of system directories, of the Docker socket or of the stack's own directory, networks with a subnet of their own, and volumes or networks that belong to something else are refused, with a message naming the line. Files the Compose file names (`env_file`, `include`, `extends`) are not read: a stack is one file plus its variables. The first deployment on a server downloads the `docker:<version>-cli` image used for that check.

A stack runs on its own network. Tick "Connect to the environment's network" if its containers and the apps and databases of the environment need to reach each other by name; names must then be unique across them.

To publish a port that is not HTTP (a mail server, a game server), use an ordinary `ports:` entry with a host port from 1024 to 65535, outside 20000 to 29999.

### Stacks from a Git repository

A service also takes a Compose file that lives in a repository ("Compose file in a Git repository"), read through the same GitHub Apps, GitLab sources and deploy keys as apps. At every deployment musdash clones the branch, reads the file in the sandbox with only the checkout in view, and applies the same checks as to a pasted file. Differences from a pasted stack:

- `build:` is allowed for contexts inside the repository. Images are built with `docker compose build`, one build at a time per server, and named by musdash.
- Files of the repository can be mounted into containers (`./nginx.conf:/etc/nginx/nginx.conf`). They are mounted read-only, and a path that is a symbolic link in the repository is refused. Data that a container writes belongs in a named volume.
- `include`, `extends` and `env_file` may name files of the repository.
- A push to the branch redeploys it, through the GitHub App or through a webhook you add to the repository; a deploy token does the same for a CI pipeline. Both are made on the Keys & tokens page, under API Tokens.
- Only the Compose file itself is scanned for `SERVICE_…` variables, not files it includes.

## More servers

Servers, "Add a server" takes another machine that has Docker and is reached over SSH. Nothing of musdash runs on it except its containers and, if you install it, the proxy that serves their domains.

1. Add the server: its address, SSH port and account, and a key (a new one is made for it unless you pick an existing one). The account must be able to run `docker`: root, or a member of the `docker` group.
2. Put the public key the page shows into that account's `~/.ssh/authorized_keys` on the server.
3. Choose **Check**. The first check records the server's host key and shows its fingerprint to compare with the server's own (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`). From then on musdash talks only to a machine that presents this key; if the server is reinstalled, "Forget host key" and check again. The check also reports Docker, the Compose plugin, git, memory, and whether the proxy can be installed.
4. Choose **Install proxy** to serve domains from that server. It copies the musdash binary into the server's data directory and starts it as the systemd service `musdash-proxy`, which needs root or `sudo` without a password. For a server of another architecture than the dashboard's, put that architecture's binary at `<data>/dist/musdash-linux-<arch>` first (`make build-linux` produces both).

With more than one server, the form a resource is added with asks which one. Point an app's domain at the server it runs on.

- A remote server keeps musdash's files (env files, Compose stacks, backups, routes, certificates) in its own data directory: `/var/lib/musdash` for root, `~/.musdash` otherwise, or what you entered.
- A command's secrets never appear in the server's process list: they travel in a private file that the command's shell reads and removes.
- One SSH connection per server is opened when first needed and closed after five idle minutes. A server with running containers is watched for their state, so its connection stays open.
- **Build server.** A Git app's Settings can name another server to build on. The image is built there and moved with `docker save` piped into `docker load` on the app's server: no registry is needed. Use it to keep builds off a small server.
- Removing a server from the dashboard changes nothing on the machine: its proxy service and data directory stay until you remove them.

## Backups, scheduled tasks and notifications

**Backups.** A database's Backups tab sets a schedule, how many backups to keep, and optionally a bucket to copy each one to. A backup is a dump made by the engine's own tool inside the database's container (`pg_dump`, `mysqldump`, `mariadb-dump`, `mongodump`, `redis-cli --rdb`), compressed and written to `<data>/backups/<database id>/`. It is streamed, so its memory use does not depend on the size of the database. Older backups beyond the number to keep are removed from the server and from the bucket.

- Restore puts a backup back into the same database after you type its name; the database keeps running. Redis backups can be downloaded but not restored from the page: an RDB file is loaded by replacing the file and restarting.
- KeyDB, Dragonfly and ClickHouse are not backed up yet. Their pages say so.
- Buckets are added under Settings, Backup storage: any S3-compatible service. Copies are made with `rclone` in a container that runs only for the upload; the keys reach it through a file that is removed afterwards. A bucket hosted on the same server is reached through its public domain. Deleting a database with its data removes its backups on the server and leaves the copies in the bucket.

**Scheduled tasks.** An app's Tasks tab runs a command in the app's running container on a schedule, with `sh -c`. Each run is listed with its exit status and output; the last 50 are kept. A run that is still going when its next time comes is not started a second time.

**Schedules** are five cron fields (`0 3 * * *`), or `@hourly`, `@daily`, `@weekly`, `@monthly`, always in UTC. A schedule whose time passed while musdash was not running fires once when it is back, however many of its times were missed.

**Notifications.** The **Notifications** page adds channels (Discord, Slack, Mattermost, Telegram, Pushover, a generic webhook, or email over SMTP) and chooses which events each one hears: a deployment finished or failed, a backup finished or failed, a scheduled task failed, a container stopped unexpectedly, the disk is nearly full. A container that keeps crashing is reported once every 15 minutes, not on every restart. A channel's address, and a bucket's endpoint, may not lead to the server's own services, to the private addresses of containers on it, or to a cloud metadata service; what such an address answers is never shown. Other machines on a private network are allowed: a self-hosted chat server or object store usually lives on one.

**Clean-up.** Once a day after 03:00 UTC, musdash removes dangling images, build cache older than a week and stopped containers whose app, database or service no longer exists. It never removes volumes, and never images that a stopped app or database still needs.

## People and roles

The account made at setup is the team's first Owner. Others join by invitation: under Team, an Owner or an Admin enters an address and gets a link to send. The link works once, for seven days; whoever opens it chooses a name and a password. There is no outgoing mail for this, so the link travels however you send it.

| | Member | Admin | Owner |
|---|---|---|---|
| Projects, apps, databases, services: create, change, deploy, delete | yes | yes | yes |
| Project and environment variables, tags, their own API tokens | yes | yes | yes |
| See servers, sources and deploy keys | yes | yes | yes |
| Add, change or remove servers, sources and deploy keys | | yes | yes |
| Settings (dashboard domain, backup storage) and Notifications | | yes | yes |
| Team and server variables | use by name | change | change |
| Invite, cancel an invitation, rename the team | | Members | Members and Admins |
| Remove a member, make them a password reset link, turn off their second step | | Members | anybody else |
| Change a role | | | yes |

- Removing a member deletes their account, their sessions and their API tokens, and withdraws the invitations they made. What they deployed stays.
- Giving somebody a higher role signs them out and ends their API tokens and reset links, so nothing made under the lower role carries over. Giving them a lower one withdraws the invitations they made.
- The team always has an Owner: the last one cannot be removed or given another role.
- Roles decide who manages the team and its servers. They do not limit what a container is given: a Member deploys containers on the team's servers under the same rules as everybody.
- One install has one team.

### Two-step sign-in

Under Account, a person can add a second step: after the password, signing in asks for a six-digit code from an authenticator app (TOTP). Setting it up shows a key to enter in the app, or to open with a password manager; there is no QR code. It is on once a code from the app has been entered, and ten recovery codes are shown then, each good for one sign-in without the phone.

- Turning it on or off asks for the password again, and turning it off for a code as well.
- A code works once. Five wrong codes lock the second step of that account for fifteen minutes.
- Lost the phone and the recovery codes: an Owner (or, for a Member, an Admin) turns the second step off from the Team page. For the only Owner, run `musdash disable-2fa <email>` on the server.
- A password reset link does not skip the second step.

## Shared variables

A value that several apps need is written once and taken by name. There are four places to keep them:

| Kept under | Named as | Who changes them |
|---|---|---|
| Team, Shared variables | `{{team.NAME}}` | Admins |
| A project's page, under **Settings** | `{{project.NAME}}` | everybody |
| An environment's Settings | `{{environment.NAME}}` | everybody |
| Servers, on a server's card | `{{server.NAME}}` | Admins |

An app or a service uses one by naming it in a variable of its own, alone or inside a longer value:

```
DATABASE_URL={{environment.DATABASE_URL}}
MAIL_URL=smtp://{{team.SMTP_HOST}}:587
```

- Nothing reaches a container that did not name it.
- Team and server variables are changed by Admins, and their values are shown only to Admins. That is not secrecy from Members: a Member can name one in an app they deploy, and the app then reads it. Keep there what the whole team may use.
- The name is replaced at each deployment with the value stored then, so a changed value takes effect at the next deploy. A preview resolves the names as its app does.
- A deployment that names a variable which does not exist fails and says which. Saving an app's variables warns about such names.
- Only this exact form is read. `{{ .Name }}` and the like, for a template engine, are left alone. An app that needs this very text for itself puts a backslash before it: `\{{environment.name}}` reaches it as `{{environment.name}}`. A shared variable's own value cannot name another one.

## Tags

A tag is a short name such as `nightly` or `frontend`. The Tags page makes, renames and deletes them, and a tag can exist before anything has it; an app's and a service's Settings page chooses among them or adds a new one. A tag's page lists what has it, and its step of the header switches to the team's other tags. Everything that has a tag is deployed with one API call (below); something that already has a deployment waiting is not queued twice.

## Search

The bar at the top of every page has a search before your own menu. Press `/` anywhere outside a field, or Cmd+K (Ctrl+K on Windows and Linux), type, and press Enter to open the first result; the arrow keys choose another.

It finds what the team has, by name and by what you would know it by: projects (and their descriptions), environments, apps (image, repository, branch), databases (engine, image), services (template, repository), domains, servers (host, address), tags, and the dashboard's own pages. Several words narrow it: `shop web` is the app `web` of the project Shop. A name from the server can be pasted whole (a container, network or volume called `musdash-<id>-…` finds what it belongs to). Variables, Compose files, keys and tokens are not searched, so a search never shows what a secret holds.

Nothing is indexed or kept in memory for it: each pause in the typing is one query on the database.

## The API

For scripts and pipelines. On the Keys & tokens page, under API Tokens, make a token: it asks for your password, is shown once, and is stored as a hash. It can end in 7, 30, 60 or 90 days or a year, or never; it acts as the person who made it, within the permissions it was given, and never does what that person may not. It stops working when they leave the team, change or reset their password, turn on two-step sign-in, or are given a higher role.

| Permission | What a token with it may do |
|---|---|
| Read (`read`) | Every `GET` below |
| Write (`write`) | Start and stop apps, databases and services |
| Deploy (`deploy`) | Start deployments |
| Read sensitive data (`read:sensitive`) | Read, with the values of an app's variables and a database's password in the answers |
| Root (`root`) | All of the above, and what later versions add |

Each permission is its own: a token that only deploys can call none of the `GET` routes, which is what a pipeline wants. (A deploy call still answers with the ids of what it queued, and with `404` for an id that does not exist.) A token made before permissions existed keeps what it could do (`read`, or `read`, `write` and `deploy`).

```bash
curl -H "Authorization: Bearer $MUSDASH_TOKEN" https://musdash.example.com/api/v1/apps
```

```bash
curl -X POST -H "Authorization: Bearer $MUSDASH_TOKEN" "https://musdash.example.com/api/v1/deploy?tag=nightly"
```

| | Route | Token |
|---|---|---|
| Who the token is | `GET /api/v1/me` | read |
| Servers, projects with their environments, tags | `GET /api/v1/servers`, `/projects`, `/tags` | read |
| Apps | `GET /api/v1/apps` (`?tag=`), `/apps/{id}` | read |
| An app's deployments, one deployment | `GET /api/v1/apps/{id}/deployments` (`?limit=`), `/deployments/{id}` | read |
| An app's variables: their names, and their values for `read:sensitive` | `GET /api/v1/apps/{id}/envs` | read |
| Databases and services (a database's `password` for `read:sensitive`) | `GET /api/v1/databases`, `/databases/{id}`, `/services`, `/services/{id}` | read |
| Deploy an app or a service | `POST /api/v1/apps/{id}/deploy`, `/services/{id}/deploy` | deploy |
| Deploy several at once | `POST /api/v1/deploy?uuid=ID,ID&tag=TAG,TAG` | deploy |
| Stop an app or a service | `POST /api/v1/apps/{id}/stop`, `/services/{id}/stop` | write |
| Start or stop a database | `POST /api/v1/databases/{id}/start`, `/databases/{id}/stop` | write |

- Answers are JSON; an error is `{"error": "…"}` with a 4xx or 5xx status. Times are Unix seconds.
- A deploy answers `202` with the deployment's id, which `GET /api/v1/deployments/{id}` follows until its `status` is `success` or `failed`. When a deployment that had not started yet will do what the call asked for, the status is `waiting` and nothing more is queued.
- `POST /api/v1/deploy` deploys nothing when one of its ids is unknown.
- The API never returns a key, a hash or a webhook secret, and returns a variable's value or a database's password only to a token with `read:sensitive`. A value that names a shared variable (`{{team.NAME}}`) is returned as that name. `GET /api/v1/me` lists the token's permissions as `abilities`.
- It reads and operates. Creating and configuring are done in the dashboard.
- Limits: 120 calls a minute for a token and 600 for an address; beyond that the answer is `429` with `Retry-After`.
- The API takes a token and nothing else: a browser's session is not accepted there, and a token is not accepted by the dashboard's pages.

## A terminal, and what things use

**Terminal.** Apps, databases and services have a Terminal tab: a shell in the running container (`bash` where the image has it, `sh` otherwise), as the user the image runs as. It is a shell in the container, never on the server. It ends when you leave the page.

- It is drawn by a small script of musdash's own, not by a terminal library: shells and full-screen programs (`vi`, `less`, `top`, `psql`) work, with colours, a scrollback of 2000 lines and copy by selecting. The mouse is not passed to programs, and double-width characters (CJK, emoji) take one column, so a line that holds them is misaligned.
- At most eight terminals are open at once, and one that carries nothing for thirty minutes is closed.
- Behind a proxy of your own in front of musdash's, WebSocket upgrades must be passed on.

**Metrics.** The Metrics tab of an app, a database or a service shows what its containers use now (processor, memory, network, disk), read from `docker stats` while the page is open and not otherwise. Servers, "Processor, memory and disk" shows the same for a server (from `/proc` and `df`; Linux only) and for every container on it.

History is off until you switch **sampling** on for a server on that page. Then, once a minute, one sample of the server and of each app, database and service on it is stored, and kept for a day; the pages draw it as charts over 1, 6 or 24 hours. Switching it off removes the samples.

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

Loads Compose files in the sandbox (including ones that try to read files of the server, and every catalogue template), then runs a two-container stack through deploy, redeploy, stop and delete.

```bash
MUSDASH_DOCKER_TEST_SERVICES=1 go test ./internal/deploy -run TestCatalogueWithDocker -v -timeout 90m
```

Installs the catalogue's templates for real, fetches each web address, redeploys and deletes. `MUSDASH_SERVICES=wordpress,minio` limits it to those.

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
- Throwaway sign-in values for local testing are in [dev.example.env](dev.example.env).

### Front end

Pages are rendered on the server with [templ](https://templ.guide) and updated with [htmx](https://htmx.org). There is no Node toolchain and no front-end framework: interactive behaviour is one script of about 5 KB compressed, driven by `data-` attributes, which lets the Content-Security-Policy forbid inline script and style.

How the pages are laid out:

- The bar at the top is where you are: the project, its environment and the resource, each a switcher to the others beside it. The address says the same: `/projects/<project>/env/<environment>` for an environment and `/projects/<project>/env/<environment>/app/<id>` (or `database`, `service`) for what is in it; a project's own address, `/projects/<project>`, is its page. A project's page lists its environments as cards, each with what is in it counted, and has two buttons: **Add environment**, and **Settings**, a dialog with the project's name and description, its shared variables and deleting it. An environment's page lists what runs in it, and its own **Settings** hold its name, its shared variables and deleting it. The short address of a resource, `/apps/<id>`, still leads to its page: it is what a notification's link holds.
- Projects, and what is in an environment, are tiles. Apps, databases and services are all resources and are added from one page, **Add resource**.
- A form of up to five fields is a dialog, opened from a button beside what it changes. Anything that stops, removes or replaces something asks first; deleting a project, an app, a database or a service asks for its name.
- Keys, tokens and webhook secrets of the whole team are on one page, **Keys & tokens**, in two tabs: **Private Keys** (SSH key pairs) and **API Tokens** (a person's API tokens, deploy tokens and webhook secrets, in one table). No other page holds them or points at them. An app's domains are on its own **Domains** tab.

The design system lives in two places:

- Tokens and component styles: `internal/web/assets/input.css`
- Components: `internal/web/ui`

Icons are from the free [Hugeicons](https://hugeicons.com) set, Stroke Rounded (MIT licence, `internal/web/ui/icons.LICENSE`). The logos of the notification channels (Discord, Slack, Mattermost, Telegram, Pushover) are from the [selfh.st icon collection](https://selfh.st/icons) (CC BY 4.0, `internal/web/static/logos.LICENSE`, which says what was changed); each is a trademark of its owner.

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
install            systemd units and the install script
test               whole-binary tests
```
