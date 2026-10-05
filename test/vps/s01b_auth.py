from lib import *
o = state()["owner"]

# S1.5 logout / login, old cookie dead
c, r = login(o["email"], o["password"])
old = dict(c.cookies)
check("S1.5a", c.get("/account").status == 200, "login works")
ghost = Client(); ghost.cookies = dict(old)
c.submit("/account", action="/logout")
r = ghost.get("/account")
check("S1.5b", r.status in (302, 303, 401, 403) and "Account" not in r.text[:0], f"old session cookie after logout -> {r.status} {r.location}", sev="S1")

# S1.6 bad creds are indistinguishable
def attempt(email, pw):
    c = Client(); t = time.time()
    r = c.submit("/login", action="/login", email=email, password=pw)
    return r.status, re.sub(r"\s+", " ", flash(r))[:400], time.time() - t, c
a = attempt(o["email"], "wrong-password-1"); b = attempt("nobody@musdash.test", "wrong-password-1")
same = (a[0], re.sub(r"[0-9a-z]{20,}", "", a[1])) == (b[0], re.sub(r"[0-9a-z]{20,}", "", b[1]))
check("S1.6a", same, f"known vs unknown email: {a[0]}/{b[0]}; same text: {same}", sev="S3", evidence=(a[1], b[1]))
check("S1.6b", abs(a[2] - b[2]) < 0.4, f"timing known {a[2]:.2f}s vs unknown {b[2]:.2f}s", sev="S3")
check("S1.6c", not any("session" in k for k in a[3].cookies), "no session cookie after wrong password")

# S1.8 CSRF
c = owner_client()
r0 = c.get("/account")
tests = {}
for path, data in [("/account/profile", dict(name="x", email="owner@musdash.test")), ("/projects", dict(name="csrf-test", description="")), ("/logout", {}),
                   ("/team/invitations", dict(email="a@b.test", role="member"))]:
    tests[path + " no token"] = c.post(path, data).status
    tests[path + " bad token"] = c.post(path, dict(data, _csrf="A" * 32)).status
    other = Client(); other.get("/login")
    tests[path + " other session's cookie token"] = c.post(path, dict(data, _csrf=other.cookies.get("musdash_csrf", "x"))).status
check("S1.8", all(v == 403 for v in tests.values()), "POST without/with wrong CSRF token -> " + json.dumps(tests), sev="S1")
# header-only attempts
r = c.post("/projects", dict(name="csrf-hdr"), headers={"X-CSRF-Token": c.cookies.get("musdash_csrf", "")})
rec("S1.8b", "PASS" if r.status == 403 else "INFO", f"CSRF header only (no form field) -> {r.status}")
r = c.post("/projects", dict(name="csrf-xorigin", _csrf="x"), headers={"Origin": "http://evil.example"})
check("S1.8c", r.status == 403, f"cross-origin POST -> {r.status}")

# S1.9 cookie flags
raw = Client()
conn = raw.conn(); conn.request("GET", "/login"); rr = conn.getresponse(); rr.read()
cookies = [v for k, v in rr.getheaders() if k.lower() == "set-cookie"]
c2 = Client(); c2.get("/login")
conn = c2.conn(); 
import urllib.parse
body = urllib.parse.urlencode(dict(_csrf=c2.cookies["musdash_csrf"], email=o["email"], password=o["password"]))
conn.request("POST", "/login", body=body, headers={"Cookie": "musdash_csrf=" + c2.cookies["musdash_csrf"], "Content-Type": "application/x-www-form-urlencoded"})
rr = conn.getresponse(); rr.read()
cookies += [v for k, v in rr.getheaders() if k.lower() == "set-cookie"]
sess = [x for x in cookies if "session" in x]
check("S1.9", sess and all("HttpOnly" in x and "SameSite" in x for x in sess + cookies), "cookies: " + " | ".join(cookies)[:400], sev="S2")
rec("S1.9b", "INFO", "Secure flag over plain HTTP is absent by design?: " + str(["Secure" in x for x in cookies]))

# S1.10 headers
r = c.get("/projects/new")
h = r.headers
csp = h.get("content-security-policy", "")
check("S1.10", "script-src 'self'" in csp and "unsafe-inline" not in csp.split("style-src")[0] and h.get("x-frame-options") == "DENY" and h.get("x-content-type-options") == "nosniff" and h.get("referrer-policy"),
      f"CSP/XFO/nosniff/referrer present; cache-control={h.get('cache-control')}", evidence=json.dumps(dict(h))[:600])
check("S1.10b", "no-store" in h.get("cache-control", ""), f"signed-in pages Cache-Control: {h.get('cache-control')}", sev="S3")

# S1.11 static
r = c.get("/")
m = re.search(r'/static/app\.css\?v=\w+', r.text); css = m.group(0)
r = Client().request("GET", css, headers={"Accept-Encoding": "gzip"})
check("S1.11a", "immutable" in r.h("cache-control") and r.h("content-encoding") == "gzip", f"static cache: {r.h('cache-control')}, enc {r.h('content-encoding')}")
trav = {}
for p in ["/static/../etc/passwd", "/static/%2e%2e/%2e%2e/etc/passwd", "/static/..%2f..%2fetc/passwd", "/static//etc/passwd", "/static/%00", "/static/..\\..\\x"]:
    rr = Client().get(p); trav[p] = (rr.status, b"root:" in rr.body)
check("S1.11b", not any(v[1] for v in trav.values()) and all(v[0] in (400, 404, 301) for v in trav.values()), "traversal: " + json.dumps(trav), sev="S1")

# S1.12 unknown route / method
a = c.get("/nope-" + "x" * 5); b = c.request("PUT", "/projects"); d = c.request("DELETE", "/")
check("S1.12", a.status == 404 and b.status in (404, 405) and d.status in (404, 405) and "goroutine" not in a.text + b.text + d.text and "runtime error" not in a.text,
      f"404 {a.status}, PUT {b.status}, DELETE {d.status}")
big = c.request("GET", "/" + "a" * 70000)
rec("S1.12b", "PASS" if big.status in (404, 414, 431, 400) else "FAIL", f"70 KB URL -> {big.status}")
