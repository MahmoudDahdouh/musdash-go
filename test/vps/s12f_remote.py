from lib import *
c = owner_client(); st = state(); sid = st["remote"]; pid = st["proj"]; env = st["env"]; R = Client(token=ensure_tokens(c)["read_token"])
# S12.9 deploy to the remote
r = c.submit(f"/projects/{pid}/apps/new?env={env}", action=f"/projects/{pid}/apps", name="t-rweb", image="nginx:alpine", port="80", domain="", server=sid, deploy=True)
a = re.search(r"/apps/([a-z2-7]{12})", r.url or "").group(1); d = re.search(r"/deployments/([a-z2-7]{12})", r.url or "").group(1)
res = dep_wait(c, a, d, 400); inside = shout("docker exec t-remote docker ps --format '{{.Names}} {{.Image}} {{.Ports}}'"); onhost = shout("docker ps --format '{{.Names}}' | grep -c " + a)
check("S12.9a", res == "success" and a in inside and onhost.strip() == "0", f"app deployed to the remote server: {res}; container runs inside the remote's Docker ({inside!r}), not on the local one ({onhost.strip()})", sev="S2")
log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{a}/deployments/{d}").text)); rec("S12.9b", "INFO", "remote deploy log: " + log[170:520])
page = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{a}").text)); rec("S12.9c", "INFO", "app page on the remote: " + page[110:420])
fl = shout("docker exec t-remote sh -c 'ls -la /var/lib/musdash /var/lib/musdash/apps 2>&1 | head -12; stat -c \"%a %n\" /var/lib/musdash/apps/*/env 2>/dev/null'")
check("S12.9d", True, f"remote data dir layout: {fl[:300]!r}")
# env secret travels via a 0600 file, not on the command line
c.submit(f"/apps/{a}/environment", action=f"/apps/{a}/environment", vars="REMOTE_SECRET=Rem0te-Secret-99")
sh("docker exec t-remote sh -c 'rm -f /tmp/pl; nohup sh -c \"for i in \\$(seq 1 150); do ps -eo args >> /tmp/pl; sleep 0.2; done\" >/dev/null 2>&1 &'")
d2 = deploy(c, a); dep_wait(c, a, d2, 300); time.sleep(4)
hits = shout("docker exec t-remote sh -c 'grep -c Rem0te-Secret-99 /tmp/pl; ps -eo args | grep -c Rem0te'")
envv = shout(f"docker exec t-remote sh -c 'docker exec $(docker ps -q | head -1) printenv REMOTE_SECRET'")
check("S12.9e", hits.split()[0] == "0" and "Rem0te-Secret-99" in envv, f"secret reaches the container but never appears in the remote's process list (hits {hits.split()[0]}); container env: {envv!r}", sev="S1")
# S12.10 install proxy: the remote has no systemd
r = c.post(f"/servers/{sid}/proxy", dict(_csrf=csrf_of(c, "/servers")), follow=True)
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote")
rec("S12.10", "INFO", f"Install proxy on a server without systemd (a container): flash={flash(r)[-160:]!r}; card: {t[i:i+260]!r}")
# S12.8 hostile answers from the remote: uname lies
sh("""docker exec t-remote sh -c 'mkdir -p /usr/local/bin; printf "#!/bin/sh\\necho \\"x86_64; touch /tmp/PWNED\\"\\n" > /usr/local/bin/uname; chmod +x /usr/local/bin/uname'""")
c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
r = c.post(f"/servers/{sid}/proxy", dict(_csrf=csrf_of(c, "/servers")), follow=True); t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote")
pw = shout("docker exec t-remote sh -c 'test -e /tmp/PWNED && echo PWNED || echo clean'; ls /var/lib/musdash/dist 2>&1 | head -3")
check("S12.8", "PWNED" not in pw, f"a remote that answers `uname -m` with 'x86_64; touch /tmp/PWNED' does not get that executed: {pw!r}; card: {t[i:i+200]!r}", sev="S1")
sh("docker exec t-remote rm -f /usr/local/bin/uname")
