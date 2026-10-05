from lib import *
c = owner_client(); tk = ensure_tokens(c); st = state()
R = Client(token=tk["read_token"]); D = Client(token=tk["deploy_token"]); web = st["web"]; who = st["who"]; pid = st["proj"]
dbid = "qfoenpmqqgvg"  # belongs to the other session's test project (read only)
# S14.1 every GET route
gets = ["/api/v1/me", "/api/v1/servers", "/api/v1/projects", "/api/v1/tags", "/api/v1/apps", f"/api/v1/apps/{web}", f"/api/v1/apps/{web}/deployments", f"/api/v1/apps/{web}/deployments?limit=2",
        "/api/v1/databases", "/api/v1/services"]
dep = re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{web}/deployments").text)[0]
gets += [f"/api/v1/deployments/{dep}"]
dbs = R.get("/api/v1/databases").json()
rec("S14.1x", "INFO", "databases JSON shape: " + json.dumps(dbs)[:300])
bodies = {}
for g in gets:
    r = R.get(g); bodies[g] = r
bad = {g: r.status for g, r in bodies.items() if r.status != 200 or not r.h("content-type").startswith("application/json")}
check("S14.1", not bad, f"{len(gets)} GET routes with a read token return 200 JSON; failures: {bad}")
lim = bodies[f"/api/v1/apps/{web}/deployments?limit=2"].json()
n = len(lim if isinstance(lim, list) else lim.get("deployments", []))
check("S14.1b", n == 2, f"?limit=2 returns 2 deployments (got {n})", sev="S3")
tag = R.get("/api/v1/apps?tag=nonexistent-tag").json(); rec("S14.1c", "INFO", "apps?tag= filter: " + json.dumps(tag)[:100])
ids = R.get("/api/v1/apps/zzzzzzzzzzzz"); check("S14.1d", ids.status == 404 and "error" in ids.json(), f"unknown app -> {ids.status} {ids.text[:60]}")

# S14.2 no secrets anywhere
SEC = ["S3cr3t-Value-4711", "Sup3rS3cret", "$2a$", "$2b$", "BEGIN ", "msd_", "master", "password"]
leaks = {}
for g, r in bodies.items():
    for s in SEC:
        if s.lower() in r.text.lower() and not (s == "password" and False): leaks.setdefault(g, []).append(s)
check("S14.2", not leaks, f"no secret-looking material in {len(bodies)} API responses: {leaks}", sev="S1")

# S14.3 scope
for m, p in [("POST", f"/api/v1/apps/{web}/deploy"), ("POST", f"/api/v1/apps/{web}/stop"), ("POST", "/api/v1/deploy?uuid=" + web), ("POST", f"/api/v1/databases/{dbid}/stop")]:
    r = R.request(m, p)
    check("S14.3" + str(["deploy", "stop", "deploy-multi", "dbstop"][[f"/api/v1/apps/{web}/deploy", f"/api/v1/apps/{web}/stop", "/api/v1/deploy?uuid=" + web, f"/api/v1/databases/{dbid}/stop"].index(p)]), r.status == 403, f"read token refused on {p}: {r.status}", sev="S1")
check("S14.3x", R.get(f"/api/v1/databases/{dbid}").status == 200 and D.get("/api/v1/databases").status == 200, "read and deploy tokens both read")

# S14.4 deploy and follow
r = D.post(f"/api/v1/apps/{web}/deploy"); j = r.json()
did = j.get("deployment", j).get("id") if isinstance(j.get("deployment", j), dict) else j.get("id")
check("S14.4a", r.status == 202 and did, f"POST deploy -> {r.status} {r.text[:120]}")
def done():
    s = D.get(f"/api/v1/deployments/{did}").json(); x = s.get("deployment", s).get("status"); return x if x in ("success", "failed") else None
res = wait_for(done, 200, 2); check("S14.4b", res == "success", f"deployment {did} followed to {res}")
# S14.5 multi
r = D.post(f"/api/v1/deploy?uuid={web},{who}"); rec("S14.5a", "PASS" if r.status in (200, 202) else "FAIL", f"deploy two uuids -> {r.status} {r.text[:200]}", "" if r.status in (200, 202) else "S2")
before = len(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{web}/deployments").text))
r = D.post(f"/api/v1/deploy?uuid={web},nonexistentid1")
time.sleep(2); after = len(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{web}/deployments").text))
check("S14.5b", r.status in (400, 404, 422) and before == after, f"one unknown id deploys nothing: {r.status} {r.text[:100]}; deployments {before}->{after}", sev="S2")
# S14.6 waiting: two quick POSTs; the second reports waiting and queues nothing more
wait_for(lambda: D.get(f"/api/v1/apps/{web}").json() and True, 5, 1)
r1 = D.post(f"/api/v1/apps/{web}/deploy"); r2 = D.post(f"/api/v1/apps/{web}/deploy"); r3 = D.post(f"/api/v1/apps/{web}/deploy")
js = [x.json() for x in (r1, r2, r3)]
rec("S14.6", "INFO", "three rapid deploys -> " + json.dumps([ (x.status, j) for x, j in zip((r1, r2, r3), js)])[:500])
ids = {(j.get("deployment", j) if isinstance(j.get("deployment", j), dict) else j).get("id") for j in js}
check("S14.6b", len(ids) <= 2, f"3 rapid deploy calls produced {len(ids)} distinct deployment ids (waiting one is reused)", sev="S3")
for i in ids: wait_for(lambda i=i: D.get(f"/api/v1/deployments/{i}").json().get("deployment", {}).get("status", D.get(f"/api/v1/deployments/{i}").json().get("status")) in ("success", "failed"), 120, 3)

# S14.7 auth failures
for label, cl in {"none": Client(), "garbage": Client(token="msd_" + "x" * 43), "wrong-prefix": Client(token="Bearer abc")}.items():
    r = cl.get("/api/v1/me"); check("S14.7" + label[0], r.status == 401 and "error" in r.json(), f"{label} token -> {r.status} {r.text[:60]}", sev="S2")
# S14.8 cookies vs tokens
r = Client(); r.cookies = dict(c.cookies); x = r.get("/api/v1/me")
check("S14.8a", x.status == 401, f"session cookie is not accepted by the API: {x.status}", sev="S1")
tp = Client(token=tk["deploy_token"]); y = tp.get("/projects/" + pid, follow=False); y2 = tp.get("/account", follow=False)
check("S14.8b", y.status in (302, 303, 401, 403) and y2.status in (302, 303, 401, 403), f"a token is not accepted by pages: {y.status}/{y2.status}", sev="S1")
# S14.11 CORS
x = R.request("GET", "/api/v1/me", headers={"Origin": "http://evil.example"}); o = R.request("OPTIONS", "/api/v1/me", headers={"Origin": "http://evil.example", "Access-Control-Request-Method": "GET"})
check("S14.11", "access-control-allow-origin" not in x.headers and "access-control-allow-origin" not in o.headers, f"no CORS allow headers (GET {x.status}, OPTIONS {o.status})", sev="S2")
