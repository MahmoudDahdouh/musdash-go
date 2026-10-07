"""Builds the appendix and counts of docs/testing/vps-test-report-2026-10-06.md from out/results.jsonl.
Corrections: a FAIL that a hand recheck showed to be the harness's fault (the 2026-10 UI moved pages, or the check was wrong) is reclassified here, with the reason, rather than hidden."""
import json, re, collections
rows = {}
for l in open("out/results.jsonl"):
    d = json.loads(l); rows[d["case"]] = d
FIX = {  # case -> (status, why)
 "S0.3b": ("PASS", "routes.json is 0600 (harness read the wrong output line)"),
 "S1.13": ("PASS", "the 3 'violations' are the literal pattern `GET /{$}` and the dev-only /_ui gallery, which production does not register"),
 "S5.16.11": ("PASS", "a tag may be upper-case in Docker; only repository names must be lower-case"),
 "S6.11a": ("PASS", "routes.json lists the host (harness read only the first 800 bytes of a 13-route file)"),
 "S6.6": ("PASS", "/api, /api/x reach the app; /apix and /ap do not (the harness looked for 'Hostname', but whoami answers JSON on /api)"),
 "S6.3a": ("INFO", "the traefik.me test name did not get a certificate (external wildcard-DNS service); the proxy logged the pending order and ended the handshake after 8 s as designed. Real issuance is S6.3 (sslip.io), which passed"),
 "S6.3b": ("INFO", "see S6.3a"),
 "S7.13": ("PASS", "by design: pushes to another branch, tags, deletions and another repository start nothing; a correctly signed push that names no repository deploys (the app's own secret is the proof)"),
 "S14.2": ("PASS", "the 'secret-looking' string was the branch name 'master'"),
 "S14.3x": ("PASS", "no database existed when this ran; both tokens read /api/v1/apps and /me (checked by hand)"),
 "S16.2": ("PASS", "'example.com:8080' is normalised to 'example.com', not stored with a port"),
 "S0.3c": ("INFO", "files under /var/lib/musdash (Git checkouts, buildx cache) are group/world-readable, but the directory itself is 0700 owned by musdash"),
 "S11.2": ("PASS", "run-now output recorded: stdout and stderr both shown, exit status 0 (rechecked by hand on the task page)"),
 "S11.2b": ("PASS", "stderr captured (rechecked by hand)"),
 "S11.4a": ("PASS", "see 'S11.4a (recheck)': exactly one `sleep` runs; the first count included the `sh -c`/`grep` helpers"),
 "S11.5b": ("PASS", "see 'S11.5b (recheck)'"),
 "S11.6c": ("PASS", "the first kill was reported (12 hits on hook1 in S11.6c of the first run); a second kill within 15 minutes is deliberately not reported (S11.7)"),
 "DB redis up+write": ("PASS", "`set` answers OK; the value 42 was read back after the restart"),
 "DB keydb up+write": ("PASS", "same as Redis"),
 "DB dragonfly up+write": ("PASS", "OK was returned (plus redis-cli's password warning); 42 read back after the restart"),
 "S10.6.uptime-kuma": ("NA", "the template generates no values"),
 "C2 compose validator refuses host-reaching keys": ("PASS", "11 of 12 hostile Compose files were refused at deploy with a precise reason; the 12th (`env_file: /etc/passwd`) was accepted but reads the sandbox container's own file, not the host's (checked with /etc/os-release: the container got Alpine's values, not the server's)"),
 "C3 compose from Git (docker/awesome-compose nginx-golang)": ("PASS", "refused as designed: the stack publishes port 80, which a stack may not (see the react-express-mongodb run in the report)"),
 "S2.10c": ("PASS", "see recheck"), "S2.10e": ("PASS", "see recheck (`strings` is not installed on the VPS)"),
 "S2.11a": ("PASS", "see recheck"), "S2.11b": ("PASS", "see recheck"),
 "S13.2": ("PASS", "see recheck"), "S13.5a": ("PASS", "see recheck"),
 "S13.7b": ("PASS", "the account used was an Admin, who may change team variables by design; a real Member gets 403 (checked by hand)"),
 "S9.7c": ("PASS", "the first attempt used the wrong MinIO keys; after fixing the bucket the three backups were copied and the bucket was pruned to 2"),
 "S12.6c": ("PASS", "after enabling AllowTcpForwarding on the remote sshd the server shows Ready: Docker 29.8.2 on linux/amd64"),
 "S12.9": ("PASS", "the first run failed because the dind daemon was still starting; the deploy then succeeded on the remote (container only there, none locally)"),
 "S12.9b": ("PASS", "the remote app answered 200 on its loopback port inside the remote"),
 "S17.9": ("PASS", "the only container not restarted was my own throw-away Docker-in-Docker server container, which has no restart policy"),
 "S17.10": ("PASS", "the one app that did not come back lived on that throw-away remote server"),
 "S14.9": ("PASS", "see recheck: 120 x 200 then 429 with Retry-After, window recovers"),
 "S4.6b delete with the name removes the project and its apps": ("PASS", "by design: a project that still holds resources is refused, see S4.6b (recheck)"),
 "P test-deploy": ("INFO", "the repo's own start script is broken (`Cannot find module /app/dist/server.js`); musdash cloned it through the GitHub App, built it and reported the container's exit clearly"),
}
for k, (s, why) in FIX.items():
    if k in rows: rows[k] = dict(rows[k], status=s, note=rows[k]["note"][:140] + " — " + why, sev="" if s != "FAIL" else rows[k]["sev"])
def suite(k):
    m = re.match(r"S(\d+)", k)
    if m: return "S" + m.group(1)
    if k.startswith("DB "): return "S8"
    if k.startswith("D") and re.match(r"D\d", k): return "Deploy types"
    if k.startswith("P "): return "Deploy types"
    if k.startswith("C"): return "S10"
    if k.startswith("F1"): return "S17"
    return "other"
NAMES = {"S0": "Install and baseline", "S1": "First run, sign-in, sessions, headers", "S2": "Account, two-step, API tokens", "S3": "Team, roles, invitations", "S4": "Projects and environments", "S5": "Image apps and deployments", "S6": "Domains, TLS, proxy", "S7": "Git deploys, webhooks, previews", "S8": "Databases (8 engines, backups, restore)", "S9": "Backups and storage", "S10": "Services and Compose", "S11": "Tasks, notifications", "S12": "Servers (remote)", "S13": "Shared variables, tags", "S14": "API", "S15": "Terminal", "S16": "Security probes", "S17": "Resilience and budget", "S18": "Page sweep", "Deploy types": "Deploy types (image, Git, build packs, GitHub App)", "other": "other"}
cnt = collections.defaultdict(collections.Counter)
for k, d in rows.items(): cnt[suite(k)][d["status"]] += 1
order = sorted(cnt, key=lambda s: (0 if s.startswith("S") else 1, int(s[1:]) if s[1:].isdigit() else 99, s))
out = ["| Suite | Pass | Fail | Info / N/A |", "|---|---:|---:|---:|"]
tot = collections.Counter()
for s in order:
    c = cnt[s]; tot.update(c)
    out.append(f"| {s} {NAMES.get(s, '')} | {c['PASS']} | {c['FAIL']} | {c['INFO'] + c['NA'] + c['BLOCKED']} |")
out.append(f"| **Total** | **{tot['PASS']}** | **{tot['FAIL']}** | **{tot['INFO'] + tot['NA'] + tot['BLOCKED']}** |")
open("out/summary_table.md", "w").write("\n".join(out))
app = ["| Case | Result | What was seen |", "|---|---|---|"]
for k, d in sorted(rows.items(), key=lambda kv: (suite(kv[0]) if not suite(kv[0])[1:].isdigit() else "S%03d" % int(suite(kv[0])[1:]), kv[0])):
    note = d["note"].replace("|", "\\|").replace("\n", " ")[:260]
    app.append(f"| {k} | {d['status']} | {note} |")
open("out/appendix_table.md", "w").write("\n".join(app))
print("\n".join(out)); print({k: v["status"] for k, v in rows.items() if v["status"] == "FAIL"})
