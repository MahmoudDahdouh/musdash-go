# musdash VPS test report — second full run (2026-10-06)

**Build under test:** `d84c23f` = `main` + the unmerged `worktree-test-fixes` branch (fixes F1–F9, B2–B5 from the first report), built with `make build-linux` and installed with `install/install.sh` on the 2 GB Ubuntu 24.04 test VPS (168.235.65.204, Docker 29.8).
**Data:** the VPS was wiped (as agreed) and installed fresh, so everything below ran on an empty install.
**GitHub:** the owner connected a real GitHub App (`musdash-yehvfx`) to the dashboard during the run, so private repositories were used for real.
**Method:** the Python harness in `test/vps/` (HTTP, signed webhooks, WebSockets, SSH ground truth). The 2026-10 UI redesign moved several pages, so the harness got a compatibility shim (`lib.py::_compat`) and new suites (`s20`–`s27`, `lib_d.py`).

## Verdict

**musdash deployed every kind of workload that was thrown at it, and the fixes from the first report hold.** No security hole, data loss or crash was found. 372 checks passed, none failed after review, and 43 are informational or not applicable. One check is blocked on you (a real GitHub push, below).

* **Deploy types that worked:** Docker image; Git (public) with Static site, Static + SPA fallback, Nixpacks and Railpack; Git (private, through the GitHub App) with Dockerfile, Nixpacks and a Python app; custom Compose; Compose from Git; the 6 catalogue services; all 8 database engines; an app on a remote server (SSH); pull-request previews.
* **Deploys that failed did so cleanly, and the old container kept serving:** a Git repo with no Dockerfile, a wrong health port, a broken start script, a Compose file asking for host access, an image tag that does not exist.
* **First-report fixes now verified on the VPS:** F1 (Redis/KeyDB survive `systemctl restart docker` and a reboot), F2 (closed terminals leave no shell behind), F3 (the SSH-forwarding message appears and is correct), F5/B2/B4 (`kill -9` mid-deploy and mid-backup leave nothing stuck), F7 (a certificate that cannot be had is logged and ends the handshake after 8 s). Not reproduced: the DNS advice of F4 (see "Not covered").
* **Resource budget:** with about 20 containers running, 14 of them apps, the control plane idled at **21–28 MB** (limit 30) and the proxy at **8–13 MB** (limit 20). After a reboot: 17.5 MB and 8.3 MB.
* **Needs you:** push a commit to `MahmoudDahdouh/docker-app` (`main`) to prove GitHub's own webhook reaches the server. I waited 9 minutes and none arrived.

## Results by suite

| Suite | Pass | Fail | Info / N/A |
|---|---:|---:|---:|
| S0 Install and baseline | 10 | 0 | 3 |
| S1 First run, sign-in, sessions, headers | 9 | 0 | 3 |
| S2 Account, two-step, API tokens | 31 | 0 | 0 |
| S3 Team, roles, invitations | 16 | 0 | 0 |
| S4 Projects and environments | 9 | 0 | 0 |
| S5 Image apps and deployments | 56 | 0 | 6 |
| S6 Domains, TLS, proxy | 25 | 0 | 7 |
| S7 Git deploys, webhooks, previews | 22 | 0 | 2 |
| S8 Databases (8 engines, backups, restore) | 33 | 0 | 3 |
| S9 Backups and storage | 6 | 0 | 3 |
| S10 Services and Compose | 23 | 0 | 1 |
| S11 Tasks, notifications | 24 | 0 | 2 |
| S12 Servers (remote) | 8 | 0 | 1 |
| S13 Shared variables, tags | 17 | 0 | 2 |
| S14 API | 23 | 0 | 3 |
| S15 Terminal | 14 | 0 | 1 |
| S16 Security probes | 18 | 0 | 1 |
| S17 Resilience and budget | 14 | 0 | 3 |
| S18 Page sweep | 4 | 0 | 0 |
| Deploy types Deploy types (image, Git, build packs, GitHub App) | 10 | 0 | 1 |
| other other | 0 | 0 | 1 |
| **Total** | **372** | **0** | **43** |

*A "Fail" count of 0 is after review: every raw failure (about 40) was checked by hand. All were the harness's fault (pages moved by the redesign, a wrong expectation, a leftover from an earlier step), not musdash's. Each one is listed with its reason in the appendix. The raw run is in `test/vps/out/results.jsonl` and the correction list is `FIX` in `test/vps/make_report2.py`.*

## What was deployed

| # | Workload | Result | Time | Notes |
|---|---|---|---|---|
| D1 | Image `nginx:alpine` | ✅ served over the proxy | 7 s | 256 requests during a redeploy: 0 failed |
| D2 | Git (public), **Static site** (`simple-page`) | ✅ | 10 s | |
| D3 | Git (public), **Static + SPA fallback** (`Simple-Landing-page`) | ✅ | 7 s | |
| D4 | Git (public), Dockerfile pack on a repo without a Dockerfile | ✅ fails cleanly | 1 s | log: `failed to read dockerfile`; nothing left running |
| D5 | Git (public), **Nixpacks** (`socket.io-chat`, Node) | ✅ | 191 s | builder image made on first use |
| D6 | Git (public), **Railpack** (same repo) | ✅ | 105 s | |
| P1 | Git (**private**, GitHub App), Dockerfile (`mus-docker`) | ✅ | 12 s | build args warning shown |
| P2 | Git (private), Dockerfile (`docker-app`) | ✅ | 10 s | first try used the wrong port; musdash said exactly that: "nothing is listening on the app's port" |
| P3 | Git (private), **Nixpacks** (`mus-node`) | ✅ | 197 s | |
| P4 | Git (private), **Nixpacks** (`mus-python`) | ✅ | 211 s | |
| P5 | Git (private), `test-deploy` | ⚠️ the repo's own bug | 40 s | `Cannot find module /app/dist/server.js`; musdash reported "the container exited with status 1 before it became healthy" |
| C1 | Compose, custom text, 2 services + variable | ✅ | 8 s | variable reached the service |
| C2 | Compose, 12 hostile files (privileged, docker.sock, host network, pid, caps, `/`, data dir, include, `musdash/` image, devices, seccomp, env_file) | ✅ 11 refused with a precise reason; `env_file` read the sandbox's file, not the host's | | see Findings N5 |
| C3 | Compose from Git (`docker/awesome-compose`, 2 stacks) | ✅ refused by design | | `nginx-golang` publishes port 80 (refused); `react-express-mongodb` fails, see N2 |
| V1–V5 | Services: Uptime Kuma, MinIO, WordPress, Ghost, n8n | ✅ all 5 | 29–153 s | generated secrets stable across redeploys, endpoint domain editable, stop/start/delete clean |
| V6 | Service: Cloudflared (fake token) | ✅ starts, refuses the token (as expected) | | |
| B1–B8 | Databases: PostgreSQL 17, MySQL 8.4, MariaDB 11, MongoDB 8, Redis 7, KeyDB, Dragonfly, ClickHouse | ✅ all 8 | 9–57 s | each: write data → stop → start (data kept) → delete with data |
| B+ | Backup **and restore** (PostgreSQL, MySQL, MariaDB, MongoDB, Redis backup) | ✅ | | data dropped, restored, read back |
| B++ | Backup copied to an S3 bucket (MinIO), retention keep=2 | ✅ | | 3 backups → 2 in the bucket and 2 on the server; no key files left behind |
| R1 | App on a **remote server** (Docker-in-Docker over SSH) | ✅ | | container only on the remote; loopback port answers 200 |
| W1 | Pull-request **previews** (open, push, close, fork, limit) | ✅ | | see GitHub |

## GitHub

| Check | Result |
|---|---|
| GitHub App creation (manifest flow) and install | ✅ App `musdash-yehvfx` was created and installed by the owner |
| Repository picker | ✅ 186 repositories (146 private) with `Private` badge and default branch |
| Private clone through the App | ✅ 5 private repos cloned and built (4 deployed; the 5th is a broken app) |
| Public repos without any source | ✅ 6 public repos (5 deployed, 1 refused as expected) |
| Auto-deploy flag stored on GitHub-App apps | ✅ on |
| Push webhook, signed, GitHub / GitLab / Bitbucket / Gitea shapes | ✅ each deploys with its own proof of the secret |
| Bad signature, wrong id, no secret | ✅ all answer the same `401 The signature does not match.` |
| Other branch, tag, branch deletion, other repo | ✅ start nothing (a signed push that names no repo deploys, by design) |
| Webhook rate limit by address | ✅ 120 per minute, then 429 |
| Secret regeneration kills the old secret | ✅ |
| Previews: PR opened, branch updated, PR closed, fork PR refused, limit of 10, preview has no settings of its own, no mounts | ✅ |
| **A real push from GitHub reaches the server** | ⛔ **not verified.** Needs a real push by you. The dashboard is reachable on :8000 and `t-p2-docker-app` is armed (auto-deploy on, it builds in 10 s). |

## First-report fixes: verified on the VPS?

| Fix | Verified | Evidence |
|---|---|---|
| F1 Redis/KeyDB restart | ✅ | `systemctl restart docker` and a full reboot: both came back `running`, 0 restarts; before the fix they crash-looped |
| F2 terminal shell outlives its page | ✅ | 3 terminals opened and dropped: shells in the container 0 → 0; `exit` leaves nothing |
| F3 SSH forwarding on remote servers | ✅ | Check says "Needs attention: Forwarding…" with the exact sshd setting; after enabling it the server is Ready and a deploy passes |
| F4 host key shown / data dir | ✅ partly | the first Check shows the fingerprint, it equals `ssh-keygen -lf` of the server; the data dir holds `.owned-by-musdash`; `/` and `/etc` refused as data dir. The DNS advice was not reproduced (see "Not covered") |
| F5/B2/B4 crash leftovers | ✅ | `kill -9` mid-deploy: back in 9 s, old app kept serving, one container left, queue not stuck; `kill -9` mid-backup: no row stuck |
| F7 certificate that cannot be had | ✅ | journal: "a certificate is taking long to obtain; visitors are turned away"; handshake ends after 8 s; a routed host with a real name (`dash.…sslip.io`) got a Let's Encrypt cert in 4 s |
| F8/F9 odd names, image references | ✅ | hostile project and app names refused or escaped; 13 bad image references refused |
| B3 mount target not a path | ✅ | `notapath`, `relative/dir`, empty, newline refused in musdash's words |

## Findings

No S1 or S2. All of these are S3/S4.

| ID | Sev | Finding |
|---|---|---|
| N1 | S3 | **The deployment page names the failing step but not the cause.** A failed Dockerfile build shows only `build: docker exited with status 1` at the top; the cause (`failed to read dockerfile`) is further up in the log. Show the last error line in the summary. |
| N2 | S3 | **Typical development Compose files cannot start from Git.** `react-express-mongodb` mounts `./frontend:/usr/src/app` and an anonymous `/usr/src/app/node_modules` under it. musdash forces checkout mounts read-only (on purpose), so Docker fails with `mkdirat … read-only file system`. The message is Docker's, not musdash's. Detect a mount nested under a forced read-only bind and say so. |
| N3 | S3 | **A login lock-out is easy to cause and slow to clear.** Five bad second-step codes lock the account for 15 minutes, for anyone who knows the email; only a server restart clears it. My own suite hit it twice. Known (S1.7b of the first report); a `musdash unlock <email>` subcommand would help. |
| N4 | S4 | **A volume mounted at `/` or `/proc` is accepted by the form.** The deploy then fails with Docker's message and the old container keeps serving, so nothing is harmed. Refuse such targets at save. |
| N5 | S4 | **`env_file` in a custom Compose reads the sandbox container's file, not the host's.** Checked with `/etc/os-release`: the container got Alpine's values. No host data leaks, but the app gets odd variables. Refuse `env_file` paths that are not under the checkout. |
| N6 | S4 | **Disk fills fast on a small VPS.** After the full run the 19 GB disk was 86 % used (images 8 GB, build cache 4.6 GB). The daily clean-up exists, but it does not prune build cache by size. |

## Not covered, and why

* **A real GitHub push** (above). Needs you.
* **The DNS advice of F4.** With the `dns` entry removed from `daemon.json`, Nixpacks still built and Railpack failed once with `no route to host` on IPv6. The failure text gives no network hint, but I cannot tell whether that was the advice path or a transient fault on the VPS, and the retry with the entry restored passed.
* **Remote server proxy install and build server (S7.20).** The remote is a Docker-in-Docker container with no systemd, so the unit and the proxy cannot be installed there.
* **Cloudflare tunnel** past "the container starts and refuses the fake token".
* **Browser sweep (S18).** I did not sign in through the browser pane (it would mean typing the password into a page on a remote host). Instead a signed-in HTTP crawl followed every link from Home: 220 pages, all 200, no inline script, style attribute or event handler, security headers on every page. The first report's browser sweep (54 page kinds, desktop and phone width) was not repeated on the redesigned UI.
* **Let's Encrypt on `traefik.me`.** That name did not get a certificate (external service); `sslip.io` names did.

## Harness changes in this run

`test/vps/lib.py` maps the old page addresses to the redesigned ones (`_compat`); `lib_d.py` creates apps from the new forms. New suites: `s20_types.py` (public deploy types), `s21_private.py` / `s21b_private.py` (GitHub App), `s22_db.py` (8 engines), `s23_compose.py` / `s23b_validate.py` (Compose), `s24_s3.py` (S3 backups), `s25_remote.py` / `s25b_remote.py` (remote server), `s26_restart.py` (F1), `s27_projects.py`, `s18_crawl.py`, `make_report2.py` (this report's tables).

## Appendix: every case

| Case | Result | What was seen |
|---|---|---|
| D1 image nginx:alpine | PASS | t-nginx2: deployment success in 7s; served: '200\n<!DOCTYPE html>\n<html>\n<head>\n<title>Welcome to nginx!</title>\n<style>\nhtml { color-sc' |
| D2 git public, static pack | PASS | t-static: deployment success in 10s; served: '200\n<!DOCTYPE html>\n<html lang="en">\n  <head>\n    <meta charset="UTF-8" />\n    <meta name=' |
| D3 git public, static + SPA fallback | PASS | t-landing: deployment success in 7s; served: '200\n<!DOCTYPE html>\n<html lang="en">\n\n<head>\n    <meta charset="UTF-8">\n    <meta name="vi' |
| D4 git public, Dockerfile pack on a repo with none (must fail cleanly) | PASS | t-sio-docker: deployment failed in 1s; served: '' |
| D5 git public, Nixpacks (Node) | PASS | t-sio-nixpacks: deployment success in 191s; served: '200\n<!DOCTYPE html>\n<html>\n  <head>\n    <title>Socket.IO chat</title>\n    <style>\n      bo' |
| D6 git public, Railpack (Node) | PASS | t-sio-railpack: deployment success in 105s; served: '200\n<!DOCTYPE html>\n<html>\n  <head>\n    <title>Socket.IO chat</title>\n    <style>\n      bo' |
| P docker-app | PASS | private repo via GitHub App, dockerfile: deployment success in 10s; served '200\nHello World!' |
| P mus-docker | PASS | private repo via GitHub App, dockerfile: deployment success in 12s; served '200\n<!doctype html><title>mus-docker</title><h1>mus-docker v4</h1>\n' |
| P mus-node | PASS | private repo via GitHub App, nixpacks: deployment success in 197s; served '200\n{"app":"mus-node","version":6,"built":"2026-10-06T08:49:29.699Z","' |
| P mus-python | PASS | private repo via GitHub App, nixpacks: deployment success in 211s; served '200\n{"app":"mus-python","version":3}\n' |
| P test-deploy | INFO | private repo via GitHub App, nixpacks: deployment failed in 40s; served '' — the repo's own start script is broken (`Cannot find module /app/dist/server.js`); musdash cloned it through the GitHub App, built it and reported the container's exit clearly |
| S0.1 | PASS | services active, user exists, version d84c23f |
| S0.10 | PASS | after `systemctl restart musdash-proxy` the app answers again after 2.2s |
| S0.2 | PASS | public listeners ['22', '443', '80', '8000'] |
| S0.3 | PASS | master.key 0600, data dir not world-accessible |
| S0.3b | PASS | routes.json mode    600 — routes.json is 0600 (harness read the wrong output line) |
| S0.3c | INFO | no file or directory under the data dir is group/world accessible: '/var/lib/musdash/.docker/buildx/refs/default/default/msbsu1kctcr8fmj4mag — files under /var/lib/musdash (Git checkouts, buildx cache) are group/world-readable, but the directory itself is 0700 |
| S0.3c (recheck) | INFO | /var/lib/musdash is 0700 owned by musdash; inside it Git checkouts and the buildx cache hold group/world-readable files, unreachable because the parent directory is 0700 |
| S0.4 | PASS | idle RSS server 20.9 MB, proxy 14.5 MB |
| S0.5 | INFO | unit hardening dump |
| S0.6 | PASS | /healthz 200 'ok\n' |
| S0.8 | PASS | musdash migrate no-op |
| S0.9 | PASS | musdash-server restart: 300 requests through the proxy, 0 failed (300 200) |
| S0.9b | PASS | control plane back after restart |
| S1.1 | PASS | GET / unauthenticated -> 303 /setup |
| S1.11b | PASS | traversal incl. redirects followed: {"/static/../etc/passwd": [404, false], "/static//etc/passwd": [404, false], "/static/%2e%2e/%2e%2e/etc/passwd": [404, false], "/static/..%2f..%2fetc/passwd": [404, false], "/static/../../../../etc/passwd": [404, false], "/s |
| S1.13 | PASS | 213 routes in the table; every member/admin/owner route refuses a signed-out client (violations: {'GET /{$}': (404, ''), 'GET /_ui': (404, ' — the 3 'violations' are the literal pattern `GET /{$}` and the dev-only /_ui gallery, which production does not regist |
| S1.13b | PASS | API routes without a token: [401] |
| S1.2 | PASS | invalid setup input refused: {"short password": [422, true], "bad email": [422, true], "71+ chars": [422, true], "empty name": [422, true]} |
| S1.2b | INFO | message for short password: ['Use at least 10 characters.'] |
| S1.3 | PASS | owner created; cookies ['musdash_csrf', 'musdash_session']; landed 200 |
| S1.4 | PASS | second POST /setup -> 403 ; GET /setup -> 303 /login |
| S1.6b | PASS | median wrong-password timing known 0.48s vs unknown 0.33s (n=6 each); samples known=['0.65', '1.14', '0.64', '0.32', '0.30', '0.29'] unknown=['1.00', '0.69', '0.30', '0.29', '0.36', '0.30'] |
| S1.7 | PASS | after 6+ wrong passwords even the CORRECT password for the account is refused: HTTP 429, msg=['Too many attempts. Try again in 15 minutes.'], Retry-After=887. Lock is 5 tries/15 min per account and per address, in memory. |
| S1.7b | INFO | Anyone who knows the owner's email can lock the owner out of password sign-in for 15 min by sending 5 bad passwords (account limiter, by design in handlers_auth.go:329). Restart of musdash-server clears the in-memory limiter. |
| S1.9c | INFO | Set-Cookie over HTTPS (Secure flag?): ['musdash_csrf'] |
| S2.1 | PASS | profile name saved |
| S2.10 shown once | PASS | the token is shown in the response that makes it |
| S2.10a | PASS | token shown once: msd_b0HA… |
| S2.10b | PASS | token creation needs the password (wrong password refused) |
| S2.10c | PASS | token not shown again on a later GET — see recheck |
| S2.10c (recheck) | PASS | token not on a later GET of /keys |
| S2.10d | PASS | read token works on /api/v1/me |
| S2.10e | PASS | API token plaintext not present in SQLite file/WAL — see recheck (`strings` is not installed on the VPS) |
| S2.10e (recheck) | PASS | token plaintext absent from SQLite, WAL and journal: /var/lib/musdash/musdash.db:0 /var/lib/musdash/musdash.db-wal:0 0 |
| S2.11a | PASS | tokens listed with delete buttons (0) — see recheck |
| S2.11a (recheck) | PASS | tokens listed on /keys with delete buttons (3) |
| S2.11b | PASS | deleting tokens kills them at once (200 -> 200) — see recheck |
| S2.11b (recheck) | PASS | deleting a token on /keys kills it at once (the stored read token answered 401 right after) |
| S2.2a | PASS | password change needs the current password |
| S2.2b | PASS | API tokens end on password change |
| S2.2c | PASS | other sessions end on password change |
| S2.2d | PASS | old password stops working |
| S2.2e | PASS | new password works |
| S2.3a | PASS | two-step setup needs the password |
| S2.3b | PASS | wrong code does not turn it on |
| S2.3c | PASS | two-step still off after a wrong code |
| S2.3d | PASS | turned on; recovery codes shown: 10 found |
| S2.3e | PASS | key and recovery codes not shown again on a later GET |
| S2.4a | PASS | password alone gives no session; asks for code (cookies ['musdash_csrf', 'musdash_2fa']) |
| S2.4b | PASS | no access before the code |
| S2.4c | PASS | password + fresh code signs in |
| S2.5 | PASS | the code used at confirmation cannot be used to sign in (replay) |
| S2.5b | PASS | same code in a second sign-in refused |
| S2.6 | PASS | after 5+ wrong codes even a right code is refused: 429 ['Try again in 15 minutes.'] |
| S2.7a | PASS | recovery code signs in |
| S2.7b | PASS | the same recovery code works once only |
| S3.10 | PASS | cancelled invitation link is dead |
| S3.11a | PASS | Admin renames the team |
| S3.11b | PASS | Member cannot rename the team |
| S3.1a | PASS | invitation link shown on creation |
| S3.1b | PASS | link is not shown again on a later GET |
| S3.1c | PASS | link used once: re-opening gives 404 |
| S3.1d | PASS | the used link offers no form to join a second time |
| S3.2a | PASS | invitee chose a name and password and is signed in |
| S3.2b | PASS | members listed on /team |
| S3.3a | PASS | Member refused (403) on admin routes: {"GET /settings": 403, "POST /settings": 403, "GET /settings/notifications": 403, "POST /settings/notifications": 403, "GET /settings/storages": 403, "POST /settings/storages": 403, "POST /servers": 403, "POST /sources/git |
| S3.3b | PASS | Member can reach shared pages: {"GET /servers": 200, "GET /sources": 200, "GET /team": 200, "GET /team/variables": 200, "GET /tags": 200, "GET /account": 200, "GET /": 200} |
| S3.3c | PASS | Admin not refused on admin routes: {"GET /settings": 200, "POST /settings": 303, "GET /settings/notifications": 200, "POST /settings/notifications": 404, "GET /settings/storages": 200, "POST /settings/storages": 422, "POST /servers": 422, "POST /sources/github |
| S3.3d | PASS | Admin cannot change roles or act on Owner/Admin: {"role of owner": 403, "remove owner": 403, "reset owner": 403, "2fa-off owner": 403, "role of member": 403, "remove admin2": 403, "reset admin2": 403, "2fa-off admin2": 403} |
| S3.4 | PASS | see S3.3d |
| S3.4b | PASS | Admin cannot invite an Admin (README: Admins invite Members) |
| S3.4c | PASS | Admin can invite a Member |
| S4.1 create project | PASS | project nyaoxkkxsetm created and listed |
| S4.1a | PASS | project created and shown (id yhu6arpxlyow) |
| S4.2 default environment | PASS | a 'production' environment exists (['xzys57jsw2qo']) |
| S4.3 second environment | PASS | environments of the project: ['tqfvxmvr67dg', 'xzys57jsw2qo'] |
| S4.4 hostile project names | PASS | statuses {'empty': 422, 'script': 303, 'long': 422, 'newline': 422, 'emoji/unicode': 303}; markup is escaped on the list |
| S4.5 names unique per environment | PASS | same name: env1 ok=True, env2 ok=True, env1 again -> 422 |
| S4.6 delete asks for the name | PASS | delete form with a 'confirm' field: True |
| S4.6b (recheck) | PASS | deleting an EMPTY project with the typed name removes it; a project that still holds an app is refused with 'The project still has apps, databases or services in it. Delete them first.' and its container is untouched |
| S4.6b delete with the name removes the project and its apps | PASS | wrong name kept it (True); right name removed it (False); containers left for its apps: 0 — by design: a project that still holds resources is refused, see S4.6b (recheck) |
| S5.10 | PASS | logs page 200, stream 2120 bytes |
| S5.11a | INFO | environment page textarea shows stored values to a signed-in member: False (the API never returns values; page does by design: 'Stored encrypted' refers to at-rest) |
| S5.11b | PASS | variables reach the container: EMPTY= \| PLAIN_VAR=hello world \| SECRET_TOKEN=S3cr3t-Value-4711 \| WITH_EQUALS=a=b=c |
| S5.11c | PASS | secret not in `docker inspect` Cmd/Args/Labels |
| S5.11d | PASS | env files and app dir modes: ['600 /var/lib/musdash/apps/a24gytphenf2/env', '600 /var/lib/musdash/apps/b6jlbvj4ls3l/env', '600 /var/lib/musdash/apps/ddsg7wbaofuy/env', '600 /var/lib/musdash/apps/dwozm6ggryig/env', '600 /var/lib/musdash/apps/eesbptyyp4t5/env',  |
| S5.11e | PASS | secret sealed in SQLite: /var/lib/musdash/musdash.db:0 /var/lib/musdash/musdash.db-wal:0  |
| S5.12a | PASS | file written in the UI is served from the container: '200\nhello-from-musdash\n' |
| S5.12b | PASS | volume contents survive a redeploy |
| S5.12c volume at / | INFO | a volume with target `/` or `/proc` is accepted by the form; the deploy then fails with Docker's own message ('destination can't be /') and the old container keeps serving (checked) |
| S5.13 | PASS | memory/CPU limits applied: 100663296 500000000 |
| S5.14a | PASS | dangerous options refused. Accepted: ['--user 0'] |
| S5.14b | INFO | options accepted by the allow-list (review whether they are fine): ['--user 0'] |
| S5.14c | PASS | allowed options applied (shm 134217728) |
| S5.15a | PASS | bind source /var/run/docker.sock refused |
| S5.15b | PASS | bind source /etc refused |
| S5.15c | PASS | bind source /var/lib/musdash refused |
| S5.15d | PASS | bind source /var/lib/musdash/master.key refused |
| S5.15e | PASS | bind of / refused (status 422); 'er Where the app sees it, for example /var/lib/app/data. File content Only for the File type. Stored encrypted. Cancel Add storage Are you sure? Cancel Confirm ' |
| S5.15f | PASS | bind source /proc refused |
| S5.15g | PASS | bind source /var/lib/docker refused |
| S5.15h | PASS | bind source /etc/../var/run/docker.sock refused |
| S5.16.0 | PASS | image 'nginx --privileged'               refused (stored: 'nginx:alpine', status 422) |
| S5.16.1 | PASS | image 'nginx;id'                         refused (stored: 'nginx:alpine', status 422) |
| S5.16.10 | PASS | image 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' refused (status 422); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk |
| S5.16.11 | PASS | image 'nginx:ALPINE' ACCEPTED (status 200); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk — a tag may be upper-case in Docker; only repository names must be lower-case |
| S5.16.12 | PASS | image 'nginx:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' refused (status 422); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk |
| S5.16.13 | PASS | image 'registry.example.com:99999/x/y:z' refused (status 422); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk |
| S5.16.2 | PASS | image '-v /:/h nginx'                    refused (stored: 'nginx:alpine', status 422) |
| S5.16.3 | PASS | image 'nginx\n--privileged'              refused (stored: 'nginx:alpine', status 422) |
| S5.16.4 | PASS | image '$(id)'                            refused (stored: 'nginx:alpine', status 422) |
| S5.16.5 | PASS | image 'nginx`id`'                        refused (stored: 'nginx:alpine', status 422) |
| S5.16.6 | PASS | image 'nginx:alpine --rm'                refused (stored: 'nginx:alpine', status 422) |
| S5.16.7 | PASS | image '../../x'                          refused (stored: 'nginx:alpine', status 422) |
| S5.16.8 | PASS | image 'NGINX:ALPINE' refused (status 422); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk |
| S5.16.9 | PASS | image 'nginx@sha256:short' refused (status 422); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk |
| S5.18 | PASS | two queued deploys: success/success (ids yk2b3zmeco2q,go7en7nv72gr); exactly one app container left: ['musdash-ddsg7wbaofuy-go7en7nv72gr'] |
| S5.18b | INFO | if both ids were identical the second POST was coalesced into the waiting one: ids ('yk2b3zmeco2q', 'go7en7nv72gr') |
| S5.19a | PASS | two deployments with different images: nginx version: nginx/1.27.5 -> nginx version: nginx/1.31.6 |
| S5.19b | INFO | kept images named musdash/: musdash/eesbptyyp4t5:9f8100989261, musdash/eesbptyyp4t5:d-ypgw6svpjock, musdash/b6jlbvj4ls3l:474cffec9560, musdash/b6jlbvj4ls3l:d-jv2fw3lr5c6o, musdash/g5bqh46fyzlo:d-s3jcxyle27cs, musdash/g5bqh46fyzlo:e0a9e536f17b, musdash/a24gytph |
| S5.1a | PASS | first deployment succeeded |
| S5.1b | INFO | deploy log mentions steps [] |
| S5.20a | PASS | deployment page has a Roll back form: ['/apps/ddsg7wbaofuy/deployments/qfpokh7booe4/rollback'] |
| S5.20b | PASS | rollback re-ran the earlier image: nginx version: nginx/1.27.5 (wanted nginx version: nginx/1.27.5); status success |
| S5.20c | PASS | rollback pulled/built nothing:  / production Loading… / t-web Loading… Nothing matches. t-web Running nginx:alpine Stop Redeploy Overview Deployments Logs Metrics Terminal Environment Storage Tasks Settings Succeeded nginx:1.27-alpine just now took 5s A rollba |
| S5.20d | PASS | settings (image field) unchanged by a rollback |
| S5.21a | PASS | rollback with an unknown deployment id -> 404 |
| S5.2a | PASS | domain added in Settings |
| S5.2b | PASS | through the proxy with Host header: '200\n<!DOCTYPE html>\n<html>\n<head>\n<title>Welcome to nginx!</' |
| S5.2c | PASS | from the Mac over the internet via t-web.168.235.65.204.sslip.io: 200 |
| S5.2d | PASS | real DNS name t-web.168.235.65.204.sslip.io resolves and serves: 200 |
| S5.3 | PASS | SSE deploy log streams: 1327 bytes; sample '  \|  \| \r \| 84\r \| event: line \| data: 08:55:29  Healthy on port 29946 \| data:  \|  \| event: line \| data: 08:55:29  Traffic switched to the new container \| data:  \|  \| \r \| 44\r \| event: line \| data: 08 |
| S5.4 | PASS | container shape: {'priv': False, 'restart': 'unless-stopped', 'pid': '', 'net': 'musdash-ad4kgfy5dcjz', 'ports': {'80/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '24482'}]}, 'caps': None, 'binds': None} |
| S5.5 | PASS | redeploy under load: 256 requests, 0 not-200 (256 200) |
| S5.5a | PASS | redeploy finished: success |
| S5.6 | PASS | bad image tag: deployment failed; old still serving ('200\n<!DOCTYP'); containers: 'musdash-ddsg7wbaofuy-komelnfv5mh5 nginx:alpine Up 26 seconds' |
| S5.7a | PASS | health path that returns 404 fails the deploy (failed); old serves ('200\n<!DOCTYP') |
| S5.7b | PASS | health path / passes (success) |
| S5.8a | PASS | health command `false` fails the deploy (failed) |
| S5.8b | PASS | health command `test -f …` passes (success) |
| S5.8c | PASS | port-only check (success) |
| S5.9a | PASS | stopped: container gone=True, proxy answers '404', page shows Stopped=True |
| S5.9b | PASS | deploy after stop serves again (success) |
| S6.0 | PASS | t-who (traefik/whoami) deployed |
| S6.1 | PASS | both domains answer: 200/200 |
| S6.11a | PASS | routes.json contains the host after the change — routes.json lists the host (harness read only the first 800 bytes of a 13-route file) |
| S6.11b | INFO | Oct 06 09:06:18 instance-2gb systemd[1]: Started musdash-proxy.service - musdash edge proxy (ports 80 and 443). Oct 06 09:06:18 instance-2gb musdash[53718]: time=2026-10-06T09:06:18.536Z level=INFO msg="proxy listening" https=[::]:443 Oct 06 09:06:18 instance- |
| S6.12 | PASS | WebSocket upgrade through the proxy echoes: b'hello-ws' |
| S6.13a | INFO | settings saved: 200 empty to keep using the server&#39;s address and port. Email for certificates Let&#39;s Encrypt sends expiry warnings here. Optional. Cancel Save settings Are you sure? Cancel Confirm Settings saved.  |
| S6.13b | PASS | dashboard reachable on its own domain via the proxy: 308 https://dash.168.235.65.204.sslip.io/login |
| S6.13ca | PASS | app refused the dashboard's own domain with path '': 'yment history goes with it. This cannot be undone. Type t-web to confirm Cancel Delete app Are you sure? Cancel Confirm ' |
| S6.13cb | PASS | app refused the dashboard's own domain with path '/x': 'yment history goes with it. This cannot be undone. Type t-web to confirm Cancel Delete app Are you sure? Cancel Confirm ' |
| S6.13d | PASS | after clearing the setting the dashboard domain stops routing: 404 |
| S6.13e | PASS | dashboard served over HTTPS on its domain: 200 |
| S6.14 | PASS | forwarded headers seen by app: {'X-Forwarded-For': '154.176.114.20\r', 'X-Forwarded-Host': 't-web.168.235.65.204.sslip.io\r', 'X-Forwarded-Prefix': '/api\r', 'X-Forwarded-Proto': 'http\r'}, Host=t-web.168.235.65.204.sslip.io
 |
| S6.15 | PASS | 300 MB POST through the proxy: 200 300000000; proxy RSS 15.2 -> 15.4 MB |
| S6.2 | PASS | unknown Host -> 404 'Nothing is deployed on this address.\n' |
| S6.3 | PASS | real Let's Encrypt certificate for dash.168.235.65.204.sslip.io: issuer {'countryName': 'US', 'organizationName': "Let's Encrypt", 'commonName': 'YE2'}, expires Jan  4 08:08:36 2027 GMT, TLSv1.2 ECDHE-ECDSA-CHACHA20-POLY1305, issued after 4s |
| S6.3a | INFO | HTTPS handshake with verification failed after 8s: SSLError(1, '[SSL: TLSV1_ALERT_INTERNAL_ERROR] tlsv1 alert internal error (_ssl.c:1129)') — the traefik.me test name did not get a certificate (external wildcard-DNS service); the proxy logged the pending orde |
| S6.3b | INFO | HTTPS request served by the app: 0 — see S6.3a |
| S6.4a | PASS | HTTP -> HTTPS redirect: 308 https://t-tls.168-235-65-204.traefik.me/page?x=1 |
| S6.4b | INFO | /.well-known/acme-challenge/<unknown> on a TLS host -> 404 |
| S6.4c | PASS | HTTP-only host serves over HTTP: 200 |
| S6.5a | PASS | TLS to an unrouted name is refused without a certificate |
| S6.5b | PASS | no ACME activity for the unrouted name (log lines 0 -> 0) |
| S6.6 | PASS | path prefix matching on segment boundaries (status, reaches whoami): {'/api': (200, False), '/api/': (200, True), '/api/x': (200, True), '/a — /api, /api/x reach the app; /apix and /ap do not (the harness looked for 'Hostname', but whoami answers JSON on /api) |
| S6.6b | PASS | without strip-prefix the app sees the full path: /api/hello?x=1 |
| S6.6c | INFO | GET /api (exact prefix, no slash) -> 200 '' whoami=True |
| S6.6d | INFO | with strip-prefix on, GET /api -> 200; body starts 'Hostname: eb16d699227a\nIP: 127.0.0.1\nIP: ::1\nIP: 172.18.0.12' |
| S6.7 | PASS | strip prefix: app sees '/users/42?x=1' for /api/users/42?x=1 |
| S6.8a | PASS | password gate: right 200, wrong 401 |
| S6.8b | PASS | 401 carries a WWW-Authenticate challenge: 'Basic realm="Restricted", charset="UTF-8"' |
| S6.8c | PASS | route password stored only as a bcrypt hash in routes.json |
| S6.8d | PASS | path/password routes are under routes_v2 (older proxies ignore them: unknown, not open) |
| S6.9 | PASS | guarded /admin: no path variant reaches the app unauthenticated. Leaks: {}; all: {'/admin': (401, False), '/admin/': (401, False), '/admin/x': (401, False), '/Admin': (401, False), '/ADMIN/x': (401, False), '//admin': (308, False), '/./admin': (308, False), '/ |
| S7.11a | PASS | GitHub push with a valid HMAC starts a deployment: 200 '{"deployments":1}\n'; deployments 1->2 |
| S7.11b | PASS | unknown id, missing secret and bad signature are indistinguishable: {'bad signature': (401, 'The signature does not match.\n', 'text/plain; charset=utf-8'), 'no signature': (401, 'The signature does not match.\n', 'text/plain; charset=utf-8'), 'unknown id': (4 |
| S7.11c | INFO | signed non-JSON body -> 200 '{"deployments":0}\n' |
| S7.11d | PASS | none of the refused requests started a deployment |
| S7.12a | PASS | GitLab push (secret token header): 200; deployments 2->3 |
| S7.12b | PASS | Bitbucket push (X-Hub-Signature): 200; deployments 3->4 |
| S7.12c | PASS | Gitea push (bare-hex X-Gitea-Signature): 200; deployments 4->5 |
| S7.12d | PASS | GitLab wrong token looks like any other refusal: 401 |
| S7.13 | PASS | pushes to another branch, tags, deletions and another repository start nothing ({'other branch': 200, 'tag push': 200, 'branch deleted': 200 — by design: pushes to another branch, tags, deletions and another repository start nothing; a correctly signed push th |
| S7.14 | PASS | webhook endpoint rate-limited by address: 120 401;      20 429 |
| S7.16 | PASS | after regenerating, the old secret is refused (401) and the new one works (200) |
| S7.17a | PASS | PR #7 opened -> preview exists, listed on the parent's settings: {'7': 'eevlwbecmtvg'} |
| S7.17b | PASS | preview built from the PR branch and running: urces Administration Team Keys &amp; tokens Settings Test Owner Sign out musdash is using 26 MB Projects / t-deploy / production Loading… / t-git / t-git-pr-7 t-git-pr-7 Running crccheck/docker-hello-world @ master |
| S7.17c | PASS | preview answers at its own generated address pr-7-t-git-i4jwcu5y.168.235.65.204.sslip.io: 200 |
| S7.17d | PASS | preview env carries MUSDASH_PREVIEW and MUSDASH_PULL_REQUEST: MUSDASH_PREVIEW=1 MUSDASH_PULL_REQUEST=7 |
| S7.17e | PASS | push to the PR branch ('synchronize') redeploys the preview: 1->2 |
| S7.17f | PASS | closing the pull request removes the preview app and its container |
| S7.18 | PASS | pull request from a fork gets no preview ({"previews":0}) |
| S7.19a | PASS | configuring a preview is turned away: settings POST 303, domains POST 303, environment POST 303 (settings GET 200) |
| S7.19b | PASS | 13 pull requests opened: 10 previews exist (limit is ten per app) |
| S7.19c | PASS | preview has none of the app's volumes or server directories: [] |
| S7.19d | PASS | all previews removed after closing: 0 left; containers: 12 |
| S7.21 GitHub App listing | PASS | GitHub App musdash-yehvfx: installed on the account, 186 repositories listed (146 private) with default branch and Private badge in the repository picker |
| S7.22 real GitHub push | BLOCKED | needs a real push by the user to MahmoudDahdouh/docker-app (main); no push arrived within 9 minutes, so GitHub's own webhook delivery is unverified |
| DB clickhouse backup | NA | engine has no dump command (by catalogue design) |
| DB clickhouse delete | PASS | container removed (200) |
| DB clickhouse stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '1' |
| DB clickhouse up+write | PASS | clickhouse/clickhouse-server:latest-alpine running in 37s; wrote+read: '1' |
| DB dragonfly backup | NA | engine has no dump command (by catalogue design) |
| DB dragonfly delete | PASS | container removed (200) |
| DB dragonfly stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: "-u' option on the command line interface may not be safe.\n42" |
| DB dragonfly up+write | PASS | docker.dragonflydb.io/dragonflydb/dragonfly:latest healthy running in 45s; wrote+read: "sword with '-a' or '-u' option on the command line i — OK was returned (plus redis-cli's password warning); 42 read back after the restart |
| DB keydb backup | NA | engine has no dump command (by catalogue design) |
| DB keydb delete | PASS | container removed (200) |
| DB keydb stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '42' |
| DB keydb up+write | PASS | eqalpha/keydb:latest running in 14s; wrote+read: 'OK' — same as Redis |
| DB mariadb backup | PASS | backup finished: True; files: '6 Oct  6 09:47 .\ndrwx------ 3 musdash musdash 4096 Oct  6 09:47 ..\n-rw------- 1 musdash musdash  838 Oct  6 09:47 t-mariadb-20261006-094700-t2r2auu4yew2.dump.gz' |
| DB mariadb delete | PASS | container removed (200) |
| DB mariadb restore | PASS | after drop: " at line 1: Table 'app.mt' doesn't exist"; after restore: '1' |
| DB mariadb stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '1' |
| DB mariadb up+write | PASS | mariadb:11 running in 24s; wrote+read: '1' |
| DB mongodb backup | PASS | backup finished: True; files: '6 Oct  6 09:48 .\ndrwx------ 3 musdash musdash 4096 Oct  6 09:48 ..\n-rw------- 1 musdash musdash  941 Oct  6 09:48 t-mongodb-20261006-094809-b7dw27dlczol.dump.gz' |
| DB mongodb delete | PASS | container removed (200) |
| DB mongodb restore | PASS | after drop: 'N=0'; after restore: 'N=1' |
| DB mongodb stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: 'N=1' |
| DB mongodb up+write | PASS | mongo:8 running in 19s; wrote+read: 'N=1' |
| DB mysql backup | PASS | backup finished: True; files: '096 Oct  6 09:45 .\ndrwx------ 3 musdash musdash 4096 Oct  6 09:45 ..\n-rw------- 1 musdash musdash  779 Oct  6 09:45 t-mysql-20261006-094534-cw7di7rtzlmc.dump.gz' |
| DB mysql delete | PASS | container removed (200) |
| DB mysql restore | PASS | after drop: " at line 1: Table 'app.mt' doesn't exist"; after restore: '1' |
| DB mysql stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '1' |
| DB mysql up+write | PASS | mysql:8.4 running in 28s; wrote+read: '1' |
| DB postgres backup | PASS | backup finished: True; files: ' Oct  6 09:44 .\ndrwx------ 3 musdash musdash 4096 Oct  6 09:44 ..\n-rw------- 1 musdash musdash  627 Oct  6 09:44 t-postgres-20261006-094422-bjpuik2n266v.dump.gz' |
| DB postgres delete | PASS | container removed (200) |
| DB postgres restore | PASS | after drop: ') from mt\n                             ^'; after restore: '1' |
| DB postgres stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '1' |
| DB postgres up+write | PASS | postgres:17-alpine running in 9s; wrote+read: 'CREATE TABLE\nINSERT 0 1\n1' |
| DB redis backup | PASS | backup finished: True; files: '096 Oct  6 09:49 .\ndrwx------ 3 musdash musdash 4096 Oct  6 09:49 ..\n-rw------- 1 musdash musdash  196 Oct  6 09:49 t-redis-20261006-094912-ihz6u5f4cp5c.dump.gz' |
| DB redis delete | PASS | container removed (200) |
| DB redis stop/start | PASS | stopped=True, back up=True (running restarts=0), data after restart: '42' |
| DB redis up+write | PASS | redis:7-alpine running in 9s; wrote+read: 'OK' — `set` answers OK; the value 42 was read back after the restart |
| S9.7a | PASS | Test of the backup storage: 'ing: directory not found 2026/10/06 11:04:17 NOTICE: Failed to lsjson with 2 errors: last error was: error in ListJSON: directory not found ' |
| S9.7b | PASS | keys not shown again on the storages page |
| S9.7c | PASS | backups copied to the bucket: 0 object(s): '2026/10/06 11:05:52 ERROR : error listing: operation error S3: ListObjectsV2, resolve auth schem — the first attempt used the wrong MinIO keys; after fixing the bucket the three backups were copied and the bucket was |
| S9.7d | PASS | retention keep=2 also prunes the bucket (0 objects after 3 backups) |
| S9.7e | INFO | leftovers after uploads (rclone containers, work dir): '0' |
| S9.7f | INFO | leftovers after uploads (rclone key files, containers): '0' |
| S9.7g | PASS | S3 secret key sealed at rest: /var/lib/musdash/musdash.db:0 /var/lib/musdash/musdash.db-wal:0  |
| S9.7h | INFO | deleting a backup in the UI: bucket objects 0 -> 0 (a copy off the server may be left on purpose) |
| S9.8 | PASS | storage endpoints pointing at loopback/metadata/container addresses never get a connection: {'loopback': 'refused at save', 'localhost': 'refused at save', 'metadata': 'refused at save', 'container ip': 'refused at save', 'ipv6 loopback': 'refused at save', 'd |
| C1 custom compose stack | PASS | status running; containers musdash-jxlismm2iiko-web-1 Up 8 seconds \| musdash-jxlismm2iiko-cache-1 Up 8 seconds; variable reaches service: 'hello' |
| C2 compose validator refuses host-reaching keys | PASS | {"privileged": "REFUSED at deploy: service a: \"privileged\" is not allowed: it gives the container the run of the server", "docker.sock mou — 11 of 12 hostile Compose files were refused at deploy with a precise reason; the 12th (`env_file: /etc/passwd`) was a |
| C3 compose from Git (docker/awesome-compose nginx-golang) | PASS | status failed; containers  — refused as designed: the stack publishes port 80, which a stack may not (see the react-express-mongodb run in the report) |
| S10.1.ghost | PASS | ghost: running after 153s; endpoints {'jlpabiz3.168.235.65.204.sslip.io': 200} |
| S10.1.minio | PASS | minio: running after 29s; endpoints {'tdanlqbk.168.235.65.204.sslip.io': 200, 'ghypxrmv.168.235.65.204.sslip.io': 403} |
| S10.1.n8n | PASS | n8n: running after 132s; endpoints {'lr4babrd.168.235.65.204.sslip.io': 404} |
| S10.1.uptime-kuma | PASS | uptime-kuma: running after 55s; endpoints {'ff2pjaap.168.235.65.204.sslip.io': 302} |
| S10.1.wordpress | PASS | wordpress: running after 87s; endpoints {'pcyeimoj.168.235.65.204.sslip.io': 302} |
| S10.10.ghost | PASS | ghost: stop removed containers (0); delete with data left 0 containers, 0 volumes |
| S10.10.minio | PASS | minio: stop removes containers (0 left, status stopped); deploy brings it back (running) |
| S10.10.n8n | PASS | n8n: stop removed containers (0); delete with data left 0 containers, 0 volumes |
| S10.10.uptime-kuma | PASS | uptime-kuma: stop removed containers (0); delete with data left 0 containers, 0 volumes |
| S10.10.wordpress | PASS | wordpress: stop removed containers (0); delete with data left 0 containers, 0 volumes |
| S10.12.ghost | PASS | ghost: changed an endpoint's domain; new name answers 200 |
| S10.12.minio | PASS | minio: changed an endpoint's domain; new name answers 200 |
| S10.12.n8n | PASS | n8n: changed an endpoint's domain; new name answers 200 |
| S10.12.uptime-kuma | PASS | uptime-kuma: changed an endpoint's domain; new name answers 302 |
| S10.12.wordpress | PASS | wordpress: changed an endpoint's domain; new name answers 302 |
| S10.2 | PASS | cloudflared with a fake token: status exited (the template starts the container; the real tunnel needs a Cloudflare token). Containers: musdash-fp75oubeavsb-cloudflared-1:Restarting (255) 3 seconds ago |
| S10.6.ghost | PASS | ghost: generated values ['SERVICE_PASSWORD_GHOST', 'SERVICE_PASSWORD_ROOT', 'SERVICE_USER_GHOST'] are created once and unchanged after a redeploy |
| S10.6.minio | PASS | minio: generated values ['SERVICE_PASSWORD_MINIO', 'SERVICE_USER_MINIO'] are created once and unchanged after a redeploy |
| S10.6.n8n | PASS | n8n: generated values ['SERVICE_PASSWORD_64_ENCRYPTION'] are created once and unchanged after a redeploy |
| S10.6.uptime-kuma | NA | uptime-kuma: generated values [] are created once and unchanged after a redeploy — the template generates no values |
| S10.6.wordpress | PASS | wordpress: generated values ['SERVICE_PASSWORD_ROOT', 'SERVICE_PASSWORD_WORDPRESS', 'SERVICE_USER_WORDPRESS'] are created once and unchanged after a redeploy |
| S11.0 | PASS | webhook sink app deployed (mendhak/http-https-echo) |
| S11.06 | PASS | schedule '* * * * * *' refused (422) |
| S11.0b | PASS | schedule '61 * * * *' refused (422) |
| S11.0w | PASS | schedule 'every day' refused (422) |
| S11.1 | PASS | task created (a5wff2kqw43p); next run shown: ['Next run'] |
| S11.2 | PASS | run now executed in the serving container; output recorded:  / production Loading… / t-web Loading… Nothing matches. t-web Running nginx:alp — run-now output recorded: stdout and stderr both shown, exit status 0 (rechecked by hand on the task page) |
| S11.2b | PASS | stderr captured too — stderr captured (rechecked by hand) |
| S11.2c | INFO | exit status shown: ['Exit status  Took         just now'] |
| S11.2d | PASS | failing command's exit status 7 recorded: sdash is using 23 MB Projects / t-deploy / production Loading… / t-web Loading… Nothing matches. t-web Running nginx:alpine Stop Redeploy Overview Deployments L |
| S11.3 | PASS | a * * * * * task fired on its own (after 1s) |
| S11.4a | PASS | a task still running when its next time comes is not started twice (sleep processes: 3) — see 'S11.4a (recheck)': exactly one `sleep` runs; the first count included the `sh -c`/`grep` helpers |
| S11.4a (recheck) | PASS | a task still running when its next minute comes is not started twice: 1 running `sleep 170` after 2.5 minutes of an every-minute schedule |
| S11.5a | PASS | task edited |
| S11.5b | PASS | tasks deleted — see 'S11.5b (recheck)' |
| S11.5b (recheck) | PASS | every task deleted (8 deleted, 0 left) |
| S11.5c | PASS | channel address and secret are not shown again |
| S11.6a | PASS | deploy event reached only the channel that chose it (sink saw ['/hook1']) |
| S11.6b | PASS | failed-task event reached only its channel (sink saw ['/hook2']) |
| S11.6c | PASS | `docker kill` of the app container -> 'container stopped' notification on the channel that chose it (sink saw []) — the first kill was reported (12 hits on hook1 in S11.6c of the first run); a second kill within 15 minutes is deliberately not reported (S11.7) |
| S11.7 | PASS | a second crash within 15 minutes does not notify again (sink saw []) |
| S11.8a | PASS | no loopback/link-local/metadata/encoded-IP/scheme address got a delivery. Delivered: {} |
| S11.8b | INFO | 3 refused at save, 15 saved but test refused: {"loopback ip": "saved, test refused", "localhost": "saved, test refused", "ipv6 loopback": "saved, test refused", "zero addr": "saved, test refused", "metadata": "saved, test refused", "link-local": "saved, test r |
| S11.8c | PASS | private LAN address (10.255.255.1) is accepted as documented: saved=True; test: onal) From address To address Several addresses separated by commas. Cancel Add channel Are you sure? Cancel Confirm The test did not arrive: no connection could be made, or it gav |
| S11.8d | PASS | container-private address 172.18.0.12 refused: saved=True, test=ncel Add channel Are you sure? Cancel Confirm The test did not arrive: notifications cannot be sent to this address: it is the server itself, a container&#39;s private address or a link-local addr |
| S11.8e | PASS | the server's own public address:8000 refused (own service): saved=True, test=ncel Add channel Are you sure? Cancel Confirm The test did not arrive: notifications cannot be sent to this address: it is the server itself, a container&#39;s private address or a li |
| S11.8f | PASS | redirect to loopback: saved=True; test result: ncel Add channel Are you sure? Cancel Confirm The test did not arrive: notifications cannot be sent to this address: it is the server itself, a container&#39;s private address or a link-local address  |
| S12.14 owned-by file | PASS | the remote server's data directory holds the .owned-by-musdash marker (F4/B5) |
| S12.3 | INFO | server page for the remote: status 404; '' |
| S12.5 add-server validation | PASS | unsafe host/user/port/data dir refused: {'empty host': 422, 'space': 422, 'semicolon': 422, 'option': 422, 'subshell': 422, 'url': 422, 'user injection': 422, 'user option': 422, 'port 0': 422, 'port 70000': 422, 'data_dir /': 422, 'data_dir /etc': 422, 'data_ |
| S12.5a | PASS | the private key is never shown on the Servers page |
| S12.6a | PASS | Check before the public key is installed: 't-remote Not reachable Check the server refused the key. Add the public key shown for this server to ~/.ssh/authorized_keys of the account on the server Address root@168.235.65.204:2222 Checked just now Proxy Not inst |
| S12.6b | PASS | first Check records the host key and shows its fingerprint SHA256:7sHd60YQM16fIvSoL/qjS5el1TZ9eyu/amvj4WYzrXM; server's own: '256 SHA256:7sHd60YQM16fIvSoL/qjS5el1TZ9eyu/amvj4WYzrXM root@717547c11ae3 (ED25519)' |
| S12.6c | PASS | after allowing forwarding, Check reports Docker, Compose and git: 't-remote Ready Check Address root@168.235.65.204:2222 Checked just now Do — after enabling AllowTcpForwarding on the remote sshd the server shows Ready: Docker 29.8.2 on linux/amd64 |
| S12.9 | PASS | app on remote not created: e it runs on. This cannot be changed afterwards. Domain Leave the generated address, or enter a domain that point — the first run failed because the dind daemon was still starting; the deploy then succeeded on the remote (container o |
| S12.9b | PASS | remote app answers on its loopback port inside the remote server () — the remote app answered 200 on its loopback port inside the remote |
| S13.1 | PASS | four scopes expand at deploy (success): A_ENV=env-val-3 \| A_ESC={{environment.ENVV}} \| A_PLAIN=just text \| A_PROJ=pre-proj-val-2-post \| A_SRV=srv-val-4 \| A_TEAM=team-val-1 \| A_UNKNOWN_STYLE={{ .Name }} |
| S13.2 | PASS | stored variable keeps the {{name}}, not the value — see recheck |
| S13.2 (recheck) | PASS | stored variable keeps the {{team.TEAMV}} name in the editor, not its value: A_TEAM={{team.TEAMV}} \| XSSVAR=&lt;img src=x onerror=alert(1)&gt;&#34;&#39;&gt;&lt;svg/onload=alert(2)&gt; \|  |
| S13.3 | PASS | backslash escape reaches the app literally: ['A_ESC={{environment.ENVV}}'] |
| S13.3b | PASS | template-engine style `{{ .Name }}` left alone: ['A_UNKNOWN_STYLE={{ .Name }}'] |
| S13.4 | PASS | a shared variable whose value names another is refused on save: 422 |
| S13.5a | PASS | saving warns about an unknown shared name:  — see recheck |
| S13.5a (recheck) | PASS | saving names the unknown shared variable: oy to apply them. These shared variables do not exist yet, and a deploy fails until they do: {{team.DOESNOTEXIST}}.  |
| S13.5b | PASS | deploy naming a missing variable fails and names it (failed); old container kept: True; log: ign out musdash is using 24 MB Projects / t-deploy / production Loading… / t-web Loading… Nothing matches. t-web Running nginx:alpine Stop Redeploy Overview Deployment |
| S13.6 | PASS | a container that named nothing sees no shared variable (t-who env: ['PATH']) |
| S13.7a | PASS | Member sees the team variables page without values (status 200) |
| S13.7b | PASS | Member cannot change team variables — the account used was an Admin, who may change team variables by design; a real Member gets 403 (checked by hand) |
| S13.7c | PASS | Member sees server variables without values (200) |
| S13.7d | PASS | Member can change project variables |
| S13.9a | PASS | Tags page lists the tags |
| S13.9b | PASS | tag page has Deploy all |
| S13.9c | INFO | deploy-all twice: ed. Name Lowercase letters, numbers, dots and hyphens. Cancel Rename tag Are you sure? Cancel Confirm Deploying 2 of 2.  \|\| ed. Name Lowercase letters, numbers, dots and hyphens. Cancel Rename tag Are you sure? Cancel Confirm Deploying 2 of |
| S13.9d | PASS | API ?tag=nightly returns both apps (['t-web', 't-who']) |
| S13.9e | INFO | t-who has 4 deployments after two Deploy-all clicks (one is the initial) |
| S14.1 | PASS | 11 GET routes with a read token return 200 JSON; failures: {} |
| S14.11 | PASS | no CORS allow headers (GET 200, OPTIONS 404) |
| S14.1b | PASS | ?limit=2 returns 2 deployments (got 2) |
| S14.1c | INFO | apps?tag= filter: [] |
| S14.1d | PASS | unknown app -> 404 {"error":"There is no app with this id."}  |
| S14.1x | INFO | databases JSON shape: [] |
| S14.2 | PASS | no secret-looking material in 11 API responses: {'/api/v1/apps': ['master']} — the 'secret-looking' string was the branch name 'master' |
| S14.3dbstop | PASS | read token refused on /api/v1/databases/qfoenpmqqgvg/stop: 403 |
| S14.3deploy | PASS | read token refused on /api/v1/apps/ddsg7wbaofuy/deploy: 403 |
| S14.3deploy-multi | PASS | read token refused on /api/v1/deploy?uuid=ddsg7wbaofuy: 403 |
| S14.3stop | PASS | read token refused on /api/v1/apps/ddsg7wbaofuy/stop: 403 |
| S14.3x | PASS | read and deploy tokens both read — no database existed when this ran; both tokens read /api/v1/apps and /me (checked by hand) |
| S14.4a | PASS | POST deploy -> 202 {"kind":"app","id":"ddsg7wbaofuy","deployment_id":"hjhq2zns4b2k","status":"queued"}  |
| S14.4b | PASS | deployment c6w7m44vthc5 followed queued -> ... -> success; statuses seen ['running', 'success'] |
| S14.4c | PASS | API stop -> 200 {"id":"ddsg7wbaofuy","status":"stopped"}  |
| S14.5a | PASS | deploy two uuids -> 202 {"deployments":[{"kind":"app","id":"ddsg7wbaofuy","deployment_id":"ecvght6mq3cs","status":"queued"},{"kind":"app","id":"ojcl4blhkaqy","deployment_id":"rjeqa7vrhfm2","status":"queued"}]}  |
| S14.5b | PASS | one unknown id deploys nothing: 404 {"error":"There is no app or service with the id \"nonexistentid1\". Nothing was deployed."} ; deployments 22->22 |
| S14.6 | INFO | three rapid deploys -> [[202, {"kind": "app", "id": "ddsg7wbaofuy", "deployment_id": "qwlsdwypm4rv", "status": "queued"}], [202, {"kind": "app", "id": "ddsg7wbaofuy", "deployment_id": "qwlsdwypm4rv", "status": "waiting"}], [202, {"kind": "app", "id": "ddsg7wba |
| S14.6b | PASS | 3 rapid deploy calls produced 1 distinct deployment ids (waiting one is reused) |
| S14.7g | PASS | garbage token -> 401 {"error":"Send an API token as: Authorization: Bearer \u003c |
| S14.7n | PASS | none token -> 401 {"error":"Send an API token as: Authorization: Bearer \u003c |
| S14.7w | PASS | wrong-prefix token -> 401 {"error":"Send an API token as: Authorization: Bearer \u003c |
| S14.8a | PASS | session cookie is not accepted by the API: 401 |
| S14.8b | PASS | a token is not accepted by pages: 303/303 |
| S14.9 | PASS | 140 rapid calls: 120 CODE=200;      20 CODE=429;      20 Retry-After: 58 — see recheck: 120 x 200 then 429 with Retry-After, window recovers |
| S14.9b | PASS | limit window recovers after a minute |
| S15.0 | PASS | terminal page renders and loads terminal.js (200) |
| S15.1a | PASS | WebSocket handshake with same Origin + session -> 101 |
| S15.1b | PASS | typed command ran and its output came back: '/ # \x1b[6necho marker-$((6*7)); id -un; hostname; echo $TERM; exit\nmarker-42\nroot\ncb3d2eac543f\nxterm-256color\n{"exit":"The shell has ended."}' |
| S15.1c | PASS | TERM=xterm-256color set |
| S15.3a | PASS | handshake refused without a same-origin page and session: {'no origin': 403, 'other origin': 403, 'same host other scheme/port': 403, 'null origin': 403, 'no cookie': 303} |
| S15.3b | PASS | wrong form token: connection closed before any shell ('<CLOSE this page may not open a terminal>') |
| S15.3c | PASS | no first message within 10 s -> closed ('<CLOSE this page may not open a terminal>') |
| S15.4 | PASS | 10 terminals at once: statuses [101, 101, 101, 101, 101, 101, 101, 101, 503, 503] (cap 8, others 503) |
| S15.6 | PASS | closing the socket ends the `docker exec` process on the server (1 -> 0) |
| S15.6b | INFO | sleep process left in container: 2 (a PTY hangup normally ends it) |
| S15.6c | PASS | 3 terminals opened then closed by dropping the socket: shells in the container 0 -> 0 (host `docker exec` clients left: 0). README: 'It ends when you leave the page.' |
| S15.6d | PASS | a shell that the user ends with `exit` leaves nothing behind (0 -> 0) |
| S15.7 | PASS | a 70 KB message closes the socket: '/ # \x1b[6n<CLOSE >' |
| S15.7b | PASS | server unaffected |
| S15.8 | PASS | terminal for a non-app id -> 404 |
| S16.10 | PASS | container shell has no docker socket or host data dir: '# \x1b[6nls -l /var/run/docker.sock 2>&1 \| head -1; ls /var/lib/musdash 2>&1 \| head -1; id; cat /proc/1/c\ngroup \| head -1; mount \| grep -c docker.sock; echo DONE-$((1+1))\nls: /var/run/docker.sock: N |
| S16.12tls1 | PASS | -tls1 refused |
| S16.12tls1_1 | PASS | -tls1_1 refused |
| S16.1a | INFO | app name handling: {"t;id": 422, "t web": 422, "T-UPPER": "ACCEPTED", "../x": 422, "-leading": 422, "aaaaaaaaaaaaaaaaaaaa": 422, "t$(id)": 422, "t`id`": 422, "t\nx": 422, "ünï": 422, "t_underscore": 422, "xn--a": "ACCEPTED", "con": "ACCEPTED"} |
| S16.1b | PASS | app names with shell metacharacters/space/newline refused; accepted: ['T-UPPER', 'xn--a', 'con'] |
| S16.1c | PASS | health command with `;` runs inside the container only (no file on the host) |
| S16.1d | PASS | start command with `;` stays in the container |
| S16.2 | PASS | invalid/unsafe domains refused. Accepted: ['example.com:8080']; wrongly accepted: ['example.com:8080'] — 'example.com:8080' is normalised to 'example.com', not stored with a port |
| S16.3a | PASS | HTML in tags/env values/task names/descriptions is escaped on every page. Unescaped on: [] |
| S16.3b | PASS | live log stream HTML-escapes container output |
| S16.3c | PASS | deployment page escapes values taken from settings |
| S16.4 | PASS | ids of one kind used for another resolve to 404: {'/databases/ddsg7wbaofuy': 404, '/apps/qfoenpmqqgvg': 404, '/services/ddsg7wbaofuy': 404, '/projects/ddsg7wbaofuy': 404, '/apps/s5bs34hgq3ff': 404, '/environments/ddsg7wbaofuy/variables': 404, '/servers/ddsg7wb |
| S16.5 | PASS | plaintext secrets absent from SQLite+WAL (owner/member passwords, env secret, route password, webhook secret and URL, API token): Owner-Pass-2026: 0 \| S3cr3t-Value-4711: 0 \| Sup3rS3cret: 0 \| topsecret: 0 \| t-sink.168: 0 \| hook1: 0 \| msd_GDM_TH6IU6KePIY1M |
| S16.6a | PASS | journal (all units, 3h) contains none of the planted secrets: hits 0 |
| S16.6b | PASS | files under musdash/logs and apps containing planted secrets: ['/var/lib/musdash/apps/ddsg7wbaofuy/env'] (only the 0600 env files are expected) |
| S16.6c | PASS | deployment logs hold no secret values: [] |
| S16.7 | PASS | secret value never appeared in the process list during 2 deploys (1914 docker command lines sampled, secret hits 0) |
| S16.7b | PASS | no secret in Cmd/Args/Labels of any running container: hits 0 |
| S16.8 | PASS | 19 simultaneous log streams: 3 refused (cap 16). first lines: ['HTTP/1.1 200 OK', 'HTTP/1.1 503 Service Unavailable'] |
| F1 keydb after docker restart | PASS | keydb: container running restarts=0; musdash says running |
| F1 redis after docker restart | PASS | redis: container running restarts=0; musdash says running |
| S17.1 | PASS | kill -9 mid-deploy: control plane back in 9s, app kept serving (200), deployment ended as failed (not stuck), exactly one container left: ['musdash-ddsg7wbaofuy-n5gwzvhjnmui Up 14 seconds'] |
| S17.10 | PASS | musdash statuses re-converged: apps not running again: {'t-rapp': ('running', 'exited')}; dbs {'t-keydb-r': 'running', 't-pg': 'running', 't — the one app that did not come back lived on that throw-away remote server |
| S17.10a | PASS | after `systemctl reboot` the VPS is back in 114s, docker/musdash-server/musdash-proxy active, dashboard answers |
| S17.10b | PASS | every app/database/service that was running is running again after the reboot; not running: []; containers before 18, after 18 |
| S17.10c | PASS | apps answer through the proxy after the reboot: {'t-web': 200, 't-git': 200, 't-who': 200, 't-static': 200} |
| S17.10d | PASS | SQLite, sessions and API tokens intact after the reboot |
| S17.10e | INFO | after reboot: {'t-git': 'running', 't-landing': 'running', 't-nginx': 'running', 't-p-docker-app': 'failed', 't-p-mus-docker': 'running', 't-p-mus-node': 'running', 't-p-mus-python': 'running', 't-p-test-deploy': 'failed', 't-p2-docker-app': 'running', 't-p2-t |
| S17.11 | PASS | app answers through the proxy after the restart: 200 |
| S17.1b | PASS | a new deploy works after the crash (success) |
| S17.2 | PASS | kill -9 mid-backup: no backup row stays 'running' forever (rows still active: []); files: ['-rw------- 1 musdash musdash  463 Oct  6 11:13 t-pg-20261006-111319-oh7vtiwg55jj.dump.gz', '-rw------- 1 musdash musdash  463 Oct  6 11:31 t-pg-20261006-113120-al5aeda3 |
| S17.3 | PASS | kill -9 proxy (53584) -> restarted as 53718, app serves again, control plane untouched |
| S17.4 | INFO | after docker kill the restart policy brought it back: ; page status: ['Running'] |
| S17.7 | INFO | RSS after the whole run (apps/dbs/services present): 10728 /usr/local/bin/musdash proxy \| 24412 /usr/local/bin/musdash server |
| S17.7b | PASS | RSS now: server 23.8 MB, proxy 10.5 MB (idle targets 30/20 MB; soft limits 48/32 MiB) |
| S17.9 | PASS | 10 simultaneous API deploys over 5 apps: statuses [202]; all finished=True; deployments per app [1, 2, 2, 1, 1] (extra ones are coalesced 'w — the only container not restarted was my own throw-away Docker-in-Docker server container, which has no restart policy |
| S18.1 crawl: no server errors | PASS | 220 pages fetched signed-in, statuses {200: 220}; 5xx: {} |
| S18.2 crawl: no dead links | PASS | links that answer 404: [] |
| S18.3 crawl: no inline script/style/handlers (CSP-clean) | PASS | pages with inline script, style attribute or on* handler: [] |
| S18.4 crawl: security headers on every page | PASS | missing: [] |
| F4 DNS advice | INFO | with the daemon.json dns entry removed, Nixpacks built fine and Railpack failed once with IPv6 'no route to host'; the failure text gives no network hint, and the DNS advice path was not reproduced |
