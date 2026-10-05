# musdash full-feature VPS test plan

**Goal:** exercise every feature of musdash (phases 0–9) on a real Linux VPS, record a pass/fail/blocked result for each case, and report what is broken.

**Target:** Ubuntu 24.04, 1 vCPU, 2 GB RAM (+2 GB swap added), Docker 29, public IPv4 `168.235.65.204`. Installed with `install/install.sh` from a fresh `make build-linux` binary (`adffb82`).

**Spec:** `docs/spec.md`, `README.md`, `docs/plans/phase-0…9`.

## How the tests are run

Three tools, each used where it is the best fit:

| Tool | Used for |
|---|---|
| `test/vps/` Python harness (stdlib only, runs on the Mac against the VPS) | Everything that is HTTP: sign-in, forms (the harness fills each form from the page's own fields and CSRF token), the API, webhooks with real HMAC signatures, proxy behaviour, rate limits. Each case records `PASS/FAIL/BLOCKED` with evidence into `test/vps/results.jsonl`. |
| SSH to the VPS | Ground truth that the UI cannot show: `docker ps`, file modes, systemd state, process RSS, `routes.json`, journal, killing processes, restarts. |
| Browser (Playwright MCP) | What needs a real browser: every page renders without console errors, HTMX/SSE live logs, the WebSocket terminal, responsive layout, screenshots. |

Result vocabulary: **PASS** (behaved as documented), **FAIL** (a defect: documented behaviour not met, a crash, a leaked secret), **BLOCKED** (cannot be tested here, with the reason), **N/A**.
Every FAIL gets a severity: **S1** security or data loss, **S2** feature broken, **S3** wrong or confusing, **S4** cosmetic.

Test data uses the prefix `t-` (projects, apps) so it is easy to find and remove. Public `*.168.235.65.204.sslip.io` names resolve to the VPS, so domains and Let's Encrypt certificates work without owning a domain.

## Environment limits (decided up front)

| Limit | Consequence |
|---|---|
| One VPS only | Phase 6 is tested against a second "server" that is a container running `sshd` with its own Docker daemon (Docker-in-Docker) on this VPS; its proxy install is checked up to the step that would collide with ports 80/443. Marked partial in the report. |
| No GitHub App / GitHub account | The App-creation flow is tested up to the redirect to GitHub; push and pull-request webhooks are tested with synthetic payloads signed with the app's real webhook secret (GitHub, GitLab, Bitbucket, Gitea shapes). |
| 2 GB RAM, 1 vCPU, ~16 GB disk | Heavy suites (Nixpacks/Railpack, all 8 database engines, the service catalogue) run one at a time and images are pruned between suites. |
| Docker Hub anonymous pull limit | Image-heavy suites are ordered so the most valuable ones run first. |
| Cloudflare Tunnel needs a real token | Template is deployed to the point of the container starting and refusing the fake token. |
| Reboot test needs the VPS to come back | Done last. |

## Suites

Case IDs are stable (`S<suite>.<n>`); the report lists every one.

### S0 Install and baseline

| ID | Case | Expected |
|---|---|---|
| S0.1 | Installer ran clean on a fresh box | `musdash-server` and `musdash-proxy` active, `musdash` user exists, binary at `/usr/local/bin/musdash`, version `adffb82` |
| S0.2 | Listening ports | 8000 (dashboard), 80 and 443 (proxy); nothing else public |
| S0.3 | File modes | `master.key` 0600; data dir not world-readable; `routes.json` 0600 |
| S0.4 | Idle RSS | server < 30 MB, proxy < 20 MB (after a minute idle) |
| S0.5 | Unit hardening | `systemctl show` for NoNewPrivileges/ProtectSystem etc. matches `install/*.service` |
| S0.6 | `/healthz` | 200 with no session |
| S0.7 | Re-running the installer (upgrade path) | idempotent; data and master key kept; apps keep serving |
| S0.8 | `musdash version`, `migrate` on an up-to-date DB | version printed; migrate is a no-op |
| S0.9 | Server restart with a deployed app | app keeps answering through the proxy during `systemctl restart musdash-server` |
| S0.10 | Proxy restart | routes reload from `routes.json`, apps answer again within seconds |

### S1 First run, sign-in, sessions, headers

| ID | Case | Expected |
|---|---|---|
| S1.1 | Unauthenticated `GET /` | redirect to `/setup` (no owner yet) |
| S1.2 | Setup validation | short/weak password, mismatched confirmation, bad email refused with a message |
| S1.3 | Create the owner | succeeds, signed in, setup closes (`/setup` then redirects to login) |
| S1.4 | Setup cannot be replayed | second `POST /setup` refused |
| S1.5 | Logout, login | works; logout invalidates the old cookie server-side |
| S1.6 | Wrong password / unknown email | same message, no user enumeration, timing roughly equal |
| S1.7 | Login rate limit | repeated failures slow down or lock (limiter), without locking the right user out from another address |
| S1.8 | CSRF | every POST without the token → 403; with another session's token → 403 |
| S1.9 | Cookie flags | `HttpOnly`, `SameSite`, `Path`; `Secure` once on HTTPS |
| S1.10 | Security headers | CSP with no `unsafe-inline` script, `X-Frame-Options`/frame-ancestors, `nosniff`, referrer policy |
| S1.11 | Static assets | `/static/app.css?v=…` immutable cache, gzip; traversal (`/static/../…`, `%2e%2e`) refused |
| S1.12 | Unknown route / method | 404 / 405, no stack traces |
| S1.13 | Route-by-role table | every route refuses a signed-out client (redirect or 401/403), nothing leaks a page |
| S1.14 | `reset-password <email>` CLI | prints a one-time link; link works once; old sessions ended |

### S2 Account: profile, password, two-step, API tokens

| ID | Case | Expected |
|---|---|---|
| S2.1 | Edit profile name | saved |
| S2.2 | Change password | needs current password; old password stops working; other sessions and API tokens end |
| S2.3 | TOTP setup | key shown once, enable only after a valid code, 10 recovery codes shown once |
| S2.4 | Sign in with TOTP | password then code; no session before the code |
| S2.5 | TOTP replay | the same code a second time is refused |
| S2.6 | Wrong codes lockout | 5 wrong codes lock the second step 15 min; counted per account |
| S2.7 | Recovery code | works once only |
| S2.8 | Turn two-step off | needs password and a code |
| S2.9 | `disable-2fa <email>` CLI | clears the second step |
| S2.10 | Create API token | needs password; shown once; only SHA-256 stored; scope read / deploy; optional expiry |
| S2.11 | Delete API token | token stops working at once |

### S3 Team, roles, invitations

| ID | Case | Expected |
|---|---|---|
| S3.1 | Invite a Member, Admin | link shown once, expires, works once |
| S3.2 | Accept invitation | chooses name/password, joins with the invited role |
| S3.3 | Role matrix | for Member, Admin, Owner: every row of the README role table (servers, settings, team variables, invitations, remove member, role change) allowed or refused with 403 |
| S3.4 | Admin cannot manage Admin/Owner | remove / reset / 2FA-off / role change refused |
| S3.5 | Raise a role | person signed out; tokens and reset links ended |
| S3.6 | Lower a role | invitations they made withdrawn |
| S3.7 | Remove a member | account, sessions, tokens gone; what they deployed stays |
| S3.8 | Last Owner protected | cannot be removed or demoted |
| S3.9 | Reset link for a member | works once; does not skip their two-step |
| S3.10 | Cancel an invitation | link dead |
| S3.11 | Rename team | admin/owner only |

### S4 Projects and environments

| ID | Case | Expected |
|---|---|---|
| S4.1 | Create project; default environment | listed on dashboard |
| S4.2 | Rename / edit project | saved |
| S4.3 | Add environment, delete environment | resources inside go with it, containers removed |
| S4.4 | Name validation | empty, too long, unicode, `../`, shell metacharacters refused or stored inert |
| S4.5 | One namespace per environment | an app, database and service cannot share a name in one environment; the same name in another environment is fine |
| S4.6 | Delete project | cascades, containers and networks removed on the server |

### S5 Image apps and deployments

| ID | Case | Expected |
|---|---|---|
| S5.1 | New app from `nginx:alpine` | deployment runs: pull, network, env file, container on a loopback port, health check, route, old container stopped |
| S5.2 | Generated sslip.io domain serves over HTTP | `curl` through the proxy returns nginx |
| S5.3 | Live deploy log | SSE stream shows lines in order and ends with the status |
| S5.4 | Container shape | named `musdash-…`, labelled, loopback-published port in 20000–29999, restart policy, on the environment network, not privileged |
| S5.5 | Redeploy without dropping a request | 20 requests/s through the proxy during redeploy: zero failures |
| S5.6 | Failed deploy keeps the old one | bad image tag fails, previous container still serves, new container removed |
| S5.7 | Health check: path | custom path expecting 200; failing path fails the deploy |
| S5.8 | Health check: command, and port-only | both honoured |
| S5.9 | Stop and start | status changes, route removed/added |
| S5.10 | App logs page and stream | recent lines and live follow |
| S5.11 | Environment variables | plain, secret (sealed, not shown again), build-time flag; reach the container; env file mode 0600; not visible in `docker inspect` args |
| S5.12 | Storage: volume and file mount | file content written in the UI appears in the container; volume persists across redeploy |
| S5.13 | Resource limits | memory/CPU limits applied to the container |
| S5.14 | Extra `docker run` options allow-list | `--privileged`, `-v /:/host`, `--pid=host`, `--cap-add`, docker.sock all refused; an allowed one accepted |
| S5.15 | Bind-mount source guard | docker socket, `/etc`, the musdash data dir refused |
| S5.16 | Image reference injection | `nginx --privileged`, `-v …`, newline, `;` in the image field refused |
| S5.17 | Delete app | container, env file, route, volume rules as documented |
| S5.18 | Two deploys queued | serialised per app lock, no overlap |
| S5.19 | Deployment history and kept images | last five images kept under `musdash/<app>:d-<id>` |
| S5.20 | Rollback | earlier deployment re-runs with no pull; rolled-back image content verified; settings unchanged |
| S5.21 | Rollback boundary | cannot roll back to another app's image (tampered id → 404/refused) |

### S6 Domains, TLS and the proxy

| ID | Case | Expected |
|---|---|---|
| S6.1 | Add a second domain | both answer |
| S6.2 | Unknown host | proxy's "nothing is deployed" page, no route data leaked |
| S6.3 | HTTPS issuance | first request on a TLS host gets a Let's Encrypt cert for `*.168.235.65.204.sslip.io` |
| S6.4 | HTTP→HTTPS redirect | TLS hosts redirect; HTTP-only hosts proxy; ACME challenge path answered |
| S6.5 | No cert for unrouted host | SNI of an unrouted name is refused, no ACME order made |
| S6.6 | Path routes | `/api` to app B, rest to app A on one host; longest prefix wins; `/apix` does not match `/api` |
| S6.7 | Strip prefix | app sees `/` |
| S6.8 | Password gate | 401 without credentials, 200 with; wrong password 401; hash in `routes.json`; credentials cached 5 min |
| S6.9 | Path canonicalisation and Guard | `/Admin`, `//admin`, `/a/../admin`, `%2e` forms hit the gate when `/admin` is guarded |
| S6.10 | Proxy routes by Host only to loopback | route table refuses non-loopback targets (unit evidence + `routes.json` inspection) |
| S6.11 | `routes.json` updated and SIGHUP on every change | route appears/disappears within a second |
| S6.12 | WebSocket and streaming through the proxy | upgrade works to an app that uses it |
| S6.13 | Dashboard on a domain (Settings) | dashboard answers on `dash.168.235.65.204.sslip.io` over HTTPS; a path under it cannot be given to an app |
| S6.14 | `X-Forwarded-*` headers | app sees correct host/proto/for |
| S6.15 | Large body and slow client | proxy streams, memory stays flat |

### S7 Git deploys

Repository used: a small public Dockerfile repo on GitHub (also a static-site repo and a repo with no Dockerfile).

| ID | Case | Expected |
|---|---|---|
| S7.1 | Public repo, Dockerfile build | clone → `git ls-tree` symlink check → `docker build` → serves |
| S7.2 | Static-site build pack | nginx serves the directory; SPA fallback works |
| S7.3 | Build args | build-time variables reach the build; runtime-only ones do not |
| S7.4 | Bad repo URL / branch / path | `-x`, `ext::`, `file://`, spaces, `;` refused; nonexistent repo fails cleanly with a log |
| S7.5 | Dockerfile path traversal | `../../etc/passwd`-style paths refused |
| S7.6 | Build timeout and OOM behaviour | a runaway build is killed, server stays up |
| S7.7 | Nixpacks build | builder image made once, plan produced, app built and serves `PORT` |
| S7.8 | Railpack build | same for Railpack |
| S7.9 | Deploy key | generated, public half shown, private half sealed; delete key |
| S7.10 | GitHub App creation | the flow redirects to GitHub with the right manifest (blocked beyond that) |
| S7.11 | Push webhook (GitHub) | valid HMAC deploys; bad signature, wrong id, missing secret all return the same response |
| S7.12 | GitLab / Bitbucket / Gitea push payloads | each shape accepted with its own proof |
| S7.13 | Webhook branch filter | push to another branch does nothing |
| S7.14 | Webhook rate limit by address | burst limited |
| S7.15 | Deploy token | `POST /api/v1/deploy` with the app's token deploys only that app; another app's id refused |
| S7.16 | Webhook secret regeneration | old secret stops working |
| S7.17 | Previews: pull-request opened/updated/closed | preview app created at `pr-N-app.<preview domain>`, rebuilt on push, removed on close |
| S7.18 | Previews: fork PR | no preview; head repo ≠ base repo ignored |
| S7.19 | Previews: limits | 11th preview refused; preview has no settings of its own; settings routes turn a preview away |
| S7.20 | Build server | built on one server, `docker save | docker load` to another (needs S12 second server) |

### S8 Databases

| ID | Case | Expected |
|---|---|---|
| S8.1 | PostgreSQL create, start, connect from another container by name | healthy; query works with the generated password |
| S8.2 | Redis, MySQL, MariaDB, MongoDB, KeyDB, Dragonfly, ClickHouse | each starts healthy and accepts a connection (one at a time, deleted after) |
| S8.3 | Connection string on Overview | correct host, user, port, password |
| S8.4 | Restart keeps data | row written before restart present after |
| S8.5 | Public port | opens on the server; closing removes it; reachable from the Mac with the password only |
| S8.6 | Settings change (tag, limits) | restarts the container with the new settings |
| S8.7 | Password handling | env file 0600; password not in process args or `docker inspect` Cmd |
| S8.8 | Logs, metrics, status | pages and streams work |
| S8.9 | Stop, start, via UI and via API | status follows |
| S8.10 | Delete without data / with data | volume kept / removed accordingly |
| S8.11 | One container at a time | restart never leaves two containers on the volume |
| S8.12 | Image reference and option injection in engine tag | refused |

### S9 Backups and storage

| ID | Case | Expected |
|---|---|---|
| S9.1 | Manual backup of Postgres, MySQL, MariaDB, MongoDB, Redis | file in `<data>/backups/<id>/`, gzip valid, size > 0 |
| S9.2 | Schedule | cron field parsed (UTC); `next_run` set; due schedule fires and creates a backup |
| S9.3 | Retention | keeps N newest, removes older from disk |
| S9.4 | Restore | after typing the name, a deleted table comes back; database keeps running |
| S9.5 | Restore refusal | wrong name refused; Redis restore not offered; KeyDB/Dragonfly/ClickHouse say not backed up |
| S9.6 | Download | file streams, valid gzip |
| S9.7 | Backup storage (S3) | MinIO deployed as a service; bucket added; Test succeeds; backup copied; retention removes it from the bucket too |
| S9.8 | Backup storage SSRF | endpoint pointing at `127.0.0.1`, container IPs or `169.254.169.254` refused |
| S9.9 | Crash recovery | kill the server during a backup: row becomes failed, no stuck `running` row |

### S10 Services (Docker Compose)

| ID | Case | Expected |
|---|---|---|
| S10.1 | Catalogue: Uptime Kuma, n8n, Ghost, WordPress, MinIO | each deploys, endpoint domain answers, magic variables generated and listed |
| S10.2 | Cloudflare Tunnel template | container starts and refuses the fake token (deploy handles it) |
| S10.3 | Own Compose file | multi-container stack deploys on its own network |
| S10.4 | Validation refusals | `privileged`, `network_mode: host`, `pid: host`, `cap_add`, `devices`, docker.sock mount, `/etc` mount, data-dir mount, custom subnet, external volume/network, unknown key, `musdash/` image name: each refused with a message naming the line |
| S10.5 | Sandbox | a file using `include`, `extends`, `env_file`, `${…}` reading host paths reads nothing of the server |
| S10.6 | `SERVICE_FQDN_*`, `SERVICE_URL_*`, `SERVICE_PASSWORD_*`, `SERVICE_BASE64_*` | domains routed, secrets generated once and stable across redeploys |
| S10.7 | Variables box, `${NAME}` | substituted; missing variable reported |
| S10.8 | Connect to environment network | name collisions refused |
| S10.9 | Non-HTTP `ports:` | 1024–65535 outside 20000–29999 accepted; others refused |
| S10.10 | Stop, deploy, delete a service | containers/volumes follow the documented rules |
| S10.11 | Service from Git | compose read from a fresh clone, repo mounted read-only, `build:` inside repo OK, symlink refused |
| S10.12 | Service endpoint domains and TLS | endpoint domain gets a route and a cert |

### S11 Operations: tasks, notifications, clean-up

| ID | Case | Expected |
|---|---|---|
| S11.1 | Scheduled task create/edit/delete | listed with next run |
| S11.2 | Run now | `docker exec sh -c` in the serving container; exit status and output recorded |
| S11.3 | Due task fires on its own | `* * * * *` task runs within 70 s; no overlap if still running |
| S11.4 | Task history | only the last 50 kept |
| S11.5 | Notification channels: generic webhook, Discord, Slack, Telegram, Pushover, Mattermost, email (SMTP) | created; Test delivers (webhook receiver and an SMTP sink run on the VPS) |
| S11.6 | Event routing | deploy success/fail, backup success/fail, task failure, container died each reach only channels that chose them |
| S11.7 | Crash notification throttle | a crash-looping container notifies once per 15 min |
| S11.8 | Channel SSRF guard | loopback, link-local, container IPs, metadata address, DNS rebinding name, redirect to loopback all refused; private LAN address allowed |
| S11.9 | Docker clean-up | dangling images and stopped orphan containers removed; volumes never removed (triggered by calling the job, 03:00 gate noted) |
| S11.10 | Disk-nearly-full event | threshold logic exercised if reachable, else code-path evidence |

### S12 Servers, metrics, multi-server

| ID | Case | Expected |
|---|---|---|
| S12.1 | Local server card | Docker, Compose, git, public IP shown correct |
| S12.2 | Server metrics page | CPU, memory, disk, per-container figures plausible against `free`, `df`, `docker stats` |
| S12.3 | Sampling on/off | samples stored once a minute, charts for 1/6/24 h, off removes them |
| S12.4 | App / database / service Metrics tabs | values appear while the page polls, polling stops after the cap |
| S12.5 | Add remote server | key generated, public key shown, address validation |
| S12.6 | Check | first contact records the host key and shows the fingerprint (compare with `ssh-keygen -lf`) |
| S12.7 | Host key change | refused until "Forget host key" |
| S12.8 | Remote command safety | hostile server answers (architecture, versions, file listings) are treated as data |
| S12.9 | Deploy an app to the remote server | container runs on the remote's Docker; routing note |
| S12.10 | Install proxy on remote | copies binary, writes systemd unit (checked on the container host; port collision noted) |
| S12.11 | Server variables | Admin-only; `{{server.NAME}}` expands |
| S12.12 | Remove server | nothing on the machine changes |
| S12.13 | SSH connection pooling | one connection, closed after idle |

### S13 Shared variables and tags

| ID | Case | Expected |
|---|---|---|
| S13.1 | `{{team.X}}`, `{{project.X}}`, `{{environment.X}}`, `{{server.X}}` | each expands in an app's env file at deploy |
| S13.2 | Stored text keeps the name | the stored variable still shows `{{…}}` |
| S13.3 | Escape `\{{…}}` | reaches the app literally |
| S13.4 | No nesting | a shared value containing `{{…}}` is not expanded again |
| S13.5 | Missing name | deployment fails naming the variable (not a value); saving warns |
| S13.6 | No inheritance | a container that did not name the variable does not see it |
| S13.7 | Roles | Member cannot change team/server variables, can name them; values hidden from Members |
| S13.8 | Services | `{{…}}` expands in a service's variables file |
| S13.9 | Tags | tag an app and a service; Tags page lists; Deploy all queues each once; second click does not double-queue |
| S13.10 | Variables survive cascades | deleting a project removes its shared variables, other teams' (n/a) untouched |

### S14 API

| ID | Case | Expected |
|---|---|---|
| S14.1 | Every GET route with a read token | 200 JSON with documented fields |
| S14.2 | Secrets never returned | scan all GET responses for variable values, passwords, key material (`TestAPINeverReturnsSecrets` equivalent against the live server) |
| S14.3 | Scope | read token refused on POST deploy/stop/start (403) |
| S14.4 | Deploy, stop for apps, services; start, stop databases | 202 and status follows `GET /deployments/{id}` to `success` |
| S14.5 | `POST /deploy?uuid=…&tag=…` | deploys the set; one unknown id deploys nothing |
| S14.6 | Waiting status | second deploy while one is waiting is not queued twice |
| S14.7 | No token / bad token / expired / revoked | 401 with `{"error":…}` |
| S14.8 | Session cookie is not accepted by the API; token is not accepted by pages | both refused |
| S14.9 | Rate limit | > 120/min per token → 429 with `Retry-After` |
| S14.10 | Token life-cycle events | password change, 2FA enable, role raise, member removal each kill the tokens |
| S14.11 | CORS / method | no permissive CORS headers |

### S15 Terminal

| ID | Case | Expected |
|---|---|---|
| S15.1 | App terminal (browser) | bash/sh prompt, typing works, colours, `top`/`vi` full-screen, resize |
| S15.2 | Database terminal (`psql`) and service terminal | connect to right container |
| S15.3 | WebSocket handshake rules | cross-origin `Origin` refused; missing/bad form token in first message refused; no session refused |
| S15.4 | Concurrency cap | ninth terminal refused |
| S15.5 | Idle close | an idle terminal closes (timer evidence) |
| S15.6 | Cleanup | leaving the page ends the `docker exec` process |
| S15.7 | Frame limits | 64 KB+ message closes the socket, server stays up |
| S15.8 | Container name from DB only | a tampered `id` for another team's container gives 404 |

### S16 Security probes

| ID | Case | Expected |
|---|---|---|
| S16.1 | Shell/option injection in every free-text field that reaches a command: image, tag, name, branch, repo URL, health command, task command (task command *is* a shell by design: confirm it runs only inside the container), domains, storage paths, labels | no host effect; refused or inert |
| S16.2 | Domain validation | IPs, `localhost`, wildcard abuse, dashboard's own domain, a domain already used by another resource refused |
| S16.3 | XSS | `<script>` in names, env values, logs (escaped), deployment output, error messages |
| S16.4 | IDOR | ids of other resources forged across kinds (app id used on `/databases/…`) → 404 |
| S16.5 | Secrets at rest | env_vars, backup keys, deploy keys, webhook secrets sealed; `grep` of the SQLite file for a known plaintext finds nothing |
| S16.6 | Logs | journal and deployment logs never contain a password, token, or secret variable value |
| S16.7 | Process list | during a deploy with secrets, `ps` shows none of them |
| S16.8 | Log-streaming caps | 17th SSE stream refused |
| S16.9 | Webhook indistinguishability | unknown id, missing secret, bad signature produce identical responses |
| S16.10 | Docker socket and host files | no app/service/terminal can reach them (verified from inside containers) |
| S16.11 | Upload/size limits | oversized form body and header refused |
| S16.12 | TLS | cipher/protocol sanity on port 443 (TLS 1.2+) |

### S17 Resilience and resource budget

| ID | Case | Expected |
|---|---|---|
| S17.1 | `kill -9` server during a deploy | job requeued on start, deployment ends consistently, old app still serves |
| S17.2 | `kill -9` server during a backup | row failed by `Recover` |
| S17.3 | `kill -9` proxy | systemd restarts it; routes restored; server unaffected |
| S17.4 | `docker events` monitor | `docker kill` of an app container → status changes to stopped/crashed, notification once; restart policy brings it back |
| S17.5 | `docker rm -f` of an app container | status reflects it; redeploy recovers |
| S17.6 | Disk pressure | fill a volume of an app; musdash stays up |
| S17.7 | RSS under load | server and proxy RSS while serving 50 hosts / 100 deploy events stay near budget |
| S17.8 | Memory limits | `GOMEMLIMIT` defaults 48 MiB/32 MiB present in the unit/env |
| S17.9 | Concurrency | 10 simultaneous API deploys: all serialised or run per the pool, none lost, none doubled |
| S17.10 | Reboot | after `reboot`: both services start, containers with restart policy return, apps routed, SQLite intact |

### S18 UI sweep (browser)

| ID | Case | Expected |
|---|---|---|
| S18.1 | Every page of every kind loads | no console errors, no 5xx, no missing asset |
| S18.2 | HTMX interactions | status polling, forms, confirmation dialogs, flash messages, autodismiss |
| S18.3 | Mobile (375 px) and tablet | nav toggle works, no horizontal scroll |
| S18.4 | Design rules | light mode only, blue brand, no external font/CDN requests (network log) |
| S18.5 | Keyboard and focus | forms tabbable, focus visible, dialogs closable |
| S18.6 | Screenshots | one per major page for the report |

## Execution order

1. S0, S1, S2 (core, quick). 2. S3, S4. 3. S5, S6 (core product). 4. S14, S13, S16 (cheap, high value). 5. S8, S9 (databases and backups), S11. 6. S10 (services; MinIO first because S9.7 needs it). 7. S7 (Git; Nixpacks/Railpack last, they are heaviest). 8. S12 (second server). 9. S15, S18 (browser). 10. S17 (destructive, last), reboot at the very end.

After each suite: log results to `test/vps/results.jsonl`, `docker system prune` for unused images, check disk and RSS. At the end: write `docs/testing/vps-test-report.md` (summary table per suite, every FAIL with severity, reproduction and evidence, BLOCKED reasons, memory figures, screenshots list).

## Review focus (inputs most likely to bite, each owned by a case above)

1. Hostile or odd text in names, images, repo URLs and domains — S4.4, S5.16, S7.4, S16.1, S16.2.
2. A process killed at the worst moment — S17.1–S17.3, S9.9.
3. Role boundaries after a role or password change — S3.3–S3.7, S14.10.
4. A path the proxy and an app read differently — S6.9.
5. A second request arriving while the first is still running — S5.18, S14.6, S17.9, S2.5.

## Cleanup

Test data is left in place until the report is read; `t-` prefixed projects can be deleted from the dashboard. Nothing outside the VPS is changed. The harness holds the root password only in the scratchpad, never in the repository.
