# Appendix: the second tester's results (suites S8–S10, S12, S13, S15, S17)

Same VPS and plan as [vps-test-report.md](vps-test-report.md), run in parallel by a second Claude session. Results are in `test/vps/out/results-b.jsonl` (later rows for a case supersede earlier ones; several first attempts were my own test mistakes and are corrected there). Scripts are `test/vps/lib_b.py`, `s08*`–`s17*` and `wsclient.py`. Test data is prefixed `t2-`.

**Build under test:** `adffb82-dirty` (the working tree at 16:29 local, with the uncommitted colour and control changes), installed with `install/install.sh` at 13:31 UTC. The other session put clean `adffb82` back at 17:35 UTC for its upgrade test, so S17 ran on the clean build. The dirty build is back on the VPS now (`musdash version` says `adffb82-dirty`).

## Tally (latest row per case)

| Suite | Pass | Fail | Blocked | Info |
|---|---:|---:|---:|---:|
| S8 Databases | 25 | 1 | 0 | 0 |
| S9 Backups and storage | 35 | 1 | 0 | 12 |
| S10 Services | 50 | 1 | 2 | 23 |
| S12 Servers and metrics | 21 | 1 | 0 | 3 |
| S13 Shared variables and tags | 16 | 0 | 0 | 1 |
| S15 Terminal | 24 | 0 | 1 | 0 |
| S17 Resilience | 11 | 3 | 0 | 1 |

Idle RSS stayed at 24 MB (server) and 16 MB (proxy); under 2,684 requests in 24 s (16 proxy plus 3 dashboard clients) it peaked at 24.7 MB and 17.4 MB with no errors.

## Findings

| # | Sev | Where | What |
|---|---|---|---|
| B1 | S2 | S8.13 | Redis and KeyDB crash-loop whenever the same container restarts (`docker restart`, dockerd restart, reboot): `can't create /tmp/musdash.conf: Permission denied`. `redisCommand`/`keydbCommand` in `internal/catalog/databases.go` write the file as root, `chown` it, and the second start cannot reopen it in the sticky `/tmp` (`fs.protected_regular=2`). Found by the other session, reproduced independently. Fix: `rm -f` first, or write into a root-owned directory. |
| B2 | S3 | S17.1, S17.1b | `kill -9` of `musdash-server` during a deploy that is waiting on the health check: systemd restarts it in 2 s, the deployment is marked failed and the old container keeps serving, but the new container that had been started stays running and nothing removes it, not even the next successful deployment. Two containers per app until someone removes one. |
| B3 | S3 | S10.4-extvol-short | A top-level volume with `external: true` used in short syntax (`- v:/x`) is not refused by `compose.Validate`. The resolved file has a mangled mount (`target: "v:/x"`, no source) and `docker compose up` fails with "mount path must be absolute". Nothing runs, but the person gets a Docker error instead of the refusal naming the line. Long syntax is refused correctly. |
| B4 | S4 | S9.9b | `kill -9` during a backup leaves the partial temp file `.musdash-<n>` in `<data>/backups/<db>/` for good (39 MB in the test). It is hidden from the list and not covered by retention. The row itself is recovered correctly ("musdash was restarted while this was running"). |
| B5 | S4 | S12.5d | An Admin can add a remote server with data directory `/etc`; `/`, relative paths and `..` are refused. |
| B6 | S4 | S12.9b | A remote whose sshd has `AllowTcpForwarding no` (Alpine's default) fails the health check with `ssh: rejected: administratively prohibited`. The README, spec and Servers page do not mention the requirement. |

Corrected expectations (not defects): `docker kill` leaves a container down because Docker treats it as manual, so the restart policy only covers crashes (S17.4a/c); Deploy all deduplicates only against deployments that are still waiting (S13.9b); shared variables cannot name other shared variables at all, which is stricter than the plan (S13.4a); endpoint SSRF checks for backup storage run when the storage is tested or used, not when saved (S9.8t); `*.sslip.io` endpoints are plain HTTP by design (S10.12).

## Blocked or partial

* S10.11 Compose from Git: clone, checkout and validation shown against a public repository; the deploy path with a read-only repository mount and the symlink refusal need a repository we control.
* S10.12 service-endpoint TLS needs a domain of our own.
* S15.5 idle close is a 30-minute timer; not waited for.
* S15.1 full-screen programs in a real browser terminal were not exercised; the WebSocket protocol, shell, resize and cleanup were.
* S12.10 Install proxy: the binary and unit are staged correctly; enabling the unit needs systemd, which the test container lacks.
* S12.13 idle close of the pooled SSH connection could not be observed: the `docker events` monitor keeps the connection busy. One connection was reused for 11 minutes.
