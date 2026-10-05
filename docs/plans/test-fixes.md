# Fixes from the VPS test of 2026-10-05: implementation plan

**Goal:** fix every defect the full-feature test found (`docs/testing/vps-test-report.md`, F1–F9, and its appendix, B2–B5), and settle the observations that are cheap to settle.

**How it is built:** one fix (or two or three small ones that belong together) at a time: plan here, plan reviewed, implemented with its tests, code reviewed, `go vet ./... && go test -short ./...` green, committed. On the branch `worktree-test-fixes` in a worktree of its own, because the main checkout holds another session's uncommitted work.

**Constraints:** the earlier ones hold. No new module, no migration unless a fix cannot do without one, idle memory does not move.

| Fix | Findings | Where |
|---|---|---|
| 1 | F1 Redis and KeyDB cannot restart in place | `internal/catalog` |
| 2 | F2 a terminal's shell outlives its page | `internal/servers`, `internal/web` |
| 3 | B2 a container left by a crash in mid-deployment; B4 a backup's temporary file left by a crash; F5 a network left by a deleted project | `internal/deploy`, `internal/backup`, `internal/ops` |
| 4 | F3 forwarding refused by a remote sshd; F4 which host key; F6 builds without DNS; B5 a data directory of `/etc`; what Check reports | `internal/servers`, `internal/runner`, README |
| 5 | F7 certificate orders that fail or hang say nothing | `internal/proxy` |
| 6 | B3 an external volume in short syntax | `internal/compose` |
| 7 | F8 control characters in names; F9 image references Docker refuses | `internal/web`, `internal/docker` |
| 8 | Observations: the upgrade restarts the proxy; 403 before 405 | `install/`, README, `internal/web` |

## Fix 1 — Redis and KeyDB restart in place (F1)

**What happens.** The two templates start through `sh -c`: the shell writes `requirepass …` to `/tmp/musdash.conf` (`umask 077`), gives the file to the engine's user, and runs the image's entry point. When Docker starts the *same* container again (its restart policy after a crash, `docker restart`, a daemon restart, a reboot) the file is still there, owned by `redis`. The shell runs as root, and on a kernel with `fs.protected_regular=2` (Ubuntu's default) nobody, root included, may open with `O_CREAT` a file in a sticky world-writable directory that belongs to somebody other than themselves or the directory's owner. The redirection fails, the container exits with status 1, and Docker tries again for ever. Starting from the dashboard works because that makes a new container.

Reproduced on local Docker with the sysctl set to 2: the old command ends with `sh: can't create /tmp/musdash.conf: Permission denied` after `docker restart`.

**The fix.** Remove the file before writing it: `umask 077 && rm -f /tmp/musdash.conf && printf … > /tmp/musdash.conf && chown …`. Unlinking is not what the sysctl guards, and root may remove another user's file from a sticky directory (`CAP_FOWNER`, which a database container keeps: musdash drops no capabilities there). The file is then created anew, by root, with mode 0600, exactly as on the first start. Tried on the same kernel setting: the container restarts twice, the file is `-rw------- redis`, the server runs as `redis`, and it still refuses a client without the password.

Rejected: `--requirepass` as an argument (the password would be in the process list, which `TestEveryEngineIsComplete` forbids); a file in the data volume (it would be in backups of the volume and outlive a password change); `mktemp` (one more file per restart, for ever in a container that crash-loops).

**Tests.**
- `internal/catalog`: a template whose command writes a file with `>` removes that file first. This is the rule, so a ninth engine written the same way is caught.
- `internal/deploy/database_test.go`: the expected command line of the Redis start.
- `TestEveryEngineStartsWithDocker` (real Docker, own switch): after an engine is up, `docker restart` its container and wait for its health command again. On a Linux host with the default sysctl this fails without the fix; on Docker Desktop (sysctl 0) it passes either way, which the test's comment says.

**After an upgrade.** A Redis or KeyDB container made before this fix keeps its old command. Restart from the dashboard once: that makes a new container, with the new command.

**Outcome.** Done. With the sysctl at 2 on local Docker, `TestEveryEngineStartsWithDocker` passes for both engines and fails for Redis when the `rm -f` is taken out again, as does the catalogue's rule.

**Still to check on the VPS:** a Redis and a KeyDB database after `systemctl restart docker` and after a reboot.

## Fix 2 — a terminal's shell ends with its page (F2)

**What happens.** A terminal is `docker exec --interactive --tty <container> sh -c …` on a terminal of the server's. When the page goes, musdash hangs up that terminal, which ends the `docker exec` *client*. Docker does not end the process the exec started: with `--tty` the daemon keeps the container's side of the terminal open, so the shell sees neither a hang-up nor an end of input, and stays until the container is replaced. Reproduced on local Docker with `alpine`, `nginx:alpine` and `debian`: after the client is hung up, `docker top` still lists `sh` (or `bash`). The same holds over SSH, where the remote sshd hangs up the client.

The phase 9 plan assumed "Docker ends an exec whose terminal went away". It does not.

**The fix.** musdash ends the shell itself, by a mark it gave it.

- Each terminal gets an id (`secret.RandomID()`), and its command carries it: `docker exec --interactive --tty --env TERM=xterm-256color --env MUSDASH_TERMINAL=<id> <container> sh -c <shellPick>`. The shell and everything it starts inherit the variable.
- When the terminal is over (the page left, the idle limit, the dashboard shutting down, or the shell ended by itself), after the terminal is closed, the handler runs one more fixed command in the same container, through the same Runner: `docker exec --env MUSDASH_HANGUP=<id> <container> sh -c <hangUp>`. Its context is a new one with ten seconds, not the request's and not `Closing`, which are both over by then.
- `hangUp` works in two steps, because the mark alone is not enough (found by the plan's review: after `su`, root without `CAP_SYS_PTRACE` cannot read the other user's `environ`, and `su` itself blocks SIGHUP, so the person's shell and its jobs stayed). First it finds the processes that carry the mark and notes the *session* each is in (`/proc/<pid>/stat`, which anybody may read). Then it sends SIGHUP to every process of those sessions. The shell of a terminal leads a session of its own (Docker gives an exec with `--tty` one), and only what it started can be in it, so nothing else of the container is touched; a session of 1 or less (the container's own) is never taken. The command that looks carries the id under another name, so it does not find itself. It needs `tr`, `grep` and `kill`, which are in BusyBox and in every image with a shell; where one is missing it exits with a status of its own and that is logged.
- Nothing a person typed is in either command: the id is ours (`[a-z2-7]`), the container's name comes from where it came before.
- It also runs when the shell ended by itself (`exit`). Then it finds nothing, or it finds what the person left running in the background, which is hung up like the rest: "It ends when you leave the page" holds whichever way the page was left. A program started with `nohup` ignores the signal and stays, as it would on any terminal.
- A failure (the container was replaced meanwhile, the server is gone) is logged at the level of a note and nothing more: there is then no shell to end.
- **Shutting down.** `Closing` now also ends the terminal itself (`end`), not only the connection: a handler stuck typing into a program that does not read would otherwise wait for the next ping. The process must not exit before the hang-ups ran: `Server.WaitTerminals(ctx)` waits until every place in the terminals' semaphore is free (it takes all eight, which it can only when nobody holds one, and gives back what it took). `main` starts it when shutting down begins, next to `srv.Shutdown` and `queue.Stop`, with fifteen seconds of its own, and joins it before it returns: the sum stays under the unit's `TimeoutStopSec=40`. systemd's signal to the control group ends the `docker exec` clients at that moment; the hang-up is started after it and is not signalled again. A `kill -9` of the dashboard still leaves the shells of the terminals open at that moment; they go with their container.

Rejected: reading the shell's process id from the terminal's own output (the stream would have to be parsed, and the first read can hold half of it); a file with the id in the container (a read-only root has nowhere to write); typing Ctrl-D before closing (a full-screen program reads it as input); `docker exec` without `--tty` and a terminal made inside the container (needs `script`, which most images lack).

**Tests.**
- `internal/web/terminal_test.go`: the command a terminal runs carries a mark; after the browser leaves, and after the shell ends by itself, the server is asked to hang up exactly that mark in that container, after the terminal was closed; `WaitTerminals` returns once the last terminal is gone and gives up when its context ends.
- A test against real Docker (`MUSDASH_DOCKER_TEST=1`), in a BusyBox image and in a Debian-based one: a container, a terminal opened through `LocalRunner` with the handler's own command, a background job started, and in the Debian one a shell of another user through `su` with a job of its own; the terminal is closed (that the shell is then still listed is Docker's behaviour and is only logged); after the hang-up command, run once directly and once through an SSH Runner against `runner/sshtest`, nothing of the terminal is left, and the container's own process still runs.

**Docs.** The phase 9 plan's row "When the browser leaves" is corrected; README keeps its sentence, which is now true; CLAUDE.md's paragraph on the terminal gets the rule.

**Still to check on the VPS:** S15.6c again (open and drop three terminals; `docker top` shows no extra shell), also on a remote server.

## Fix 3 — what a crash or a delete leaves behind (B2, B4, F5)

Three leftovers, each small, each found by looking at the server after something ended badly.

### B2: the container of a deployment that the dashboard died in

**What happens.** A deploy job may run once (`WithMaxAttempts(1)`), so when the process is killed in the middle of one, `jobs.Start` fails the job instead of requeueing it, and `FailStaleDeployments` marks the deployment failed with `finished_at = now`. The new container, started and waiting for its health check, is still there. The monitor's `Reconcile` at start is what removes containers no app points at, but `removeOrphans` spares every container whose deployment finished in the last five minutes (`ProtectedDeployments`: queued, running, *or finished after the cut-off*), and the deployment has just been finished by the recovery itself. Reconcile runs once per connection of the monitor, so nothing looks again: two containers for the app until the dashboard is restarted once more.

**The fix.** The grace is for a container that may still be draining: the one a *successful* deployment made, replaced a moment later by the next. A failed deployment's container is never serving and never wanted: `deploy` removes it itself when it fails in the ordinary way. So `ProtectedDeployments` protects queued and running deployments and those that *succeeded* after the cut-off; a failed one is not protected. After a crash the order at start is already right: the deployment is failed first (`FailStaleDeployments`, before `monitorServers` is started), then Reconcile finds its container unprotected and removes it.

Nothing else changes: a container that an app points at is `current` and is never an orphan, whatever its deployment's status (a process killed after the switch leaves the app pointing at the new container, which stays).

**Tests.** `TestReconcileRemovesOrphans` gains two containers: one of a deployment that failed a second ago (removed) and one of a deployment that succeeded a second ago (kept). A second test goes through the crash itself: a deployment left `running` with its container listed, `FailStaleDeployments`, `Reconcile`: the container is removed and the app's serving container is not.

### B4: the half-written file of a backup the dashboard died in

**What happens.** `Runner.WriteFile` writes to `.musdash-<random>` next to the destination and renames when everything is written. A process killed in the middle of a dump leaves that file in `<data>/backups/<database>/`: not listed, not counted by retention, as large as the dump had got.

**The fix.** Two places, because the directory is on the database's server and start-up recovery does not talk to servers.

- `ops.Recover` (before the queue starts, so nothing is being written yet) removes `<data>/backups/*/.musdash-*` on this machine, next to where it already removes storage key files of an upload that was cut short.
- Before a dump, the backup job removes what is left in the database's backup directory through the Runner, which covers a remote server: `find <dir> -maxdepth 1 -type f ( -name '.musdash-*' -o -name '.rclone-*' ) -mmin +60 -delete`. Backups, restores and starts of one database share a lock, so no dump of this database is being written; the hour is for the one thing that may write there at the same moment, a key file of a delete somebody asked for from the page, which lives for seconds. This also removes key files left on a remote server, which nothing did. `find` with these options is in GNU findutils and BusyBox; a failure is logged and the backup goes on.

**Tests.** `ops`: `Recover` removes a `.musdash-123` file from a database's backup directory and leaves a finished dump; a backup run issues the sweep before the dump, for the right directory, and still succeeds when the sweep fails. `backup`: the sweep command against a real directory with old and new files (old temp and key files go; a new temp file, a dump and a directory stay).

### F5: the network of an environment nothing runs in

**What happens.** Every start (`deploy`, a database's start, a service that joins its environment) makes sure the environment's network `musdash-<environment>` exists on the server. Nothing ever removes it. Deleting a project or an environment needs its apps, databases and services deleted first, so by then there is nothing to ask which servers had the network.

**The fix.**

- When an app, a database or a service is destroyed, after its record is gone: if no app, database or service of that environment is left on that server (`db.EnvironmentUsesServer`), the network is removed there (`docker network rm`, a missing network is not an error). Rows decide, not containers: a stopped app is a row, and so is an app that is being deployed for the first time, so a network is never taken from under a deployment. A failure is logged; the delete has succeeded.
- The daily clean-up of a server also removes managed networks named `musdash-<id>` whose environment no longer exists: what earlier versions left, and what a delete could not remove because the server did not answer. Docker refuses to remove a network something is attached to, which is the last guard.
- New in `docker.Client`: `RemoveNetwork` and `Networks` (managed ones, by label).

Not done: removing the network when the last *container* stops. A stopped app would lose it and the next start would make it again, for nothing.

**Tests.** `deploy`: destroying the only app of an environment removes the network; with a database left in the environment on the same server it does not; with a resource left only on *another* server it does; the same for a database and a service. `ops`: the clean-up removes the network of an environment that is gone and leaves one whose environment exists, and names that are not an environment's. `db`: `EnvironmentUsesServer`.

**Still to check on the VPS:** S17.1 of the appendix (kill -9 while a deploy waits on its health check, then `docker ps`), S9.9b, S4.6.

## Fix 4 — remote servers: forwarding, which host key, builds without DNS, the data directory (F3, F4, F6, B5)

### F3: an sshd that refuses to forward

**What happens.** The health check of a container on a remote server reaches its loopback port through the SSH connection (`Runner.Dial`, a `direct-tcpip` channel). An sshd with `AllowTcpForwarding no` (Alpine's default, and a common hardening) refuses the channel, so every deployment there fails after its whole health timeout with `ssh: rejected: administratively prohibited`, while Check says the server is ready. The README does not mention it.

**The fix.**
- Check opens one forwarded connection to `127.0.0.1:1` on the server. Nothing listens there, and the two refusals are different: `ssh.Prohibited` is sshd's own policy, `ssh.ConnectionFailed` is the connection that could not be made, which proves forwarding works. Only the first fails the check, as a needed item: "Forwarding: the server's sshd does not forward connections, which is how musdash checks that a new container answers. Set `AllowTcpForwarding yes` (or `local`) in /etc/ssh/sshd_config and restart sshd". Any other outcome (including a connection that succeeds) passes.
- A deployment that fails this way anyway (the setting changed after the check) says the same thing at once instead of waiting out the health timeout: `waitHealthy` treats a prohibited channel as final.
- README, remote-server steps: the requirement, in one sentence.

### F4: which host key was recorded

**What happens.** The SSH library asks for ECDSA host keys before RSA and Ed25519. A server with all three presents its ECDSA key, musdash records it, and both the page and the README tell the person to compare the fingerprint with `ssh_host_ed25519_key.pub`, which does not match.

**The fix.**
- First contact prefers Ed25519, then ECDSA, then RSA (`HostKeyAlgorithms`), which is what OpenSSH's own client does and what the instructions assume.
- Once a key is recorded, the connection asks for exactly that key's kind. This is needed by the first point (a server recorded with its ECDSA key before this change must keep presenting it, not be refused for presenting its Ed25519 key now) and is right by itself. If the server no longer has a key of that kind, the handshake finds no common algorithm; that is reported as a changed host key, with the same advice.
- The page names the kind with the fingerprint (`ED25519 SHA256:…`) and the notice after a first check names the matching file (`/etc/ssh/ssh_host_<kind>_key.pub`). README says the same.

### F6: builds that cannot look up names

**What happens.** On the test VPS (Ubuntu 24.04, Docker 29 with the containerd image store, systemd-resolved) `RUN` steps of a build had no DNS while containers did. Every build that downloads something failed with the tool's own words (`dns error: failed to lookup address information`, `Temporary failure in name resolution`, `Could not resolve host`), which say nothing about Docker. The cure is a `dns` entry in `/etc/docker/daemon.json`.

**The fix.** musdash cannot see this before a build without running one (it needs a base image and seconds on every check), so it says it when it happens: the build's output is watched, as it streams to the log, for the handful of phrases resolvers use; if the build then fails, the deployment's error and log end with a paragraph that says what to try. Nothing is buffered: the watcher keeps the last few dozen bytes to match across writes. README gets a short "Builds cannot reach the network" entry. `install.sh` is left alone: it cannot build either without pulling an image.

### B5: a data directory that is a system directory

**What happens.** A remote server's data directory is validated only for its shape, so `/etc` is accepted and musdash would make `apps/`, `backups/`, `proxy/` in it.

**The fix.** `servers.ValidDataDir`: a full, clean path of the allowed characters that is not a system directory, not inside one that is never ours (`/etc`, `/proc`, `/sys`, `/dev`, `/boot`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/usr`, `/run`, `/var/run`, `/tmp`, `/var/tmp`), and not one of the shared ones itself (`/var`, `/var/lib`, `/var/log`, `/home`, `/root`, `/opt`, `/srv`, `/mnt`, `/media`). The defaults (`/var/lib/musdash`, `/home/<user>/.musdash`) and the usual choices (`/srv/musdash`, `/opt/musdash`, `/data`) pass. The handler uses it; the message says a directory of musdash's own is wanted.

### Not a defect: what Check reports (S12.6c)

Check does report Compose, git, memory and the proxy's needs; they are shown in the answer to the Check itself and not stored, so a later visit shows only what is stored. Nothing changes.

**Tests.**
- `runner/sshtest` learns to refuse forwarding (`Silence`-style switch `Forwarding`). `servers`: Check against it reports the needed item and the server as a problem; with forwarding allowed the item is OK. `deploy`: a remote deployment whose health dial is prohibited fails at once with the advice, not after the timeout.
- `runner`: against a test server that has an Ed25519, an ECDSA and an RSA host key, first contact records the Ed25519 one; a configuration that has the ECDSA key recorded still connects and is presented that key; one whose recorded kind the server no longer has gets `ErrHostKeyChanged`. `servers`: `HostKeyKind`.
- `deploy`: a build whose output holds a resolver's phrase, split across writes, and fails ends with the advice; one that fails for another reason, or succeeds after such a line, does not.
- `servers`: `ValidDataDir`, a table. `web`: the handler refuses `/etc`.

**Still to check on the VPS:** S12.9f against the Alpine sshd container (Check now says so; with forwarding on, deploys pass), S12.6d (`ssh-keygen -lf` of the named file matches), a Railpack build with the `dns` entry removed (the advice appears).

## Fix 5 — a certificate that cannot be had says so, and does not hold the visitor (F7)

**What happens.** The proxy hands TLS to `autocert.Manager` and discards the server's error log (scanners make a line per failed handshake). So an order that Let's Encrypt refuses, or one that never completes, leaves nothing in the journal: the person sees a browser that waits. And it waits long: `GetCertificate` runs inside the handshake for as long as the order takes, up to autocert's own five minutes; the server's handshake deadline cannot interrupt it, because it is not reading or writing.

**The fix.** The proxy puts its own function in front of the manager's `GetCertificate` (`certs` in `internal/proxy/certs.go`):

- **It says what happened, for hosts that are ours.** When the manager returns an error for a host that has a TLS route (the same question `hostPolicy` asks), a warning is logged with the host, the error and how long it took. Refusals for names nothing is routed on stay silent, as now. One line per host a minute at most: autocert keeps a failed order for a minute before trying again, and every handshake in that minute gets the same error. The table of when each host was last logged is bounded (cleared when it reaches 256 hosts).
- **It says when a certificate was obtained.** A call that succeeds after more than a second was an order, not a read from memory or disk: one line with the host and the time.
- **It does not hold the connection.** The manager is called in a goroutine and waited for up to thirty seconds. After that the handshake is given up with an error and a warning is logged; the order goes on in the manager (it does not use the handshake's context) and, if it completes, the next visitor gets the certificate. A goroutine per handshake costs far less than the handshake's own arithmetic, and ends with the manager's call.

Rejected: a logger on `http.Server.ErrorLog` (it would bring back a line per scanner); replacing autocert's HTTP client to log requests (it shows requests, not outcomes).

**Tests.** `internal/proxy`, with a manager function scripted by the test and a log handler that collects: a failure for a routed TLS host is logged once however many handshakes fail within the minute, and again after it; a failure for an unrouted host is not logged; a slow success is logged, a fast one is not; a call that outlasts the limit returns an error at the limit and logs, and the goroutine's late answer is taken without blocking (no leak: the test waits for it to end).

**Still to check on the VPS:** S6.3c: an app domain whose order cannot complete shows a line in `journalctl -u musdash-proxy` and the handshake ends after 30 s.

## Fix 6 — a mount whose target is not a path (B3)

**What happens.** The report says an `external: true` volume in short syntax gets past `Validate`. The cause is elsewhere. Reproduced with Compose 5.5: in `- v:/x` the volume's name is one letter, and Compose reads a letter and a colon as a Windows drive, so the whole text becomes the *target* of an anonymous volume (`{"type":"volume","target":"v:/x"}`, no source) and the top-level volume `v`, now unused, is left out of the normalised document together with its `external`. Nothing external is attached; Docker then refuses the mount ("mount path must be absolute"), and the person gets Docker's words instead of ours. With a longer name (`vol:/x`) the volume is there and `external` is refused as it should be.

More generally `Validate` never looked at a mount's target, and Compose passes on a relative one (`target: relative/path`) for a volume, a bind or a tmpfs alike.

**The fix.** In `checker.mounts`, every mount's target must be a full path in the container (`docker.ValidMountPath`: absolute, no `..`, none of the characters a `--mount` option splits on). The message names the target; when it looks like a drive (`^[A-Za-z]:[\\/]`) it adds what happened: a volume with a one-letter name is read as a Windows drive, give it a longer name.

**Tests.** `internal/compose`: the normalised document of the report's file (as Compose 5.5 prints it) is refused with the hint; a relative target of each mount type is refused; ordinary targets pass. The sandboxed test against real Docker Compose (where there is one) loads the report's YAML and expects the refusal.

## Fix 7 — names with control characters, image references Docker refuses (F8, F9)

### F8

**What happens.** A project's name is checked for length only, so a NUL byte or a line break is stored. It is escaped wherever it is shown, but it is in notifications, in logs and in the API's JSON.

**The fix.** One rule for every text a person types that is only ever shown (`plainText` in `internal/web`): no control characters (Unicode `Cc`, which includes NUL, tab and line breaks) and none of the characters that reorder text (`U+202A`–`U+202E`, `U+2066`–`U+2069`), which can make a name read as another. It is applied to: a project's name and description; a person's name (setup, profile, accepting an invitation); the team's name; the names of a server, a source, a deploy key, a backup storage, a notification channel, a scheduled task, an API token and a tag where they are not already held to a pattern. Names that become part of an address or a command (apps, databases, services, environments) already have a pattern.

### F9

**What happens.** `docker.ValidImage` is one expression that lets a first component be upper-case because it may be a registry's host. So `NGINX:ALPINE` passes (Docker reads a name without a slash as a repository, which must be lower-case) and so does a port of `99999`. Both fail later, at the pull.

**The fix.** `ValidImage` reads a reference the way Docker does: digest off the end, tag after the last slash, and the first component is a registry only when there is a slash and it has a dot, a colon, is `localhost` or has an upper-case letter. A registry is a host name with an optional port from 1 to 65535; the rest is lower-case path components with Docker's separators (`.`, `_`, `__`, hyphens); the tag and digest as before. What it must never do stays true and stays tested: nothing that starts with `-` and nothing with a space or a shell character is a reference.

**Tests.** `docker`: a table of references Docker accepts and refuses (the existing cases, the two of the report, `a..b`, `host:0/x`, `HOST/x`, `localhost:5000/x`, a digest, a tag in upper case). `web`: a project name with a NUL, a line break and a right-to-left override is refused with a message; the other fields by a table over their forms.

## Fix 8 — the observations that are cheap to settle

- **Request headers.** The dashboard's server takes Go's default of 1 MB of headers; the proxy already limits its own to 64 KB. The dashboard gets the same `MaxHeaderBytes`.
- **The upgrade restarts the proxy.** `install.sh` replaces the one binary and restarts both services; the proxy is back within a second, and requests in that second are refused. The README's upgrade note says so, next to what it already promises for the control plane. Handing the listening sockets over would end the gap; it is not built here.
- Left as they are, on purpose: five wrong passwords lock an account's sign-in for fifteen minutes (the alternative lets a password be guessed without limit; `reset-password` on the server is the way in for a locked-out Owner, which the README says); a method that changes something is refused for its missing form token (403) before the router is asked whether the method exists (405); Members see the values of what they work with; a correctly signed push that names no repository deploys.
