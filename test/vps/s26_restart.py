"""F1 check: Redis/KeyDB after `systemctl restart docker`, and everything else re-converging."""
from lib_d import *
c = owner_client(); R = lambda: Client(token=ensure_tokens(c)["read_token"])
ids = {}
for eng in ("redis", "keydb"):
    r = c.submit(base() + f"/databases/new?engine={eng}", action=base() + "/databases", name=f"t-{eng}-r", engine=eng)
    ids[eng] = re.search(r"/databases/([a-z2-7]{12})", r.url).group(1)
    wait_for(lambda: "Running" in flash(c.get(f"/databases/{ids[eng]}", follow=True))[:700], 300, 4)
save_state(redis_r=ids["redis"], keydb_r=ids["keydb"])
before = {a["name"]: a["status"] for a in R().get("/api/v1/apps").json()}; dbs = {d["name"]: d["status"] for d in R().get("/api/v1/databases").json()}
cn_before = shout("docker ps --format '{{.Names}}' | sort").split()
print("before:", len(cn_before), "containers;", dbs)
t0 = time.time(); sh("systemctl restart docker", timeout=300); print("docker restarted in %.0fs" % (time.time() - t0))
time.sleep(40)
cn_after = shout("docker ps --format '{{.Names}}' | sort").split(); missing = set(cn_before) - set(cn_after)
time.sleep(15)
after = {a["name"]: a["status"] for a in R().get("/api/v1/apps").json()}; dbs2 = {d["name"]: d["status"] for d in R().get("/api/v1/databases").json()}
check("S17.9", not missing, f"`systemctl restart docker`: {len(cn_after)}/{len(cn_before)} containers came back by restart policy; missing: {sorted(missing)}", "S2")
for eng in ("redis", "keydb"):
    cn = f"musdash-db-{ids[eng]}"
    st = shout(f"docker inspect {cn} --format '{{{{.State.Status}}}} restarts={{{{.RestartCount}}}}'")
    check(f"F1 {eng} after docker restart", st.startswith("running") and dbs2.get(f"t-{eng}-r") == "running", f"{eng}: container {st}; musdash says {dbs2.get(f't-{eng}-r')}", "S2", evidence=shout(f"docker logs --tail 5 {cn} 2>&1"))
bad = {k: (before[k], after.get(k)) for k in before if before[k] == "running" and after.get(k) != "running"}
check("S17.10", not bad, f"musdash statuses re-converged: apps not running again: {bad}; dbs {dbs2}", "S2")
g = shout("curl -s -m5 -o /dev/null -w %{http_code} -H 'Host: t-web.168.235.65.204.sslip.io' http://127.0.0.1/")
check("S17.11", g == "200", f"app answers through the proxy after the restart: {g}", "S2")
