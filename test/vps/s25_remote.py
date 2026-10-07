"""A second server: a Docker-in-Docker container with sshd on the VPS, driven over SSH by musdash."""
from lib_d import *
c = owner_client()
print(sh("""docker rm -f t-remote >/dev/null 2>&1; docker run -d --name t-remote --privileged -e DOCKER_TLS_CERTDIR= -p 2222:22 docker:29-dind >/dev/null && sleep 8 && docker exec t-remote sh -c 'apk add --no-cache openssh bash git curl >/dev/null 2>&1; ssh-keygen -A >/dev/null; mkdir -p /root/.ssh; chmod 700 /root/.ssh; printf "PermitRootLogin prohibit-password\\nPasswordAuthentication no\\n" >> /etc/ssh/sshd_config; passwd -u root >/dev/null 2>&1; /usr/sbin/sshd; docker version --format "{{.Server.Version}}"; docker compose version --short'""", timeout=400)[1])
# validation of the add-server form
f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == "/servers"][0]
bad = {}
for label, kw in {"empty host": dict(host=""), "space": dict(host="host with space"), "semicolon": dict(host="a;b"), "option": dict(host="-oProxyCommand=id"), "subshell": dict(host="$(id)"), "url": dict(host="http://x"), "user injection": dict(ssh_user="root;id"), "user option": dict(ssh_user="-oX"), "port 0": dict(port="0"), "port 70000": dict(port="70000"), "data_dir /": dict(data_dir="/"), "data_dir /etc": dict(data_dir="/etc"), "data_dir relative": dict(data_dir="rel/x")}.items():
    d = dict(name="t-bad", host="127.9.9.9", port="22", ssh_user="root", key="new", data_dir=""); d.update(kw)
    rr = c.post_form(f, follow=False, **d); bad[label] = rr.status
made = "t-bad" in flash(c.get("/servers", follow=True))
check("S12.5 add-server validation", not made and all(v in (200, 422) for v in bad.values()), f"unsafe host/user/port/data dir refused: {bad}; a server was created: {made}", "S1")
r = c.post_form(f, follow=True, name="t-remote", host=HOST, port="2222", ssh_user="root", key="new", data_dir="")
pub = re.findall(r"(ssh-ed25519 AAAA[A-Za-z0-9+/=]+)", c.get("/servers").text + r.text)
sid = [s for s in Client(token=ensure_tokens(c)["read_token"]).get("/api/v1/servers").json() if s["name"] == "t-remote"][0]["id"]; save_state(remote=sid)
pg = c.get("/servers").text
check("S12.5a", "PRIVATE KEY" not in pg, "the private key is never shown on the Servers page", "S1")
def check_server():
    c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
    t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote"); return t[i:i + 800]
t = check_server(); rec("S12.6a", "PASS" if "Running" not in t[:120] and "Unreachable" not in t[:0] else "FAIL", f"Check before the public key is installed: {t[:240]!r}")
sh(f"docker exec t-remote sh -c 'echo \"{pub[-1]} musdash\" > /root/.ssh/authorized_keys; chmod 600 /root/.ssh/authorized_keys'")
t = check_server()
fp_real = shout("docker exec t-remote ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"); m = re.search(r"SHA256:[A-Za-z0-9+/]{43}", t)
check("S12.6b", m and m.group(0) in fp_real, f"first Check records the host key and shows its fingerprint {m.group(0) if m else None}; server's own: {fp_real!r}", "S1")
check("S12.6c", all(w in t for w in ("Docker", "Compose")) and "git" in t.lower(), f"Check reports Docker, Compose and git: {t[:300]!r}", "S3")
# deploy an app to the remote
r, forms = c.forms(base() + "/apps/new"); f2 = c.find_form(forms, base() + "/apps")
print("fields:", [x["name"] for x in f2["fields"]])
rr = c.post_form(f2, name="t-rapp", image="nginx:alpine", port="80", domain="", deploy=True, server=sid)
m = re.search(r"/apps/([a-z2-7]{12})/deployments/([a-z2-7]{12})", rr.url or "")
if not m: rec("S12.9", "FAIL", "app on remote not created: " + flash(rr)[-250:], "S2")
else:
    aid, dep = m.groups(); res = dep_wait(c, aid, dep, 400)
    ins = shout(f"docker exec t-remote docker ps --format '{{{{.Names}}}} {{{{.Image}}}} {{{{.Ports}}}}'")
    local = shout(f"docker ps -q --filter name=musdash-{aid} | wc -l")
    check("S12.9", res == "success" and aid in ins and local == "0", f"app deployed to the remote Docker only: deployment {res}; remote has {ins!r}; local containers {local}", "S2", evidence=shout(f"tail -c 500 /var/lib/musdash/logs/deployments/{dep}.log"))
    port = re.search(r"127.0.0.1:(\d+)->", ins)
    body = shout(f"docker exec t-remote curl -s -m5 -o /dev/null -w %{{http_code}} http://127.0.0.1:{port.group(1)}/") if port else ""
    check("S12.9b", body == "200", f"remote app answers on its loopback port inside the remote server ({body})", "S2")
    rr = c.get(f"/servers")
    # server cannot be removed while it has resources
    f3 = [x for x in parse_forms(c.get("/servers").text) if x["action"] == f"/servers/{sid}/delete"]
    if f3:
        d = c.post_form(f3[0], follow=True, confirm="t-remote")
        gone = "t-remote" not in flash(c.get("/servers"))
        check("S12.12a", not gone, f"a server with an app is not removed ({flash(d)[-140:]!r})", "S2")
    save_state(remote_app=aid)
    # metrics of the remote server
    mp = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/servers/{sid}").text)) if c.get(f"/servers/{sid}").status == 200 else ""
    rec("S12.3", "INFO", f"server page for the remote: status {c.get(f'/servers/{sid}').status}; {mp[200:500]!r}")
