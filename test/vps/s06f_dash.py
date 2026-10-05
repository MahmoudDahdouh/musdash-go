from lib import *
c = owner_client(); web = state()["web"]
D = f"dash.{HOST}.sslip.io"
r = c.submit("/settings", action="/settings", instance_domain=D, acme_email="")
rec("S6.13a", "INFO", f"settings saved: {r.status} {flash(r)[-200:]}")
time.sleep(3)
dc = Client(f"http://{D}")
r = dc.get("/login", follow=False)
check("S6.13b", r.status in (200, 303, 308, 301) , f"dashboard reachable on its own domain via the proxy: {r.status} {r.location}")
# an app cannot take this host (or a path under it)
for path in ("", "/x"):
    try:
        rr = c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/domains", host=D, path=path, tls=False)
        txt = flash(rr)
        listed = D in re.sub(r"<[^>]+>", " ", c.get(f"/apps/{web}/settings").text).split("Domains")[-1][:600]
        check("S6.13c" + ("b" if path else "a"), not listed, f"app refused the dashboard's own domain with path {path!r}: {txt[-120:]!r}", sev="S1")
    except Exception as e:
        rec("S6.13c", "FAIL", repr(e), "S2")
# secret cookie scope: the session cookie is not accepted from a different Host header?
# revert
c.submit("/settings", action="/settings", instance_domain="", acme_email="")
time.sleep(2)
r = Client(f"http://{D}").get("/login", follow=False)
check("S6.13d", r.status == 404, f"after clearing the setting the dashboard domain stops routing: {r.status}", sev="S3")
