from lib_b import *
import threading
c = owner_client(); A = state()["b_app"]; H = f"t2-app.{HOST}.sslip.io"
n = shout(f"docker ps --format '{{{{.Names}}}}' | grep musdash-{A}")
# S17.6 disk pressure in the app's container (3 GB, root disk has ~9 GB free)
print(shout("df -h / | tail -1"))
shout(f"docker exec {n} sh -c 'dd if=/dev/zero of=/fill bs=1M count=3000 2>&1 | tail -1'", timeout=300)
print(shout("df -h / | tail -1"))
t0 = time.time()
ok = [Client().get("/healthz").status, owner_client().get("/", follow=True).status, Client(f"http://{HOST}", timeout=5).get("/", headers={"Host": H}).status]
print("while filled:", ok, round(time.time() - t0, 1), "s")
shout(f"docker exec {n} rm -f /fill")
check("S17.6", ok == [200, 200, 200], f"with 3 GB written into an app container (root disk {shout('df -h / | tail -1').split()[4]} used) musdash stays up: healthz/dashboard/proxy answer {ok}")
# S17.7 RSS under load
rss = lambda: {k: float(v) / 1024 for k, v in (l.split()[::-1][::-1][0:2] for l in [])} 
def rss():
    out = shout("ps -o rss=,args= -C musdash")
    d = {}
    for l in out.splitlines():
        r, _, a = l.strip().partition(" "); d["proxy" if "proxy" in a else "server"] = int(r) / 1024
    return d
base = rss(); print("idle", base)
stop = False; cnt = [0, 0]
def hammer():
    cl = Client(f"http://{HOST}", timeout=10)
    while not stop:
        try: r = cl.get("/", headers={"Host": H}); cnt[0] += 1
        except Exception: cnt[1] += 1
def dash():
    cl = owner_client()
    while not stop:
        try: cl.get("/", follow=True); cl.get("/servers", follow=True); cnt[0] += 1
        except Exception: cnt[1] += 1
ts = [threading.Thread(target=hammer) for _ in range(16)] + [threading.Thread(target=dash) for _ in range(3)]
[t.start() for t in ts]
peak = dict(base)
for i in range(12):
    time.sleep(2); r = rss(); peak = {k: max(peak[k], r[k]) for k in peak}
stop = True; [t.join() for t in ts]
time.sleep(5); end = rss()
print("peak", peak, "after", end, "requests ok/err", cnt)
check("S17.7", peak["server"] < 48 and peak["proxy"] < 32, f"RSS under {cnt[0]} requests in 24 s (16 proxy + 3 dashboard clients): idle {base}, peak server {peak['server']:.1f} MB proxy {peak['proxy']:.1f} MB, after {end}; errors {cnt[1]}")
