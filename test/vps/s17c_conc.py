from lib import *
import threading
c = owner_client(); st = state(); R = Client(token=ensure_tokens(c)["read_token"]); D = Client(token=ensure_tokens(c)["deploy_token"])
apps = [a for a in R.get("/api/v1/apps").json() if a["name"] in ("t-web", "t-who", "t-sink", "t-git", "t-static")]
ids = [a["id"] for a in apps]
print([a["name"] for a in apps])
before = {a: len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{a}/deployments").text))) for a in ids}
out = []
def fire(i):
    cl = Client(token=D.token); out.append((i, cl.post(f"/api/v1/apps/{ids[i % len(ids)]}/deploy")))
ts = [threading.Thread(target=fire, args=(i,)) for i in range(10)]
[t.start() for t in ts]; [t.join() for t in ts]
codes = [r.status for _, r in out]; depl = {}
for i, r in out:
    try: depl.setdefault(ids[i % len(ids)], set()).add(r.json().get("deployment_id"))
    except Exception: pass
time.sleep(5)
def all_done():
    for a in ids:
        s = R.get(f"/api/v1/apps/{a}/deployments?limit=3").json()
        s = s if isinstance(s, list) else s.get("deployments", [])
        if any(x.get("status") in ("queued", "running", "waiting") for x in s): return False
    return True
fin = wait_for(all_done, 600, 5)
after = {a: len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{a}/deployments").text))) for a in ids}
fails = {a: [x["status"] for x in (R.get(f"/api/v1/apps/{a}/deployments?limit=3").json() if isinstance(R.get(f"/api/v1/apps/{a}/deployments?limit=3").json(), list) else R.get(f"/api/v1/apps/{a}/deployments?limit=3").json().get("deployments", []))] for a in ids}
conts = {a: len(shout(f"docker ps --filter name=musdash-{a} --format '{{{{.Names}}}}'").split()) for a in ids}
check("S17.9", all(code == 202 for code in codes) and fin and all(v == 1 for v in conts.values()), f"10 simultaneous API deploys over {len(ids)} apps: statuses {sorted(set(codes))}; all finished={bool(fin)}; deployments per app {[after[a] - before[a] for a in ids]} (extra ones are coalesced 'waiting' repeats); containers per app {list(conts.values())}; latest statuses {list(fails.values())}", sev="S2")
