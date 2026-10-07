from lib_b import *
import base64
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
def flashmsg(rr):
    ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", ""))
    return base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else ""
def create(name, compose, variables="", deploy=False):
    forms = parse_forms(c.get(f"/projects/{P}/e/{E}/services/new?template=custom", follow=True).text)
    f = [x for x in forms if x["action"] == f"/projects/{P}/e/{E}/services"][0]
    rr = c.post_form(f, follow=False, name=name, compose=compose, variables=variables, deploy=deploy)
    return rr, flashmsg(rr)
def svc_exists(name):
    return name in flash(c.get(f"/projects/{P}?env={E}", follow=True))
BAD = {
 "privileged": "services:\n  a:\n    image: nginx:alpine\n    privileged: true\n",
 "network_host": "services:\n  a:\n    image: nginx:alpine\n    network_mode: host\n",
 "pid_host": "services:\n  a:\n    image: nginx:alpine\n    pid: host\n",
 "cap_add": "services:\n  a:\n    image: nginx:alpine\n    cap_add: [SYS_ADMIN]\n",
 "devices": "services:\n  a:\n    image: nginx:alpine\n    devices: ['/dev/sda:/dev/sda']\n",
 "docker_sock": "services:\n  a:\n    image: nginx:alpine\n    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n",
 "etc_mount": "services:\n  a:\n    image: nginx:alpine\n    volumes: ['/etc:/host-etc']\n",
 "data_dir_mount": "services:\n  a:\n    image: nginx:alpine\n    volumes: ['/var/lib/musdash:/d']\n",
 "custom_subnet": "services:\n  a:\n    image: nginx:alpine\nnetworks:\n  default:\n    ipam:\n      config:\n        - subnet: 10.99.0.0/16\n",
 "external_volume": "services:\n  a:\n    image: nginx:alpine\n    volumes: ['v:/x']\nvolumes:\n  v:\n    external: true\n",
 "external_network": "services:\n  a:\n    image: nginx:alpine\n    networks: [n]\nnetworks:\n  n:\n    external: true\n",
 "unknown_key": "services:\n  a:\n    image: nginx:alpine\n    totally_new_key: 1\n",
 "musdash_image": "services:\n  a:\n    image: musdash/railpack:0.40.1\n",
 "security_opt": "services:\n  a:\n    image: nginx:alpine\n    security_opt: ['seccomp:unconfined']\n",
 "userns_host": "services:\n  a:\n    image: nginx:alpine\n    userns_mode: host\n",
 "ipc_host": "services:\n  a:\n    image: nginx:alpine\n    ipc: host\n",
 "bind_root": "services:\n  a:\n    image: nginx:alpine\n    volumes: ['/:/host']\n",
 "sysctls": "services:\n  a:\n    image: nginx:alpine\n    sysctls: ['kernel.core_pattern=|/x']\n",
 "cgroup_parent": "services:\n  a:\n    image: nginx:alpine\n    cgroup_parent: /x\n",
 "extra_hosts_meta": "services:\n  a:\n    image: nginx:alpine\n    command: ['sh']\n    entrypoint: ['sh', '-c', 'x']\n    extra_hosts: ['metadata:169.254.169.254']\n",
}
for k, comp in BAD.items():
    name = f"t2-bad-{k}".replace("_", "-")[:30]
    before = shout("docker ps -aq | wc -l")
    rr, msg = create(name, comp)
    made = svc_exists(name)
    note = f"{rr.status} {msg[:200]}"
    # refusal either at create (not listed) or flagged; record evidence
    rec(f"S10.4-{k}", "PASS" if (not made and msg.startswith(("danger", "warn"))) or (not made) else "INFO", f"created={made} | {note}", evidence=comp)
