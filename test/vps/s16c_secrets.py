from lib import *
c = owner_client(); st = state(); web = st["web"]
SECRETS = {"owner password": st["owner"]["password"], "env secret": "S3cr3t-Value-4711", "route password": "Sup3rS3cret!", "webhook secret": "topsecret", "read token": st["read_token"], "deploy token": st["deploy_token"], "member pw": st["user_pw"]}
# S16.7 process list during a deploy with secret variables
c.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars="SECRET_TOKEN=S3cr3t-Value-4711\n")
sh("rm -f /tmp/pslog; nohup sh -c 'for i in $(seq 1 120); do ps -eo args >> /tmp/pslog; sleep 0.25; done' >/dev/null 2>&1 &")
for i in range(2):
    d = deploy(c, web); dep_wait(c, web, d, 200)
time.sleep(5)
n = shout("grep -c 'S3cr3t-Value-4711' /tmp/pslog; grep -c 'docker' /tmp/pslog").split()
check("S16.7", n[0] == "0", f"secret value never appeared in the process list during 2 deploys ({n[1]} docker command lines sampled, secret hits {n[0]})", sev="S1")
sh("rm -f /tmp/pslog")
# S16.6 logs
jl = shout("journalctl --no-pager --since '-3 hours' | grep -c -E '" + "|".join(re.escape(v) for k, v in SECRETS.items() if k not in ("member pw",)) + "'")
check("S16.6a", jl.strip() == "0", f"journal (all units, 3h) contains none of the planted secrets: hits {jl.strip()}", sev="S1")
fl = sh("grep -rl -a -E '" + "|".join(re.escape(v) for k, v in SECRETS.items() if k not in ("member pw",)) + "' /var/lib/musdash/logs /var/lib/musdash/apps 2>/dev/null")[1].strip()
rec("S16.6b", "PASS" if not fl or all(p.endswith("/env") for p in fl.splitlines()) else "FAIL", f"files under musdash/logs and apps containing planted secrets: {fl.splitlines()} (only the 0600 env files are expected)", "" if not fl or all(p.endswith('/env') for p in fl.splitlines()) else "S1")
dl = shout("for f in /var/lib/musdash/logs/deployments/*.log; do grep -l -E 'S3cr3t-Value|Sup3rS3cret|topsecret|msd_' $f; done | head")
check("S16.6c", not dl, f"deployment logs hold no secret values: {dl.splitlines()[:3]}", sev="S1")
# S16.5 at rest
out = shout("cd /var/lib/musdash && for s in 'Owner-Pass-2026' 'S3cr3t-Value-4711' 'Sup3rS3cret' 'topsecret' 't-sink.168' 'hook1' '" + st['read_token'] + "' 'Member-Pass-2026'; do printf '%s: ' \"$s\"; cat musdash.db musdash.db-wal | grep -a -c \"$s\"; done")
bad = [l for l in out.splitlines() if not l.endswith(": 0")]
check("S16.5", not bad, "plaintext secrets absent from SQLite+WAL (owner/member passwords, env secret, route password, webhook secret and URL, API token): " + out.replace("\n", " | "), sev="S1")
# S0.3 modes of files created since
modes = shout("find /var/lib/musdash -type f -perm /077 | head; find /var/lib/musdash -type d -perm /077 | head")
check("S0.3c", not modes, f"no file or directory under the data dir is group/world accessible: {modes[:200]!r}", sev="S1")
# docker inspect for secret exposure on every musdash container
leak = shout("for c in $(docker ps -q); do docker inspect $c --format '{{.Name}} {{json .Config.Cmd}} {{json .Args}} {{json .Config.Labels}}' ; done | grep -c -E 'S3cr3t-Value|Sup3rS3cret|topsecret|msd_'")
check("S16.7b", leak.strip() == "0", f"no secret in Cmd/Args/Labels of any running container: hits {leak.strip()}", sev="S1")
