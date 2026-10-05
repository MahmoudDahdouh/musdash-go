from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
API = Client(token=ensure_tokens(c)["read_token"])
r = c.submit(f"/projects/{pid}/databases/new?env={env}&engine=redis", action=f"/projects/{pid}/databases", name="t-redis-rs")
db = re.search(r"/databases/([a-z2-7]{12})", r.url or r.text).group(1)
wait_for(lambda: API.get(f"/api/v1/databases/{db}").json().get("status") == "running" or None, 300, 4); time.sleep(3)
cid = shout(f"docker ps -q --filter name=musdash-db-{db}")
print(shout(f"docker exec {cid} ls -la /tmp/musdash.conf"))
sh(f"docker restart {cid}"); time.sleep(8)
st1 = shout(f"docker ps -a --filter name=musdash-db-{db} --format '{{{{.Status}}}}'"); lg = shout(f"docker logs --tail 3 {cid} 2>&1")
check("S8.13", "Restarting" not in st1 and "Exited" not in st1 and "Permission denied" not in lg, f"Redis container survives `docker restart` (what a host reboot or daemon restart does): status {st1!r}; log tail {lg[-120:]!r}", sev="S2",
      evidence="redis template writes /tmp/musdash.conf with umask 077 + chown redis, then runs as root without CAP_DAC_OVERRIDE; on the second start the file exists and cannot be rewritten")
# which engines share the pattern?
import json as _j
print(shout(f"docker inspect {cid} --format '{{{{json .HostConfig.CapDrop}}}} {{{{json .HostConfig.CapAdd}}}} {{{{json .Config.User}}}}'"))
