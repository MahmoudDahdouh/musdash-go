"""Compose services: custom text, from Git (public GitHub), and the validator's refusals."""
from lib_d import *
c = owner_client(); H = lambda n: f"{n}.{HOST}.sslip.io"
def create(name, tpl="custom", **kw):
    r, forms = c.forms(base() + f"/services/new?template={tpl}")
    f = c.find_form(forms, base() + "/services")
    return c.post_form(f, name=name, deploy=kw.pop("deploy", True), connect_env=False, **kw)
def sid_of(r):
    m = re.search(r"/services/([a-z2-7]{12})", r.url or ""); return m.group(1) if m else None
def status(S): return (Client(token=ensure_tokens(c)["read_token"]).get(f"/api/v1/services/{S}").json() or {}).get("status")
def settle(S, t=600): return wait_for(lambda: status(S) if status(S) in ("running", "failed", "stopped", "exited") else None, t, 6)
def drop(S):
    pg = c.get(f"/services/{S}/settings").text; f = [x for x in parse_forms(pg) if x["action"] == f"/services/{S}/delete"][0]
    nm = re.search(r"Type ([a-z0-9\-]+) to confirm", re.sub("<[^>]+>", " ", pg)); c.post_form(f, confirm=nm.group(1), delete_data=True)

# valid custom stack, with a variable and an endpoint domain
compose = "services:\n  web:\n    image: nginx:alpine\n    environment:\n      GREETING: ${GREETING}\n  cache:\n    image: redis:7-alpine\n"
r = create("t-stack", compose=compose, variables="GREETING=hello\n")
S = sid_of(r)
if not S: rec("C1", "FAIL", "custom compose not created: " + flash(r)[-200:], "S2")
else:
    s = settle(S); ctr = shout(f"docker ps --filter name=musdash-{S} --format '{{{{.Names}}}} {{{{.Status}}}}'")
    env = shout(f"docker exec $(docker ps -q --filter name=musdash-{S}-web | head -1) printenv GREETING")
    check("C1 custom compose stack", s == "running" and "web" in ctr and "cache" in ctr and env == "hello", f"status {s}; containers {ctr.replace(chr(10), ' | ')}; variable reaches service: {env!r}", "S2")
    drop(S)
# refusals
bad = {
 "privileged": "services:\n  a:\n    image: nginx\n    privileged: true\n",
 "docker.sock mount": "services:\n  a:\n    image: nginx\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
 "host network": "services:\n  a:\n    image: nginx\n    network_mode: host\n",
 "pid host": "services:\n  a:\n    image: nginx\n    pid: host\n",
 "cap_add": "services:\n  a:\n    image: nginx\n    cap_add: [SYS_ADMIN]\n",
 "bind /": "services:\n  a:\n    image: nginx\n    volumes:\n      - /:/host\n",
 "bind data dir": "services:\n  a:\n    image: nginx\n    volumes:\n      - /var/lib/musdash:/x\n",
 "env_file read": "services:\n  a:\n    image: nginx\n    env_file: /etc/passwd\n",
 "include": "include:\n  - /etc/passwd\nservices:\n  a:\n    image: nginx\n",
 "musdash/ image": "services:\n  a:\n    image: musdash/abc:latest\n",
 "devices": "services:\n  a:\n    image: nginx\n    devices: ['/dev/sda:/dev/sda']\n",
 "security_opt": "services:\n  a:\n    image: nginx\n    security_opt: ['seccomp=unconfined']\n",
}
res = {}
for k, v in bad.items():
    r = create("t-bad", compose=v, deploy=False)
    S = sid_of(r)
    res[k] = "REFUSED" if (r.status == 422 or not S) else "ACCEPTED"
    if S: drop(S)
check("C2 compose validator refuses host-reaching keys", all(v == "REFUSED" for v in res.values()), json.dumps(res), "S1")
# from Git: a public compose repo
r = create("t-gitstack", tpl="git", access="public", repo="https://github.com/docker/awesome-compose", branch="master", compose_path="nginx-golang/compose.yaml")
S = sid_of(r)
if not S: rec("C3", "FAIL", "git compose service not created: " + flash(r)[-200:], "S2")
else:
    s = settle(S, 900); ctr = shout(f"docker ps -a --filter name=musdash-{S} --format '{{{{.Names}}}} {{{{.Status}}}}'")
    rec("C3 compose from Git (docker/awesome-compose nginx-golang)", "PASS" if s in ("running",) else "FAIL", f"status {s}; containers {ctr.replace(chr(10), ' | ')}", "" if s == "running" else "S3",
        evidence=shout(f"tail -c 600 $(ls -t /var/lib/musdash/logs/services/*{S}* | head -1)"))
    drop(S)
