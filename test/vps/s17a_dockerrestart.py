from lib import *
c = owner_client(); R = Client(token=ensure_tokens(c)["read_token"])
before = {a["name"]: a["status"] for a in R.get("/api/v1/apps").json()}; dbs = {d["name"]: d["status"] for d in R.get("/api/v1/databases").json()}
cn_before = shout("docker ps --format '{{.Names}}' | sort").split()
print("before:", before, dbs)
sh("python3 -c \"import json,os; p='/etc/docker/daemon.json'; d=json.load(open(p)) if os.path.exists(p) else {}; d['dns']=['8.8.8.8','1.1.1.1']; json.dump(d, open(p,'w'), indent=1)\"; cat /etc/docker/daemon.json")
t0 = time.time(); sh("systemctl restart docker", timeout=300); print("docker restarted in %.0fs" % (time.time() - t0))
time.sleep(25)
cn_after = shout("docker ps --format '{{.Names}}' | sort").split()
missing = set(cn_before) - set(cn_after)
time.sleep(10)
R = Client(token=ensure_tokens(c)["read_token"])
after = {a["name"]: a["status"] for a in R.get("/api/v1/apps").json()}; dbs2 = {d["name"]: d["status"] for d in R.get("/api/v1/databases").json()}
print("after:", after, dbs2)
check("S17.9", not missing, f"`systemctl restart docker`: {len(cn_after)}/{len(cn_before)} containers came back by restart policy; missing: {sorted(missing)}", sev="S2")
bad = {k: (before[k], after.get(k)) for k in before if before[k] == "running" and after.get(k) != "running"}
check("S17.10", not bad, f"musdash statuses re-converged after the Docker restart: apps not running again: {bad}; dbs {dbs2}", sev="S2")
w = Client(f"http://t-web.{HOST}.sslip.io").get("/").status; g = Client(f"http://t-git.{HOST}.sslip.io").get("/").status
check("S17.11", w == 200 and g == 200, f"apps answer through the proxy again: t-web {w}, t-git {g}", sev="S2")
print(shout("journalctl -u musdash-server --since '-2 min' --no-pager | tail -6 | cut -c1-180"))
d = shout("docker run --rm alpine sh -c 'nslookup nodejs.org >/dev/null 2>&1 && echo ok'")
