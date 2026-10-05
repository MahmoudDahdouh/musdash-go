from lib import *
c = owner_client(); who = state()["who"]; H = f"t-web.{HOST}.sslip.io"
for f in parse_forms(c.get(f"/apps/{who}/settings").text):
    if f["action"].startswith(f"/apps/{who}/domains/") and f["action"].endswith("/delete") and "/admin" not in f["action"]: pass
page = c.get(f"/apps/{who}/settings").text
rows = [f for f in parse_forms(page) if f["action"].startswith(f"/apps/{who}/domains/") and f["action"].endswith("/delete")]
for f in rows: c.post_form(f)
c.submit(f"/apps/{who}/settings", action=f"/apps/{who}/domains", host=H, path="/api", tls=False); time.sleep(3)
cl = Client(f"http://{H}")
res = {p: (cl.get(p, follow=False).status, "Hostname" in cl.get(p).text) for p in ["/api", "/api/", "/api/x", "/apix", "/ap", "/"]}
check("S6.6", res["/api"][1] and res["/api/x"][1] and not res["/apix"][1] and not res["/ap"][1] and not res["/"][1], f"path prefix matching on segment boundaries (status, reaches whoami): {res}", sev="S2")
