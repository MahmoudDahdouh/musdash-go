from lib import *
c = owner_client(); st = state(); sid = st["remote"]; R = Client(token=ensure_tokens(c)["read_token"])
pass
a = [x for x in R.get("/api/v1/apps").json() if x["name"] == "t-rweb"][0]["id"]
d = deploy(c, a); res = dep_wait(c, a, d, 400)
inside = shout("docker exec t-remote docker ps --format '{{.Names}} {{.Image}} {{.Ports}}'"); onhost = shout("docker ps --format '{{.Names}}' | grep -c " + a)
check("S12.9a", res == "success" and a in inside and onhost.strip() == "0", f"app deployed to the remote server: {res}; it runs inside the remote's Docker ({inside!r}) and not on the local one ({onhost.strip()})", sev="S2")
c.submit(f"/apps/{a}/environment", action=f"/apps/{a}/environment", vars="REMOTE_SECRET=Rem0te-Secret-99")
sh("docker exec t-remote sh -c 'rm -f /tmp/pl; nohup sh -c \"for i in \\$(seq 1 150); do ps -eo args >> /tmp/pl; sleep 0.2; done\" >/dev/null 2>&1 &'")
d2 = deploy(c, a); dep_wait(c, a, d2, 300); time.sleep(5)
hits = shout("docker exec t-remote sh -c 'grep -c Rem0te-Secret-99 /tmp/pl'")
cid = shout("docker exec t-remote docker ps -q --filter name=musdash-" + a)
envv = shout(f"docker exec t-remote docker exec {cid.splitlines()[0]} printenv REMOTE_SECRET")
check("S12.9e", hits.strip() == "0" and "Rem0te-Secret-99" in envv, f"secret reaches the container but never appears in the remote's process list (hits {hits.strip()}); container env: {envv!r}", sev="S1")
# generated address / loopback port reachable only via the remote itself
pg = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{a}").text)); rec("S12.9g", "INFO", "app page: " + pg[110:460])
# S12.7 host key change
sh("docker exec t-remote sh -c 'rm -f /etc/ssh/ssh_host_*; ssh-keygen -A >/dev/null; pkill -HUP -f \"sshd: /usr/sbin/sshd\"; sleep 1'")
sh("docker exec t-remote pkill -f 'sshd: /usr/sbin/sshd' ; docker exec -d t-remote /usr/sbin/sshd; sleep 2")
time.sleep(2)
c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote")
check("S12.7a", "Not reachable" in t[i:i+60] or "host key" in t[i:i+300].lower(), f"after the remote's host keys change, Check refuses to talk to it: {t[i:i+300]!r}", sev="S1")
# deploys to it are refused too
d3 = deploy(c, a); r3 = dep_wait(c, a, d3, 120)
check("S12.7b", r3 == "failed", f"a deployment to the server whose key changed fails instead of trusting the new key ({r3})", sev="S1")
c.post(f"/servers/{sid}/forget-host-key", dict(_csrf=csrf_of(c, "/servers")), follow=True)
c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote")
check("S12.7c", "Ready" in t[i:i+60], f"'Forget host key' then Check records the new key: {t[i:i+200]!r}", sev="S3")
