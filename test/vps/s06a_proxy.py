from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; web = st["web"]
H = f"t-web.{HOST}.sslip.io"
from lib_d import *
r = new_app(c, "t-who", source="image", image="traefik/whoami:latest", port="80", domain=False)
who = re.search(r"/apps/([a-z2-7]{12})", r.url).group(1); save_state(who=who)
dep = re.search(r"/deployments/([a-z2-7]{12})", r.url).group(1)
check("S6.0", dep_wait(c, who, dep) == "success", "t-who (traefik/whoami) deployed")
def dom(app, host, **kw):
    return c.submit(f"/apps/{app}/settings", action=f"/apps/{app}/domains", host=host, **kw)
def routes_ready():
    time.sleep(2)

# S6.1 second domain on the same app
H2 = f"t-web2.{HOST}.sslip.io"; dom(web, H2, tls=False); routes_ready()
r1 = Client(f"http://{H}").get("/"); r2 = Client(f"http://{H2}").get("/")
check("S6.1", r1.status == 200 and r2.status == 200, f"both domains answer: {r1.status}/{r2.status}")

# S6.6 path route: /api on H goes to t-who, the rest stays on t-web
dom(who, H, path="/api", tls=False); routes_ready()
a = Client(f"http://{H}").get("/api/hello?x=1"); b = Client(f"http://{H}").get("/"); cc = Client(f"http://{H}").get("/apix"); d = Client(f"http://{H}").get("/api")
rec("S6.6", "PASS" if ("Hostname" in a.text and "nginx" in b.text.lower() and "Hostname" not in cc.text and "Hostname" in d.text) else "FAIL",
    f"/api/hello -> whoami={('Hostname' in a.text)}; / -> nginx={'nginx' in b.text.lower()}; /apix -> whoami={'Hostname' in cc.text} (should be False, {cc.status}); /api exact -> whoami={'Hostname' in d.text}",
    "" if ("Hostname" in a.text and "nginx" in b.text.lower() and "Hostname" not in cc.text) else "S1")
m = re.search(r"GET (\S+) HTTP", a.text); path_seen = m.group(1) if m else None
check("S6.6b", path_seen == "/api/hello?x=1", f"without strip-prefix the app sees the full path: {path_seen}")
# S6.7 strip prefix: replace the domain row with strip_prefix
rows = [f for f in parse_forms(c.get(f"/apps/{who}/settings").text) if f["action"].startswith(f"/apps/{who}/domains/") and f["action"].endswith("/delete")]
for f in rows: c.post_form(f)
dom(who, H, path="/api", tls=False, strip_prefix=True); routes_ready()
a = Client(f"http://{H}").get("/api/users/42?x=1"); m = re.search(r"GET (\S+) HTTP", a.text)
check("S6.7", m and m.group(1) == "/users/42?x=1", f"strip prefix: app sees {m.group(1) if m else a.text[:80]!r} for /api/users/42?x=1")
# S6.14 forwarded headers
hdr = dict(re.findall(r"^([A-Za-z\-]+): (.*)$", a.text, re.M))
rec("S6.14", "PASS" if hdr.get("X-Forwarded-For") and hdr.get("X-Forwarded-Proto") else "FAIL", f"forwarded headers seen by app: { {k: v for k, v in hdr.items() if k.startswith('X-')} }, Host={hdr.get('Host')}", "" if hdr.get("X-Forwarded-For") else "S3")
# S6.9 path canonicalisation vs guarded /admin: make t-who /admin password-protected
dom(who, H, path="/admin", tls=False, auth_user="alice", auth_password="Sup3rS3cret!"); routes_ready()
tests = {}
cl = Client(f"http://{H}")
for p in ["/admin", "/admin/", "/admin/x", "/Admin", "/ADMIN/x", "//admin", "/./admin", "/a/../admin", "/%61dmin", "/admin%2f", "/admin/..;/admin", "/admin%00", "/%2e/admin", "/admin?x=1", "/admin#f"]:
    r = cl.get(p, follow=False); tests[p] = (r.status, "Hostname" in r.text)
leaked = {p: v for p, v in tests.items() if v[1]}
check("S6.9", not leaked, f"guarded /admin: no path variant reaches the app unauthenticated. Leaks: {leaked}; all: {tests}", sev="S1")
auth = base64.b64encode(b"alice:Sup3rS3cret!").decode()
ok = cl.get("/admin/x", headers={"Authorization": "Basic " + auth}); bad = cl.get("/admin/x", headers={"Authorization": "Basic " + base64.b64encode(b"alice:wrong").decode()})
check("S6.8a", ok.status == 200 and "Hostname" in ok.text and bad.status == 401, f"password gate: right {ok.status}, wrong {bad.status}", sev="S1")
check("S6.8b", cl.get("/admin").status == 401 and "WWW-Authenticate" in str(cl.get("/admin").headers), "401 carries a WWW-Authenticate challenge")
rt = shout("cat /var/lib/musdash/proxy/routes.json")
check("S6.8c", "Sup3rS3cret" not in rt and ("$2" in rt), "route password stored only as a bcrypt hash in routes.json", sev="S1", evidence=rt[:300])
check("S6.8d", '"routes_v2"' in rt, "path/password routes are under routes_v2 (older proxies ignore them: unknown, not open)", sev="S2", evidence=rt[:200])
