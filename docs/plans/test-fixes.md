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
