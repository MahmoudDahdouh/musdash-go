from lib import *
import statistics
o = state()["owner"]
def t(email):
    c = Client(); c.get("/login")
    t0 = time.time(); c.post("/login", dict(_csrf=c.cookies["musdash_csrf"], email=email, password="wrong-password-1")); return time.time() - t0
ks, us = [], []
for i in range(6):
    ks.append(t(o["email"])); us.append(t("nobody%d@musdash.test" % i))
mk, mu = statistics.median(ks), statistics.median(us)
rec("S1.6b", "PASS" if abs(mk - mu) < 0.35 else "FAIL", f"median wrong-password timing known {mk:.2f}s vs unknown {mu:.2f}s (n=6 each); samples known={['%.2f'%x for x in ks]} unknown={['%.2f'%x for x in us]}", "" if abs(mk-mu) < 0.35 else "S3")

# S1.11b: follow the redirects the mux makes for cleaned paths and make sure nothing sensitive is served
c = Client(); leak = {}
for p in ["/static/../etc/passwd", "/static//etc/passwd", "/static/%2e%2e/%2e%2e/etc/passwd", "/static/..%2f..%2fetc/passwd", "/static/../../../../etc/passwd", "/static/....//....//etc/passwd"]:
    r = c.get(p, follow=True); leak[p] = (r.status, b"root:" in r.body)
check("S1.11b", not any(v[1] for v in leak.values()), "traversal incl. redirects followed: " + json.dumps(leak), sev="S1")

# S1.12 with a valid CSRF token for PUT/DELETE
oc = owner_client(); tok = oc.cookies["musdash_csrf"]
a = oc.request("PUT", "/projects", data=dict(_csrf=tok)); b = oc.request("DELETE", "/", data=dict(_csrf=tok))
check("S1.12", a.status in (404, 405) and b.status in (404, 405), f"PUT with token {a.status}, DELETE with token {b.status} (403 without a token is CSRF running first)", sev="S4")
