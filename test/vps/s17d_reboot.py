from lib import *
import subprocess
c = owner_client(); R = Client(token=ensure_tokens(c)["read_token"])
def snap():
    apps = {a["name"]: a["status"] for a in R.get("/api/v1/apps").json()}; dbs = {d["name"]: d["status"] for d in R.get("/api/v1/databases").json()}; svcs = {s["name"]: s["status"] for s in R.get("/api/v1/services").json()}
    return apps, dbs, svcs
a0, d0, s0 = snap(); n0 = shout("docker ps -q | wc -l"); print("before:", n0, "containers;", sum(v == "running" for v in a0.values()), "apps running;", d0, s0)
time.sleep(140)
t0 = time.time(); sh("systemctl reboot", timeout=30) if False else subprocess.run([SSH, "nohup sh -c 'sleep 1; systemctl reboot' >/dev/null 2>&1 &"], capture_output=True, timeout=30)
time.sleep(25)
up = None
for i in range(40):
    try:
        rc, o = sh("uptime -p; systemctl is-active musdash-server musdash-proxy docker", timeout=20)
        if rc == 0 and "up" in o: up = o; break
    except Exception: pass
    time.sleep(8)
print("ssh back after %.0fs: %s" % (time.time() - t0, (up or "").strip().replace("\n", " ")))
time.sleep(60)
dash = wait_for(lambda: Client().get("/healthz").status == 200, 120, 3)
c = owner_client(); R = Client(token=ensure_tokens(c)["read_token"])
n1 = shout("docker ps -q | wc -l"); a1, d1, s1 = snap()
check("S17.10a", dash and "active" in (up or ""), f"after `systemctl reboot` the VPS is back in {time.time()-t0:.0f}s, docker/musdash-server/musdash-proxy active, dashboard answers", sev="S1")
lost = [k for k, v in {**a0, **d0, **s0}.items() if v == "running" and {**a1, **d1, **s1}.get(k) != "running"]
check("S17.10b", not [k for k in lost if k not in ("t2-redis",)], f"every app/database/service that was running is running again after the reboot; not running: {lost}; containers before {n0}, after {n1}", sev="S2")
live = {}
for h in ("t-web", "t-git", "t-who", "t-static"):
    try: live[h] = Client(f"http://{h}.{HOST}.sslip.io").get("/").status
    except Exception as e: live[h] = repr(e)[:30]
check("S17.10c", all(v == 200 for v in live.values()), f"apps answer through the proxy after the reboot: {live}", sev="S1")
check("S17.10d", c.get("/account").status == 200 and len(R.get("/api/v1/apps").json()) >= 5, "SQLite, sessions and API tokens intact after the reboot", sev="S1")
print(shout("free -m | sed -n 2,3p; cat /etc/docker/daemon.json; swapon --show | tail -1; docker ps -a --format '{{.Names}} {{.Status}}' | grep -E 'Restarting|Exited' | head; journalctl -u musdash-server -b --no-pager | grep -iE 'error|panic|fail' | head -5 | cut -c1-200"))
rec("S17.10e", "INFO", "after reboot: " + str(a1) + " " + str(d1))
