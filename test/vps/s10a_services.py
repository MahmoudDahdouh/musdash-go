from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
def new_service(name, compose, variables="", connect=False, deploy=True):
    r = c.submit(f"/projects/{pid}/services/new?env={env}&template=custom", action=f"/projects/{pid}/services", name=name, compose=compose, variables=variables, connect_env=connect, deploy=deploy)
    m = re.search(r"/services/([a-z2-7]{12})", r.url or "") or re.search(r"/services/([a-z2-7]{12})", r.text)
    return (m.group(1) if m else None), r
def svc_state(sid):
    t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/services/{sid}").text)); return t
def settle(sid, t=240):
    def f():
        s = svc_state(sid)
        for w in ("Running", "Failed", "Stopped", "Error"):
            if w in s[:400]: return w
        return None
    return wait_for(f, t, 4)
def drop(sid):
    pg = c.get(f"/services/{sid}/settings").text
    f = [x for x in parse_forms(pg) if x["action"] == f"/services/{sid}/delete"]
    nm = re.search(r"Type ([a-z0-9\-]+) to confirm", re.sub("<[^>]+>", " ", pg))
    if f: c.post_form(f[0], **{x["name"]: (nm.group(1) if nm else "x") for x in f[0]["fields"] if x["type"] == "text"}, **{x["name"]: True for x in f[0]["fields"] if x["type"] == "checkbox"})
REFUSE = {
 "privileged":   ("services:\n  a:\n    image: nginx:alpine\n    privileged: true\n", "privileged"),
 "host-network": ("services:\n  a:\n    image: nginx:alpine\n    network_mode: host\n", "host"),
 "pid-host":     ("services:\n  a:\n    image: nginx:alpine\n    pid: host\n", "pid"),
 "ipc-host":     ("services:\n  a:\n    image: nginx:alpine\n    ipc: host\n", "ipc"),
 "cap-add":      ("services:\n  a:\n    image: nginx:alpine\n    cap_add: [SYS_ADMIN]\n", "cap"),
 "devices":      ("services:\n  a:\n    image: nginx:alpine\n    devices: ['/dev/sda:/dev/sda']\n", "device"),
 "docker-sock":  ("services:\n  a:\n    image: nginx:alpine\n    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n", "docker.sock"),
 "etc-bind":     ("services:\n  a:\n    image: nginx:alpine\n    volumes: ['/etc:/hostetc']\n", "/etc"),
 "data-dir":     ("services:\n  a:\n    image: nginx:alpine\n    volumes: ['/var/lib/musdash:/x']\n", "musdash"),
 "root-bind":    ("services:\n  a:\n    image: nginx:alpine\n    volumes: ['/:/host']\n", "/"),
 "subnet":       ("services:\n  a:\n    image: nginx:alpine\n    networks: [n]\nnetworks:\n  n:\n    ipam:\n      config:\n        - subnet: 10.99.0.0/24\n", "subnet"),
 "ext-network":  ("services:\n  a:\n    image: nginx:alpine\n    networks: [n]\nnetworks:\n  n:\n    external: true\n", "external"),
 "ext-volume":   ("services:\n  a:\n    image: nginx:alpine\n    volumes: [v:/d]\nvolumes:\n  v:\n    external: true\n", "external"),
 "own-image":    ("services:\n  a:\n    image: musdash/x:1\n", "musdash"),
 "low-port":     ("services:\n  a:\n    image: nginx:alpine\n    ports: ['80:80']\n", "port"),
 "reserved-port":("services:\n  a:\n    image: nginx:alpine\n    ports: ['25000:80']\n", "port"),
 "security-opt": ("services:\n  a:\n    image: nginx:alpine\n    security_opt: ['seccomp=unconfined']\n", "security"),
 "userns":       ("services:\n  a:\n    image: nginx:alpine\n    userns_mode: host\n", "userns"),
 "sysctls-net":  ("services:\n  a:\n    image: nginx:alpine\n    sysctls: {net.ipv4.ip_forward: 1}\n", ""),
 "cgroup-host":  ("services:\n  a:\n    image: nginx:alpine\n    cgroup: host\n", "cgroup"),
 "build-ctx":    ("services:\n  a:\n    build: .\n", "build"),
 "volumes-from-sock": ("services:\n  a:\n    image: nginx:alpine\n    volumes:\n      - type: bind\n        source: /proc\n        target: /p\n", "/proc"),
 "extends-file": ("services:\n  a:\n    extends:\n      file: /etc/passwd\n      service: x\n", ""),
 "include-file": ("include:\n  - /etc/hostname\nservices:\n  a:\n    image: nginx:alpine\n", ""),
 "env-file":     ("services:\n  a:\n    image: nginx:alpine\n    env_file: /etc/shadow\n", ""),
}
res = {}
for k, (compose, word) in REFUSE.items():
    sid, r = new_service("t-" + k[:14].replace("_", "-"), compose)
    if not sid:
        res[k] = ("refused at save", flash(r)[-120:]); continue
    s = settle(sid, 150); txt = svc_state(sid)
    log = shout(f"cat /var/lib/musdash/logs/services/{sid}*.log 2>/dev/null | tail -5 | cut -c1-200")
    res[k] = (s, (txt + " " + log)[-200:])
    drop(sid)
refused = {k: v for k, v in res.items() if v[0] in ("Failed", "Error") or v[0] == "refused at save"}
accepted = {k: v for k, v in res.items() if k not in refused}
check("S10.4", not accepted, f"{len(refused)}/{len(REFUSE)} dangerous Compose files refused. NOT refused: { {k: v[0] for k, v in accepted.items()} }", sev="S1", evidence=res)
for k, v in res.items(): print("   ", k, "->", v[0], "|", v[1][-110:])
