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
    r = create("t-bad", compose=v, deploy=True)
    S = sid_of(r)
    if not S: res[k] = "REFUSED at save"
    else:
        st_ = settle(S, 120); lg = shout(f"tail -c 300 $(ls -t /var/lib/musdash/logs/services/*{S}* | head -1)"); ctr = shout(f"docker ps -aq --filter name=musdash-{S} | wc -l")
        res[k] = ("REFUSED at deploy: " + lg.strip().splitlines()[-1][:90]) if st_ == "failed" and ctr == "0" else f"ACCEPTED ({st_}, {ctr} containers)"
        drop(S)
check("C2 compose validator refuses host-reaching keys", all(v.startswith("REFUSED") for v in res.values()), json.dumps(res), "S1")
