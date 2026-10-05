from lib import *
H = f"t-web.{HOST}.sslip.io"
cl = Client(f"http://{H}")
# the rows from s06a: /api (strip) and /admin (password) both exist now
r = cl.get("/api", follow=False); rec("S6.6c", "INFO", f"GET /api (exact prefix, no slash) -> {r.status} {r.location!r} whoami={'Hostname' in r.text}")
r2 = cl.get("/api", follow=True) if False else None
r = cl.get("/admin"); check("S6.8b", "www-authenticate" in r.headers, f"401 carries a WWW-Authenticate challenge: {r.h('www-authenticate')!r}", sev="S2")
ok = Client(f"http://{H}").get("/api"); rec("S6.6d", "INFO", f"with strip-prefix on, GET /api -> {ok.status}; body starts {ok.text[:60]!r}")
