from lib import *
st = state(); web = st["web"]; D = Client(token=st["deploy_token"]); R = Client(token=st["read_token"])
r = D.post(f"/api/v1/apps/{web}/deploy"); did = r.json()["deployment_id"]
seen = []
def done():
    s = D.get(f"/api/v1/deployments/{did}").json(); seen.append(s.get("status")); return s.get("status") if s.get("status") in ("success", "failed") else None
res = wait_for(done, 200, 2)
check("S14.4b", res == "success", f"deployment {did} followed queued -> ... -> {res}; statuses seen {list(dict.fromkeys(seen))}")
# stop and start via API
r = D.post(f"/api/v1/apps/{web}/stop"); time.sleep(4)
check("S14.4c", r.status in (200, 202) and "Stopped" in re.sub("<[^>]+>", " ", owner_client().get(f"/apps/{web}").text), f"API stop -> {r.status} {r.text[:80]}")
r = D.post(f"/api/v1/apps/{web}/deploy"); wait_for(lambda: D.get(f"/api/v1/deployments/{r.json()['deployment_id']}").json().get("status") == "success", 120, 3)
# S14.9 rate limit: 120/min per token
codes = {}; ra = None
t0 = time.time()
for i in range(135):
    x = R.get("/api/v1/me"); codes[x.status] = codes.get(x.status, 0) + 1
    if x.status == 429 and ra is None: ra = x.h("retry-after"), x.text[:80]
check("S14.9", 429 in codes and codes.get(200, 0) <= 125 and ra and ra[0], f"135 calls in {time.time()-t0:.0f}s: {codes}; first 429 Retry-After={ra}", sev="S2")
