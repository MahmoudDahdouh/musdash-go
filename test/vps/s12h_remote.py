from lib import *
import base64
c = owner_client(); st = state(); sid = st["remote"]; R = Client(token=ensure_tokens(c)["read_token"])
a = [x for x in R.get("/api/v1/apps").json() if x["name"] == "t-rweb"][0]["id"]
def rsh(cmd): return shout(f"docker exec t-remote sh -c 'echo {base64.b64encode(cmd.encode()).decode()} | base64 -d | sh'")
rsh("pkill -f 'sshd: /usr/sbin/sshd'; rm -f /etc/ssh/ssh_host_*; ssh-keygen -A >/dev/null; pkill -f sshd-session; sleep 1")
sh("docker exec -d t-remote /usr/sbin/sshd; sleep 2")
d = deploy(c, a); res = dep_wait(c, a, d, 150)
log = shout(f"tail -3 /var/lib/musdash/logs/deployments/{d}.log | cut -c1-200")
check("S12.7b", res == "failed", f"with a new connection after the host key changed, a deployment is refused ({res}): {log[-180:]!r}", sev="S1")
c.post(f"/servers/{sid}/forget-host-key", dict(_csrf=csrf_of(c, "/servers")), follow=True); c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
d = deploy(c, a); res = dep_wait(c, a, d, 300); check("S12.7d", res == "success", f"after Forget host key and Check, deployments work again ({res})", sev="S3")
# S12.13 idle SSH connection closes after ~5 minutes: connection count now vs later
n1 = shout("ss -tn | grep -c ':2222.*ESTAB'")
rec("S12.13b", "INFO", f"SSH connections to the remote right after a deploy: {n1.strip()} (one pooled connection expected)")
# S12.11 server variables expand for apps on the remote (and {{server.X}} resolves per server)
c.submit(f"/servers/{sid}/variables", action=f"/servers/{sid}/variables", vars="SRVV=remote-value\n")
c.submit(f"/apps/{a}/environment", action=f"/apps/{a}/environment", vars="FROM_SERVER={{server.SRVV}}\n")
d = deploy(c, a); dep_wait(c, a, d, 300)
cid = rsh("docker ps -q --filter name=musdash- | head -1"); v = rsh(f"docker exec {cid} printenv FROM_SERVER")
check("S12.11", v == "remote-value", f"{{{{server.SRVV}}}} resolves from the REMOTE server's own variables, not the local one's ('srv-val-4'): {v!r}", sev="S2")
