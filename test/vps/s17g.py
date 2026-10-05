from lib_b import *
import threading
c = owner_client(); A = state()["b_app"]
tok = ensure_tokens(c)["deploy_token"]
sq = lambda s: shout(f"python3 -c \"import sqlite3,sys;d=sqlite3.connect('file:/var/lib/musdash/musdash.db?mode=ro',uri=True);print(d.execute(sys.argv[1]).fetchall())\" \"{s}\" 2>&1")
b = sq(f"select count(*) from deployments where app_id='{A}'")
res = []
def go():
    r = Client(token=tok).request("POST", f"/api/v1/deploy?uuid={A}"); res.append((r.status, r.body[:80]))
ts = [threading.Thread(target=go) for _ in range(10)]
[t.start() for t in ts]; [t.join() for t in ts]
print(sorted(set((s, b_.decode()[:60]) for s, b_ in res)))
time.sleep(5)
wait_for(lambda: sq(f"select count(*) from deployments where app_id='{A}' and status in ('queued','running','waiting')") == "[(0,)]", 180, 4)
a = sq(f"select count(*) from deployments where app_id='{A}'"); st = sq(f"select status, count(*) from deployments where app_id='{A}' order by created_at desc limit 12")
rows = sq(f"select status from (select status from deployments where app_id='{A}' order by created_at desc limit 12)")
print("deployments", b, "->", a, rows)
cs = shout(f"docker ps -a --format '{{{{.Names}}}} {{{{.Status}}}}' | grep musdash-{A}")
n202 = sum(1 for s, _ in res if s == 202)
check("S17.9", all(s in (200, 202) for s, _ in res) and cs.count("\n") == 0 and shout(f"docker ps --filter name=musdash-{A} --format '{{{{.Status}}}}'").startswith("Up"), f"10 simultaneous API deploys: statuses {sorted(set(s for s,_ in res))} ({n202}x202); deployments {b}->{a}; containers afterwards: {cs!r}")
