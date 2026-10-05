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
- `hangUp` does what the kernel does when a terminal goes away, in two steps, because the mark alone is not enough (found by the plan's review: after `su`, root without `CAP_SYS_PTRACE` cannot read the other user's `environ`, and `su` itself blocks SIGHUP, so the person's shell and its jobs stayed). First it finds the terminal's *session*: the session of a process that carries the mark and still has a terminal (`/proc/<pid>/stat`, which anybody may read). Then it sends SIGHUP to every process of that session. The shell of a terminal leads a session of its own (Docker gives an exec with `--tty` one), and only what it started can be in it, so nothing else of the container is touched, a second terminal in the same container included; a session of 1 or less (the container's own) is never taken. The command that looks carries the id under another name, so it does not find itself. It needs `tr`, `grep` and `kill`, which are in BusyBox and in every image with a shell; where one is missing it exits with a status of its own and that is logged.
- Nothing a person typed is in either command: the id is ours (`[a-z2-7]`), the container's name comes from where it came before.
- What left the terminal is left alone, as on any terminal (narrowed after the code's review, which found that the first version also ended a daemon a person had started by hand): a program started with `setsid` or `nohup`, and a job in the background of a shell that the person ended with `exit` (the kernel takes the terminal from a session when its leader goes, so there is nothing to find; the command still runs then, and does nothing). "It ends when you leave the page" is about leaving the page.
- A failure (the container was replaced meanwhile, the server is gone) is logged at the level of a note and nothing more: there is then no shell to end.
- **Shutting down.** `Closing` now also ends the terminal itself (`end`), not only the connection: a handler stuck typing into a program that does not read would otherwise wait for the next ping. The process must not exit before the hang-ups ran: `Server.WaitTerminals(ctx)` waits until every place in the terminals' semaphore is free (it takes all eight, which it can only when nobody holds one, and gives back what it took). `main` starts it when shutting down begins, next to `srv.Shutdown` and `queue.Stop`, with fifteen seconds of its own, and joins it before it returns: the sum stays under the unit's `TimeoutStopSec=40`. systemd's signal to the control group ends the `docker exec` clients at that moment; the hang-up is started after it and is not signalled again. A `kill -9` of the dashboard still leaves the shells of the terminals open at that moment; they go with their container. Closing a WebSocket no longer waits behind a write that the browser is not taking (up to the write timeout of thirty seconds, found by the code's review): closing ends that write, and the goodbye frame has two seconds.

Rejected: reading the shell's process id from the terminal's own output (the stream would have to be parsed, and the first read can hold half of it); a file with the id in the container (a read-only root has nowhere to write); typing Ctrl-D before closing (a full-screen program reads it as input); `docker exec` without `--tty` and a terminal made inside the container (needs `script`, which most images lack).

**Tests.**
- `internal/web/terminal_test.go`: the command a terminal runs carries a mark; after the browser leaves, and after the shell ends by itself, the server is asked to hang up exactly that mark in that container, after the terminal was closed; `WaitTerminals` returns once the last terminal is gone and gives up when its context ends.
- A test against real Docker (`MUSDASH_DOCKER_TEST=1`), in a BusyBox image and in a Debian-based one, with the commands run once directly and once through an SSH Runner against `runner/sshtest`: three terminals opened in one container with the handler's own command. The first runs a background job, a program started with `setsid`, and a shell of another user through `su` with a job of its own and a program in front; the second a job; in the third the shell is ended with `exit`, leaving a job. The first is closed (that its shell is then still listed is Docker's behaviour and is only logged) and hung up: nothing of it is left but the `setsid` program, and the second terminal, the third's job and the container's own process still run. Then the second is closed and hung up.

**Docs.** The phase 9 plan's row "When the browser leaves" is corrected; README keeps its sentence, which is now true; CLAUDE.md's paragraph on the terminal gets the rule.

**Still to check on the VPS:** S15.6c again (open and drop three terminals; `docker top` shows no extra shell), also on a remote server.

## Fix 3 — what a crash or a delete leaves behind (B2, B4, F5)

Three leftovers, each small, each found by looking at the server after something ended badly.

### B2: the container of a deployment that the dashboard died in

**What happens.** A deploy job may run once (`WithMaxAttempts(1)`), so when the process is killed in the middle of one, `jobs.Start` fails the job instead of requeueing it, and `FailStaleDeployments` marks the deployment failed with `finished_at = now`. The new container, started and waiting for its health check, is still there. The monitor's `Reconcile` at start is what removes containers no app points at, but `removeOrphans` spares every container whose deployment finished in the last five minutes (`ProtectedDeployments`: queued, running, *or finished after the cut-off*), and the deployment has just been finished by the recovery itself. Reconcile runs once per connection of the monitor, so nothing looks again: two containers for the app until the dashboard is restarted once more.

**The fix.** A failed deployment's container is never serving and never wanted: `deploy` removes it itself when it fails in the ordinary way (before the status is set, so a second removal is harmless). So `ProtectedDeployments` protects queued and running deployments and those that *succeeded* after the cut-off; a failed one is not protected. After a crash the order at start is already right: the deployment is failed first (`FailStaleDeployments`, before `monitorServers` is started), then Reconcile finds its container unprotected and removes it.

What the grace is really for is said correctly while here (the plan's review traced it): a container carries the id of the deployment that *made* it, so the grace spares the container that a deployment made minutes ago and the next deployment has already replaced, which may still be draining. It never covered more than deployments in quick succession, and it still covers those.

Nothing else changes: a container that an app points at is `current` and is never an orphan, whatever its deployment's status (a process killed after the switch leaves the app pointing at the new container, which stays). `ProtectedDeployments` has this one caller.

**Tests.** `TestReconcileRemovesOrphans` gains two containers: one of a deployment that failed a second ago (removed) and one of a deployment that succeeded a second ago (kept). A second test goes through the crash as it happens: a deploy job left `running` with its one attempt used, its deployment `running`, its container listed; then what start-up does (`jobs.Start` on a new queue, `FailStaleDeployments`, `Reconcile`): the job and the deployment are failed, the container is removed, the app's serving container is not.

### B4: the half-written file of a backup the dashboard died in

**What happens.** `Runner.WriteFile` writes to `.musdash-<random>` next to the destination and renames when everything is written. A process killed in the middle of a dump leaves that file in `<data>/backups/<database>/`: not listed, not counted by retention, as large as the dump had got.

On a remote server it is worse, and not only for backups (found by the plan's review). `SSHRunner.WriteFile` is one remote command: `cat > tmp && chmod && mv tmp dest`. When the connection goes (the dashboard was killed, the network dropped), sshd closes the command's input; `cat` sees the end of its input, exits with 0, and the half-written file is *moved into place*: a truncated dump under a backup's name that no record names, or a truncated env file or `routes.json`.

**The fix.**

- **`SSHRunner.WriteFile` commits in a second command.** The first only writes the temporary file (`umask 077; cat > tmp`). When it has ended well *and* the reader this side has been read to its end without an error, a second command gives the file its mode and its name. A connection that goes away between the two leaves a temporary file and never a destination. A failure of either removes the temporary file, with a few seconds of its own. One more command per file written over SSH, on the connection that is already open.
- **`ops.Recover`** (before the queue starts, so nothing is being written yet) removes `<data>/backups/*/.musdash-*` on this machine, next to where it already removes storage key files of an upload that was cut short.
- **On a remote server** the same is done where the key files are already removed: `servers.sweep`, which runs on the first connection of this process to a server, before anything of this process is there. Its `find` takes `.musdash-*` as well as `.rclone-*` under `backups/*/`.

Not done: a sweep before each dump. With the two above, a leftover is gone at the next start of the dashboard, which is when one can exist.

**Tests.** `runner`: over the test SSH server, a write whose connection is dropped half-way leaves no file under the destination's name (it did), and a write whose reader fails leaves neither the destination nor a temporary file; a complete write still arrives with its mode, replaces what was there, and leaves no temporary file. `ops`: `Recover` removes a `.musdash-123` file from a database's backup directory and leaves a finished dump. `servers`: the sweep removes a `.musdash-*` and a `.rclone-*` file under `backups/<id>/` and leaves a dump.

### F5: the network of an environment nothing runs in

**What happens.** Every start (`deploy`, a database's start, a service that joins its environment) makes sure the environment's network `musdash-<environment>` exists on the server. Nothing ever removes it. Deleting a project or an environment needs its apps, databases and services deleted first, so by then there is nothing to ask which servers had the network.

**The fix.**

- When an app, a database or a service is destroyed, after its record is gone: if no app (previews count: they are apps), database or service of that environment is left on that server (`db.EnvironmentUsesServer`), the network is removed there. Rows decide, not containers: a stopped app is a row, and so is an app being deployed for the first time, so a network is not taken from under a deployment. (A resource created in the instant between the question and the removal would lose the network before its container starts and fail that one deployment; the window is one command.) A failure is logged; the delete has succeeded.
- The daily clean-up of a server also removes networks that carry musdash's label, are named exactly like an environment's (`^musdash-[a-z][a-z2-7]{11}$`: what the server lists is the server's word), whose environment is not in the database (`ErrNotFound`, nothing else), and that no container at all is attached to, running or not (`docker ps --all --quiet --filter network=<name>` is empty). The last matters: Docker removes a network that only stopped containers are attached to, and those containers can then never be started again; on a Docker daemon shared with another installation, its idle networks look exactly like leftovers.
- New in `docker.Client`: `Networks` (by label), `RemoveNetwork` (a network that is not there is not an error: Docker says "not found" for networks, which `ignoreMissing` does not know) and `NetworkInUse`.

Not done: removing the network when the last *container* stops. A stopped app would lose it and the next start would make it again, for nothing.

**Tests.** `deploy`: destroying the only app of an environment removes the network; with a database left in the environment on the same server it does not; with a resource left only on *another* server it does; the same for a database and a service; a network that is already gone is not an error. `ops`: the clean-up removes the network of an environment that is gone, and leaves one whose environment exists, one a stopped container is attached to, and names that are not an environment's. `db`: `EnvironmentUsesServer`, with a preview.

**Still to check on the VPS:** S17.1 of the appendix (kill -9 while a deploy waits on its health check, then `docker ps`), S9.9b (also for a database on a remote server), S4.6.

## Fix 4 — remote servers: forwarding, which host key, builds without DNS, the data directory (F3, F4, F6, B5)

### F3: an sshd that refuses to forward

**What happens.** The health check of an app on a remote server, by port or by path, reaches the container's loopback port through the SSH connection (`Runner.Dial`, a `direct-tcpip` channel). An sshd with `AllowTcpForwarding no` (Alpine's default, and a common hardening) refuses the channel, so every such deployment fails after its whole health timeout with `ssh: rejected: administratively prohibited`, while Check says the server is ready. The README does not mention it.

**The fix.**
- `SSHRunner.Dial` wraps a refusal by policy (`ssh.Prohibited`) in `runner.ErrForwardRefused`, so that packages without the SSH library can ask for it.
- Check opens a forwarded connection to `127.0.0.1:1`, with ten seconds of its own. Nothing listens there. A current sshd answers "connect failed", which proves forwarding works. "Prohibited" is its policy, but an sshd up to 7.4 (CentOS 7, Amazon Linux 2) says "prohibited" for *any* channel it could not open, a closed port included (found by the plan's review, in OpenSSH's `serverloop.c`). So on "prohibited" a second connection is tried, to the port sshd itself is reached on: one that opens proves forwarding too. Only when both are refused does the check say so, as a needed item: "Forwarding: the server's sshd does not forward connections, which is how musdash checks that a new container of an app answers on its port. Allow it for this account: `AllowTcpForwarding yes` (or `local`) and no `DisableForwarding` in /etc/ssh/sshd_config, no `PermitOpen` that leaves out 127.0.0.1, and no `restrict` or `no-port-forwarding` before the key in authorized_keys. Then restart sshd." Everything else passes.
- A deployment is not failed early on that answer: on an old sshd it is also what a container that does not listen *yet* looks like. But when the health check has run out of time and its last answer was a refusal by policy, the error says what Check says, instead of only the SSH library's words.
- README, remote-server steps: the requirement, in one sentence.

### F4: which host key was recorded

**What happens.** The SSH library asks for ECDSA host keys before RSA and Ed25519. A server with all three presents its ECDSA key, musdash records it, and both the page and the README tell the person to compare the fingerprint with `ssh_host_ed25519_key.pub`, which does not match.

**The fix.**
- First contact prefers Ed25519, then ECDSA (256, 384, 521), then RSA (`rsa-sha2-512`, `rsa-sha2-256`, and `ssh-rsa` last, which the library offered before too): what OpenSSH's own client does, and what the instructions assume. Host certificates are no longer asked for on a first contact; a server that has one also has the key it was made from.
- Once a key is recorded, the connection asks for that key's kind and no other. This is needed by the first point (a server recorded with its ECDSA key must keep presenting it, not be refused for presenting its Ed25519 key now) and is right by itself. The kind is not the key's type name: for a recorded `ssh-rsa` key the algorithms are `rsa-sha2-512`, `rsa-sha2-256` and `ssh-rsa` (asking for `ssh-rsa` alone would be asking for SHA-1, which a current sshd no longer offers, and every server recorded with an RSA key would be locked out; found by the plan's review); for an ECDSA or Ed25519 key, its own type; for a certificate, the certificate algorithms of its kind; for a type this code does not know, nothing is set and the library's own list applies. It is derived where the connection is made, from the recorded key, because further connections to a server are dialled with nothing else.
- When the server no longer has a key of the recorded kind, the handshake fails with the library's `AlgorithmNegotiationError` for the host key. That, and only that, with a key recorded, is reported as a changed host key, with the same advice; a mismatch of ciphers or key exchange keeps its own message.
- `servers.HostKeyKind` names the kind for people: `ED25519`, `ECDSA`, `RSA`, or nothing for anything else (a certificate's fingerprint is not what `ssh-keygen -lf` prints for the key). The page shows it with the fingerprint (`ED25519 SHA256:…`), and the notice after a first check names the matching file (`/etc/ssh/ssh_host_ed25519_key.pub`, `…_ecdsa_…`, `…_rsa_…`). README says the same.

### F6: builds that cannot look up names

**What happens.** On the test VPS (Ubuntu 24.04, Docker 29 with the containerd image store, systemd-resolved) `RUN` steps of a build had no DNS while containers did. Every build that downloads something failed with the tool's own words, which say nothing about Docker. The cure is a `dns` entry in `/etc/docker/daemon.json`.

**The fix.** musdash cannot see this before a build without running one, so it says it when it happens. The output of `docker build` (apps: `build.go`) and of `docker compose build` (services from a repository: `service.go`) is watched, as it streams to the log, for what resolvers print: `temporary failure in name resolution`, `temporary failure resolving`, `eai_again`, `could not resolve host`, `failed to lookup address information`, `bad address '` (compared in lower case). Nothing is kept but the last bytes needed to match across two writes. If the build then fails, one fixed sentence is added to the deployment's error, and so to its log and its notification: that a step could not look up a name; that the name may be mistyped, but if containers on that server can look names up and builds cannot, Docker's builds have no DNS there, and a `"dns"` entry in /etc/docker/daemon.json followed by a restart of Docker gives them one. It names the server the build ran on. Never the matched output itself.

Not watched: the clone (git's "Could not resolve host" is about the server's own DNS, where that advice would be wrong), the builders' own images and plans (their downloads are done by the Docker daemon, not by a `RUN` step), and what an app prints when it runs. Not matched: `no such host`, `name or service not known`, `ENOTFOUND`, which are what a mistyped name or the daemon's own lookups give.

README gets a short "Builds cannot reach the network" entry. `install.sh` is left alone.

### B5: a data directory that is a system directory

**What happens.** A remote server's data directory is validated only for its shape, so `/etc` is accepted and musdash would make `apps/`, `backups/`, `proxy/` in it. Worse, found by the plan's review: on the first connection of a process musdash empties `<data>/work` (leftovers of builds). With a directory that is somebody else's, such as `/data` or a home directory, a `work` directory that was already there would be emptied.

**The fix.**
- `servers.ValidDataDir`: a full, clean path of the allowed characters that is not inside a tree that is never ours (`/etc`, `/proc`, `/sys`, `/dev`, `/boot`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/usr`, `/run`, `/var/run`, `/tmp`, `/var/tmp`, `/var/lib/docker`, `/var/lib/containerd`), not one of the shared directories itself (`/var`, `/var/lib`, `/var/log`, `/home`, `/root`, `/opt`, `/srv`, `/mnt`, `/media`, and `/home/<name>`), and not an `.ssh` directory or inside one. The defaults (`/var/lib/musdash`, `/home/<user>/.musdash`) and the usual choices (`/srv/musdash`, `/opt/musdash`, `/data`, `/mnt/volume1/musdash`) pass. The handler uses it; the message says a directory of musdash's own is wanted.
- Check looks before it makes anything: a data directory that exists, has something in it, and has neither `apps` nor `proxy` in it is somebody else's. The check then fails with a needed item ("… already has other things in it. Choose a directory of musdash's own, such as …/musdash") and creates nothing. Check is the first contact, and nothing else talks to a server that has not passed its first contact, so the sweep cannot get there first. A server that is already in use has both directories and passes.

### Not a defect: what Check reports (S12.6c)

Check does report Compose, git, memory and the proxy's needs; they are shown in the answer to the Check itself and not stored, so a later visit shows only what is stored. Nothing changes.

**Tests.**
- `runner/sshtest` learns to refuse forwarding (a switch of its own, answering `Prohibited`, for everything or for everything but one port) and to have several host keys. `runner`: `Dial` returns `ErrForwardRefused` for a refusal by policy and not for a closed port. `servers`: Check against a server that refuses reports the item named Forwarding as needed and not OK; against one that answers like an old sshd (prohibited for the closed port, open for its own) the item is OK; with forwarding allowed it is OK. `deploy`: a health check that ran out of time with a refusal as its last answer says what to do; one that failed otherwise does not.
- `runner`: against a test server with an Ed25519, an ECDSA and an RSA host key, first contact records the Ed25519 one; configurations with the ECDSA key and with the RSA key recorded still connect and are presented that key; one whose recorded kind the server no longer has gets `ErrHostKeyChanged`. `servers`: `HostKeyKind`.
- `deploy`: a build whose output holds a resolver's phrase, split across writes and in another case, and fails: the error ends with the advice and names the server; one that fails for another reason, or succeeds after such a line, does not; the same for a service's build.
- `servers`: `ValidDataDir`, a table. Check against a directory with other things in it creates nothing and fails; against an empty one, a missing one and one that has `apps` and `proxy` it passes. `web`: the handler refuses `/etc`.

**Still to check on the VPS:** S12.9f against the Alpine sshd container (Check now says so; with forwarding on, deploys pass), S12.6d (`ssh-keygen -lf` of the named file matches), a Railpack build with the `dns` entry removed (the advice appears).

## Fix 5 — a certificate that cannot be had says so, and does not hold the visitor (F7)

**What happens.** The proxy hands TLS to `autocert.Manager` and discards the server's error log (scanners make a line per failed handshake). So an order that Let's Encrypt refuses, or one that never completes, leaves nothing in the journal: the person sees a browser that waits. And it waits long: `GetCertificate` runs inside the handshake for as long as the order takes, up to autocert's own five minutes. The server's handshake deadline (ten seconds, from `ReadHeaderTimeout`) does not interrupt it, because it is not reading or writing; it only makes sure that whatever comes back after ten seconds can no longer be sent.

**The fix.** The proxy puts its own function in front of the manager's `GetCertificate` (`certs`, in `internal/proxy/certs.go`).

- **One flight per host and kind of key.** The first handshake for a host starts a goroutine that asks the manager, and waits for it; handshakes for the same host that arrive meanwhile wait for the same flight and then ask for themselves. So however many visitors come while an order is running, one goroutine is inside the manager, where it may sit for five minutes; the others wait on a channel and a timer. The manager keeps two certificates for a host, one for clients that can use an ECDSA key and one for the few that cannot, and orders them apart; the two are two flights, by the manager's own rule for telling the clients apart, or the order for the second would turn away every visitor the first is there for (found by the code review). Hosts are compared in the form the route table uses, so `ExAmPlE.com` is not another host.
- **Nobody waits longer than eight seconds**, which is inside the handshake's own deadline, so that the visitor gets a TLS error instead of a connection that closes without a word. The eight seconds are counted from when the connection arrived, as the server's ten are (the server's `ConnContext` notes the moment), not from when the client said which host it wants; a client that was slow to say so still gets a second, which is more than a certificate that is there needs. The order goes on in its goroutine (the manager does not use the handshake's context) and, if it completes, the next visitor gets the certificate.
- **The goroutine that asked says what came of it**, since only it has the real answer: the manager gives the order's error to the caller that made the order, and "missing certificate" to everybody else for the next minute. A failure for a host that has a TLS route is a warning with the host, the error and the time it took, once a minute a host at most: an error from before any order, such as a certificate directory the proxy cannot read, comes back for every handshake. "missing certificate" is never logged, and neither is a refusal for a name nothing is routed on. A success that took more than a second was an order: one line with the host and the time. A flight that is still running after eight seconds says once that visitors are being turned away meanwhile.
- **Renewals.** autocert renews on timers and discards the error; a certificate that could not be renewed is simply served until it expires. When the certificate handed out for a routed host has less than fourteen days left (renewal starts thirty days before), a warning says so, once a day a host at most.
- The handshakes Let's Encrypt itself makes for a TLS-ALPN challenge are passed straight through: the manager answers them without waiting for anything, and they must not wait behind the order that caused them.

The table of what was last said about which host holds routed hosts only and is bounded; the table of flights holds what is inside the manager now.

Rejected: a logger on `http.Server.ErrorLog` (it would bring back a line per scanner); replacing autocert's HTTP client to log requests (it shows requests, not outcomes).

**Tests.** `internal/proxy`, with a manager function scripted by the test and a log handler that collects, in a `testing/synctest` bubble, so that the real eight seconds, a minute and a day pass at once and "every goroutine is waiting" is something the test can ask for: an order that fails is logged once, with its error, though three handshakes waited for it, and "missing certificate" answers are not logged; a failure for an unrouted host is not logged; a slow success is logged once, a fast one not at all; a flight that outlasts the limit turns its visitors away at the limit with an error, logs that once, and its late answer is still logged and its goroutine ends (the test waits for the flight table to be empty); a second handshake for the same host does not start a second flight, one for another host does; a challenge handshake does not wait behind a flight; a certificate with ten days left is warned about once a day; an order for clients that need an RSA key does not hold up the others; fifty handshakes that fail the same way are one line, and another a minute later. And one test with a real server and real handshakes, set up by the function the proxy uses (`guardCerts`): a visitor who is turned away gets a TLS error before the server's deadline, also one who waited before saying hello, and the wait is logged for a host of the route table.

**Still to check on the VPS:** S6.3c: an app domain whose order cannot complete shows a line in `journalctl -u musdash-proxy` and the handshake ends after 8 s.

## Fix 6 — a mount whose target is not a path (B3)

**What happens.** The report says an `external: true` volume in short syntax gets past `Validate`. The cause is elsewhere. Reproduced with Compose 5.5: in `- v:/x` the volume's name is one letter, and Compose reads a letter and a colon as a Windows drive, so the whole text becomes the *target* of an anonymous volume (`{"type":"volume","target":"v:/x"}`, no source) and the top-level volume `v`, now unused, is left out of the normalised document together with its `external`. Nothing external is attached; Docker then refuses the mount ("mount path must be absolute"), and the person gets Docker's words instead of ours. With a longer name (`vol:/x`) the volume is there and `external` is refused as it should be.

More generally `Validate` never looked at a mount's target, and Compose passes on a relative one (`target: relative/path`) for a volume, a bind or a tmpfs alike.

**The fix.** In `checker.mounts`, every mount's target must be a full path in the container (`docker.ValidMountPath`: absolute, no `..`, none of the characters a `--mount` option splits on). The message names the target; when it looks like a drive (`^[A-Za-z]:[\\/]`) it adds what happened: a volume with a one-letter name is read as a Windows drive, give it a longer name.

**Tests.** `internal/compose`: the normalised document of the report's file (as Compose 5.5 prints it) is refused with the hint; a relative target of each mount type is refused; ordinary targets pass. The sandboxed test against real Docker Compose (where there is one) loads the report's YAML and expects the refusal.

## Fix 7 — names with control characters, image references Docker refuses (F8, F9)

### F8

**What happens.** A project's name is checked for length only, so a NUL byte or a line break is stored. It is escaped wherever it is shown, but it is in notifications, in logs and in the API's JSON.

**The fix.** One rule for every text a person types that is only ever shown (`plainText` in `internal/web`): valid UTF-8, no control characters (Unicode `Cc`, which includes NUL, tab and line breaks), no line or paragraph separator (`U+2028`, `U+2029`), and none of the characters that reorder text (`U+202A`–`U+202E`, `U+2066`–`U+2069`), which can make a name read as another. Not the rest of the format characters: joiners and direction marks belong in Arabic and Persian names. A text that breaks the rule is refused with a message of its own ("… has a character that cannot be shown, such as a line break"), not stripped, and not answered with the message about length.

It is applied to: a project's name and description; a person's name (setup, profile, accepting an invitation); the team's name; the names of a server, a deploy key, a source, a backup storage, a notification channel, a scheduled task and an API token. Some of these refused `\r`, `\n` and NUL already, with the message about length; they get the one rule and its message. Not to texts where a line break belongs: a task's command, variables, a Compose file, a file's content. Names that become part of an address or a command (apps, databases, services, environments, tags) have a pattern already.

### F9

**What happens.** `docker.ValidImage` is one expression that lets a first component be upper-case because it may be a registry's host. So `NGINX:ALPINE` passes (Docker reads a name without a slash as a repository, which must be lower-case) and so does a port of `99999`. Both fail later, at the pull.

**The fix.** `ValidImage` reads a reference the way Docker does: the digest off the end, the tag after the last slash, and the first component is a registry only when there is a slash and the component has a dot or a colon, is `localhost`, or has an upper-case letter. A registry is a host name (letters, digits, hyphens inside, dots between; an IPv4 address is one) with an optional port that is a number from 1 to 65535; the rest is lower-case path components with Docker's separators (one dot, one or two underscores, any number of hyphens); the tag and digest as before. What it must never do stays true and stays tested: nothing that starts with `-` and nothing with a space or a shell character is a reference.

`ValidImage` is also asked at every deployment (`RunSpec.Args`, `Pull`), so what it newly refuses must be what Docker refuses too, or an app that runs today would stop deploying. It is: an upper-case repository, a doubled dot, a port out of range. What Docker would take and this still does not, as before: a registry given as an IPv6 address in brackets, and an underscore in a registry's host name. What it newly accepts, because Docker does: two underscores between parts of a name (`a__b`).

**Tests.** `docker`: a table. Accepted: the existing cases, `127.0.0.1:5000/x`, `10.0.0.5/x`, `registry:5000/x`, `Registry.Example.COM/team/app`, `Foo/bar` (a registry called Foo), `localhost/x`, `localhost:5000/x`, `my_image:1`, `a__b`, `host:05000/x`, a tag in upper case, a digest, every image the catalogue and the builders name. Refused: `NGINX:ALPINE`, `foo/Bar`, `a___b`, `a..b`, `a.-b`, `a_.b/c`, `a-/x`, `host./x`, `-host.io/x`, `x/y:1:2`, `host:0/x`, `host:99999/x`, `[::1]:5000/x`, and the injection cases. `web`: `plainText` by a table; a project name with a NUL, a line break and a right-to-left override is refused with the message; each of the other forms refuses a line break in its name.

## Fix 8 — the observations that are cheap to settle

- **Request headers.** The dashboard's server takes Go's default of 1 MB of headers; the proxy already limits its own to 64 KB. The dashboard gets the same `MaxHeaderBytes` (its cookies are small, and so are the headers of webhooks and of the terminal's upgrade).
- **The upgrade restarts the proxy.** `install.sh` replaces the one binary and restarts both services, the proxy first. The proxy stops listening, lets requests that are under way finish for up to fifteen seconds, and starts again: usually about a second in which new connections are refused, longer while a download or a log stream is under way. The README's upgrade note and the script's first comment say so, next to what they already promise for the control plane. Handing the listening sockets over would end the gap; it is not built here.
- Left as they are, on purpose: five wrong passwords lock an account's sign-in for fifteen minutes (the alternative lets a password be guessed without limit; `reset-password` on the server is the way in for a locked-out Owner); a method that changes something is refused for its missing form token (403) before the router is asked whether the method exists (405); Members see the values of what they work with; a correctly signed push that names no repository deploys.
