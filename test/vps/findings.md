
## Findings

Severity: **S1** security/data loss · **S2** feature broken · **S3** wrong or confusing · **S4** cosmetic. No S1 was found. The 8 FAIL rows in the tables are F1 (twice), F2, F3, F4, F5, F8 and F9; F6 and F7 are recorded as 'info' because they are environment/observability gaps rather than failed expectations.

### Defects

| # | Sev | Finding | Evidence |
|---|---|---|---|
| F1 | S2 | **Redis and KeyDB cannot restart in place.** `redisCommand`/`keydbCommand` in `internal/catalog/databases.go:104-105` write `/tmp/musdash.conf` with `umask 077` and `chown redis|keydb` it. The second start of the same container runs as root and cannot reopen that file: Ubuntu's `fs.protected_regular=2` forbids opening a file you don't own in a sticky world-writable directory, even for root. Result: `Restarting (1)` forever with `sh: can't create /tmp/musdash.conf: Permission denied`; musdash shows the database `exited`. Hits every reboot, `docker restart`, daemon restart and crash. Pressing Restart in the UI works because that creates a fresh container. Postgres, MySQL, MariaDB, MongoDB (and the three untested-for-restart engines' templates without this pattern) are not affected. **Fix:** `rm -f` the file before writing, or write it into a root-owned directory (e.g. `/run` or a path created by the command), or pass the password with `--requirepass` from the environment. | S8.13 (reproduced on a fresh Redis), S17.14, S17.10b; `sysctl fs.protected_regular = 2` |
| F2 | S2 | **A terminal's shell outlives its page.** Closing the WebSocket kills the host-side `docker exec` client but not the shell in the container (a PTY exec is owned by the daemon). Each open/close leaves one more `sh` running inside the app, database or service container until it restarts (11 → 14 after three cycles; a shell ended with `exit` leaves nothing). Contradicts "It ends when you leave the page" and slowly burns RAM in a 2 GB box. **Fix:** on close, signal the exec'd process (e.g. exec `kill -HUP` via the exec id, or run the shell under `timeout`/with a wrapper that exits on stdin EOF). | S15.6c, S15.6d |
| F3 | S3 | **Remote servers need `AllowTcpForwarding yes`, and nothing says so.** The health check of a remote app tunnels through SSH. With the Alpine/hardened default (`AllowTcpForwarding no`) a deploy fails after 60 s with `ssh: rejected: administratively prohibited (open failed)`, while **Check reports the server "Ready"**. Not in the README's remote-server steps. **Fix:** document it, and make Check open a test forward and report it. | S12.9f |
| F4 | S3 | **Host-key fingerprint is for whichever key type was negotiated, and the page doesn't say which.** On a server offering ECDSA, ED25519 and RSA keys, musdash recorded the ECDSA one, but the README tells you to compare with `ssh_host_ed25519_key.pub`, which will not match. **Fix:** show the key type, or prefer/pin ed25519, or list all fingerprints. | S12.6b, S12.6d |
| F5 | S3 | **Deleting a project leaves its empty environment network** (`musdash-<environment id>`) behind. One orphan Docker network per deleted project; harmless but never cleaned (the daily clean-up does not remove networks). | S4.6 |
| F6 | S3 | **Docker builds had no DNS on this VPS.** On Ubuntu 24.04 + Docker 29.8 (containerd image store) + systemd-resolved, `RUN nslookup` fails inside `docker build` while `docker run` resolves. Every Git build that downloads anything fails (Railpack: `dns error: failed to lookup address information`). Fixed by `{"dns":["8.8.8.8","1.1.1.1"]}` in `/etc/docker/daemon.json`. This is the host, not musdash, but `install.sh` and Servers → Check could detect it (`docker build` of a one-line `RUN getent hosts`) and warn. After the fix Railpack built in 64 s and Nixpacks in 189 s. | S12.14, S7.7, S7.8 |
| F7 | S3 | **Failed or hanging ACME orders leave no trace.** The proxy's `autocert.Manager` has no logger; an order that never completes (an app domain on `traefik.me`) made the TLS handshake block 100+ s with nothing in the journal. A working order (dashboard domain on `sslip.io`) took 5 s. **Fix:** log order start/failure and bound the handshake wait. | S6.3c, S6.3d |
| F8 | S4 | Project names containing a NUL byte or a newline are accepted and stored (escaped on output, no XSS). | S4.4e |
| F9 | S4 | Image reference check is lenient: `NGINX:ALPINE` (uppercase repository) and a registry port `99999` are accepted; Docker rejects them later at pull time. | S5.16.8, S5.16.13 |

### Findings from the second session (full detail in [vps-test-report-appendix-b.md](vps-test-report-appendix-b.md))

The other session ran its own S8–S17 suites on the same VPS (data in `test/vps/out/results-b.jsonl`), confirmed F1 and the remote-sshd requirement, and found these additional issues, which I have not independently reproduced:

| # | Sev | Finding |
|---|---|---|
| B2 | S3 | `kill -9` of `musdash-server` during a deploy leaves the half-started new container running; nothing removes it, not even the next successful deploy (two containers per app). I saw the same symptom during a disturbed re-run (two stray app containers after server restarts), though my own undisturbed `kill -9` test (S17.1) ended with one container. |
| B3 | S3 | An `external: true` volume in short syntax (`- v:/x`) is not refused by the validator; it reaches `docker compose up` and fails with a Docker error (matches what I saw in S10.4v). |
| B4 | S4 | `kill -9` mid-backup leaves a hidden `.musdash-<n>` temp file (39 MB) in `backups/<db>/` forever. |
| B5 | S4 | An Admin can add a remote server whose data directory is `/etc`. |

### Observations (behaviour worth a product decision, not defects)

* **Account lockout is a denial-of-service lever.** Five wrong passwords for an e-mail lock that account's password sign-in for 15 minutes (per-account limiter, in memory, cleared by a restart); anyone who knows the owner's e-mail can do it (S1.7, S1.7b). The same applies to a second step (S2.6).
* **`sslip.io`/`nip.io` hosts are HTTP-only by design** (shared-suffix certificate limits). The dashboard domain is the exception: on `sslip.io` it gets HTTPS and a real Let's Encrypt certificate (S6.3, S6.13). A preview domain on `sslip.io` is refused (S7.17).
* **Members can read** a database's password, an app's variable values and a Git app's webhook secret on the pages they may open (by design: the dashboard shows them; the API never returns them).
* **A correctly signed push that names no repository deploys** (the app's own secret is the proof); pushes naming another repository, other branches, tags and deletions do nothing (S7.13).
* **`docker kill` of an app container leaves it down** (Docker never restarts a manually killed container). musdash reports it correctly: UI "Not running", API `exited`, proxy 502 page, notification sent (S17.4).
* **Upgrading with `install.sh` restarts the proxy**, which refuses connections for about a second (8 of 192 probes at 0.1 s intervals). The README promises uninterrupted serving only for the control-plane restart (S0.7c).
* **Server Check** shows only "Docker 29.8.2 on linux/amd64" on a healthy machine; the README says it also reports Compose, git and memory (S12.6c).
* **Install proxy on a machine without systemd** fails with a clear `can't create '/etc/systemd/system/musdash-proxy.service'` message (S12.10).
* A 70 KB request header is accepted (Go's 1 MB default), and an unsupported method gets 403 before 405 (S16.11b, S1.12).

### What held up (highlights)

* **Auth and access:** setup closes after the owner exists; CSRF double-submit on all 187 routes' POSTs; per-route role table enforced (member/admin/owner matrix, 34 checks); role raise ends sessions, tokens and reset links; last owner protected; two-step TOTP with replay protection, lockout and single-use recovery codes; `Secure` cookies over HTTPS; strict CSP, no external requests on any page.
* **Secrets:** no plaintext of passwords, API tokens, env secrets, route passwords, webhook secrets, S3 keys or deploy keys in SQLite/WAL, logs, process lists, `docker inspect` or deployment logs; env files are 0600.
* **Deploys and proxy:** zero failed requests during redeploys, server restarts and `kill -9` of the control plane; failed deploys keep the old container; rollback re-runs the kept image with no pull; path routes match on segment boundaries; path-canonicalisation variants of a guarded `/admin` never reach the app; WebSocket upgrade works; 300 MB upload flat at 14.6 MB RSS.
* **Injection:** hostile repo URLs, branches, base paths, image references, domains, app names, Docker options, bind sources, Compose files (24 of 24 dangerous ones refused with a message naming the line) and notification/storage endpoints (loopback, link-local, metadata, encoded IPs, container IPs, redirects) were all refused or inert. A hostile `uname` answer from a remote server was displayed escaped and never executed.
* **Operations:** all 8 database engines start; PostgreSQL, MariaDB, MySQL and MongoDB back up, restore after a drop and delete cleanly; Redis downloads; KeyDB/Dragonfly/ClickHouse say "not backed up yet"; schedule, retention (disk and bucket), S3 copy through MinIO, scheduled tasks, 7 notification channel types, 6 catalogue services (WordPress, Ghost, n8n, MinIO, Uptime Kuma, Cloudflare Tunnel), previews for pull requests (create, rebuild, close, fork refusal, limit of ten), build on another server with `docker save | load`, remote deploys, per-server variables.
* **Resilience:** `kill -9` mid-deploy and mid-backup recover; `systemctl restart docker` brought 13/13 containers back; a full **reboot** restored every app, database (except Redis, F1) and service in 119 s with SQLite, sessions and tokens intact.

### Not covered (and why)

| Area | Reason |
|---|---|
| GitHub App end-to-end (create, install, push/PR events from GitHub, PR comments) | needs a GitHub account; the flow was checked only up to the redirect, webhooks were tested with synthetic signed payloads |
| Docker clean-up job, disk-nearly-full notification (S11.9, S11.10) | the clean-up runs once a day after 03:00 UTC and cannot be triggered; the disk event was not subscribed when the disk filled |
| Certificates for an app domain that is not `sslip.io`/`nip.io` | no domain was available; the ACME path is proven on the dashboard domain |
| E-mail delivery; Discord/Slack/Telegram/Pushover real delivery | channels were created and their Test failed cleanly with fake credentials; a webhook channel delivered for real |
| Build timeout and out-of-memory behaviour (S7.6) | would need a 30-minute build on a 2 GB box shared with another session |
| Invitation (7 days) and reset-link (1 hour) expiry | time-gated; single-use and cancellation were tested |
| Cloudflare Tunnel with a real token | the template starts and restart-loops with a fake token, as expected |
| Server install of the proxy on a second machine | the second server was a container without systemd |
| Another team's data (IDOR across teams) | one install has one team; cross-kind IDs were tested instead |

### Numbers

| Measure | Value |
|---|---|
| Idle RSS | server 21.4 MB, proxy 14.4 MB (targets 30/20) |
| RSS after the whole run | server 21.8 MB, proxy 13.1 MB |
| Proxy during a 300 MB POST | 14.0 → 14.6 MB |
| App redeploy (nginx) | 5–10 s, 0 dropped requests at 20 req/s |
| Control plane `kill -9` | back in 8 s |
| Host reboot | all services up in 119 s |
| Git Dockerfile build (busybox) | 9 s · Railpack (Node) 64 s · Nixpacks (Node) 189 s |
| Database start incl. image pull | MariaDB 25 s, MySQL 48 s, MongoDB 32 s, ClickHouse 26 s, KeyDB 12 s, Dragonfly 42 s |
| Let's Encrypt certificate (dashboard domain) | 5 s |
| API rate limit | 120 calls/min/token enforced (429 + Retry-After) |

### State the VPS was left in

musdash `adffb82` (clean) running, owner `owner@musdash.test`, all `t-*` test data still present (apps, databases, services, a MinIO bucket, notification channels, members). The VPS now has a 2 GB swap file (`/swapfile`, in fstab), `{"dns":["8.8.8.8","1.1.1.1"]}` in `/etc/docker/daemon.json`, the dashboard domain `dash.168.235.65.204.sslip.io`, and the other session's `t2-*` data, which was not touched. Two ~630-byte `t-pg-*` dumps may remain in the other session's `t2-backups` bucket (my first S3 run used the wrong storage row). The password used for the root login is kept only in a scratchpad file outside the repository.
