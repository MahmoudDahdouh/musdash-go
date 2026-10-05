# musdash full-feature VPS test report

**Date:** 2026-10-05 · **Target:** Ubuntu 24.04, 1 vCPU, 2 GB RAM (+2 GB swap added), Docker 29.8.1, public IPv4 168.235.65.204 · **Plan:** [vps-full-test-plan.md](vps-full-test-plan.md) · **Harness:** `test/vps/` (Python, stdlib only) · **Screenshots:** [screenshots/](screenshots/) · **Second session's results:** [vps-test-report-appendix-b.md](vps-test-report-appendix-b.md)

## Verdict

musdash works as documented across nearly everything that was exercised: **about 480 checks passed**, and the security-relevant behaviour (CSRF, roles, secrets at rest, injection and SSRF guards, Compose sandbox, host-key pinning, webhook indistinguishability, rate limits) held up under hostile input. Idle memory is **21 MB for the server and 13–14 MB for the proxy** (budget 30/20 MB), unchanged after a full day of tests, a 300 MB upload through the proxy, 54 page loads and a host reboot.

**Nine findings (F1–F9), none of them data loss or a security hole.** The two worth fixing first:

1. **Redis and KeyDB databases do not survive a restart of their container** (host reboot, `systemctl restart docker`, restart policy). They crash-loop with `can't create /tmp/musdash.conf: Permission denied` until someone presses Restart in the UI. Reproducible on every Ubuntu host (S8.13, S17.14).
2. **A closed terminal leaves its shell running in the container.** Every time a page is closed one more `sh` stays behind (S15.6c), contradicting the README's "it ends when you leave the page".

Everything else is S3/S4 (docs gaps, a leaked empty Docker network, missing ACME logging). Details, evidence and suggested fixes are in **Findings** below.

## Read this first: what was actually under test

* The committed build `adffb82` was installed on a wiped VPS at 13:09 UTC with `install/install.sh`.
* **Another Claude session was working on the same VPS at the same time.** At 13:31 it replaced the binary with `adffb82-dirty` (this checkout's uncommitted UI changes), created its own data (`t2-*`), ran its own database/service/builder suites, restarted services several times and left `docker` busy. My installer re-run (test S0.7) put the clean `adffb82` back at 17:35.
  Consequence: **most functional results ran against `adffb82-dirty`**, the final reboot and later cases against clean `adffb82`. I re-ran S13, the core of S14 and S5.1–S5.8 on the clean build (same outcomes), and a controlled five-redeploy check (S5.18c). A larger clean re-run was spoiled when the other session restarted the control plane in the middle of it. Two stray app containers appeared during those restarts; they did not reappear in an undisturbed re-run, and the other session later confirmed the cause as a real bug (B2 in its appendix, below). If you need certainty for a release, re-run `test/vps/*.py` on a clean VPS.
* A few early timing results were affected by the other session's load (swap, 98 % disk at one point), e.g. a scheduled-task run that took 19 s.

## How it was tested

| Tool | Used for |
|---|---|
| Python harness (`test/vps/lib.py` and ~45 scripts) | Everything over HTTP: it fills each form from the page's own fields and CSRF token, calls the API, sends webhooks signed with real HMACs, opens real WebSockets, probes the proxy, TLS and rate limits. 554 results recorded in `test/vps/out/results.jsonl`. |
| SSH to the VPS | Ground truth: `docker ps/inspect`, file modes, process lists, journal, SQLite/WAL greps for plaintext secrets, `kill -9`, restarts, reboot. |
| Real browser (headless Chrome via Playwright) | All 54 page kinds at 1280 px and 375 px (console errors, failed requests, external hosts, overflow), toasts, dialogs, keyboard focus, `Secure` cookies over HTTPS. |

Test data is prefixed `t-` (projects, apps, databases, services, channels). A second "server" was a Docker-in-Docker container with `sshd`.
