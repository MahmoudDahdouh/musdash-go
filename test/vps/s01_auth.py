from lib import *
OWNER = dict(name="Test Owner", email="owner@musdash.test", password="Owner-Pass-2026!x")

anon = Client()
r = anon.get("/")
check("S1.1", r.status in (302, 303) and r.location == "/setup", f"GET / unauthenticated -> {r.status} {r.location}")

# S1.2 setup validation
def setup(c, **kw):
    d = dict(OWNER); d.update(kw)
    return c.submit("/setup", action="/setup", **d)
c = Client()
bad = {"short password": dict(password="short"), "bad email": dict(email="notanemail"),
       "71+ chars": dict(password="x" * 80), "empty name": dict(name="")}
res = {}
for label, kw in bad.items():
    r = setup(c, **kw)
    res[label] = (r.status, "Create the owner account" in r.text or r.location == "/setup")
check("S1.2", all(v[1] for v in res.values()), "invalid setup input refused: " + json.dumps(res), evidence=res)
rr = setup(c, password="short")
rec("S1.2b", "INFO", "message for short password: " + re.findall(r'(?:alert|error|danger)[^>]*>([^<]{5,120})', rr.text)[:2].__repr__())

# S1.3 create owner
r = setup(c)
sess = {k: v for k, v in c.cookies.items()}
check("S1.3", "musdash_session" in " ".join(sess) or any("sess" in k for k in sess), f"owner created; cookies {list(sess)}; landed {r.status}", evidence=r.text[:300])
save_state(owner=OWNER)

# S1.4 replay
r2 = Client().submit("/setup", action="/setup", **dict(OWNER, email="second@musdash.test")) if False else None
c2 = Client(); rr = c2.get("/setup", follow=False)
f = Client()
page = f.get("/setup")
csrf = f.cookies.get("musdash_csrf", "")
r = f.post("/setup", dict(_csrf=csrf, name="Intruder", email="intruder@musdash.test", password="Intruder-Pass-2026!"))
check("S1.4", not (r.status == 200 and "Create the owner" in r.text) and not any("sess" in k for k in f.cookies), f"second POST /setup -> {r.status} {r.location}; GET /setup -> {page.status} {page.location}")
