exec(open("s13b.py").read().split("teamv = getvars")[0])
S = state()["b_svc_vars"]
for path in [f"/apps/{A}/tags", f"/services/{S}/tags"]:
    base = path.rsplit("/", 1)[0] + "/settings"
    f = [x for x in parse_forms(c.get(base, follow=True).text) if x["action"] == path][0]
    c.post_form(f, follow=False, tags="t2-grp")
r = c.get("/tags", follow=True); t = flash(r)
i = t.find("t2-grp"); print(t[i-30:i+260])
forms = [f for f in parse_forms(r.text) if "t2-grp" in f["action"] or "deploy" in f["action"]]
print([(f["action"]) for f in forms])
check("S13.9a", "t2-grp" in t and "t2-app" in t and "t2-svcvars" in t, "Tags page lists the tag with both the app and the service")
def n_deploys(): return int(shout("ls /var/lib/musdash/logs/deployments | wc -l")), int(shout("ls /var/lib/musdash/logs/services | wc -l"))
# Deploy all left the tag's page on 2026-10-07: the tag is deployed through the API.
D = Client(token=ensure_tokens(c)["deploy_token"])
b = n_deploys()
rr = D.post("/api/v1/deploy?tag=t2-grp"); print(rr.status, rr.text[:200])
rr2 = D.post("/api/v1/deploy?tag=t2-grp"); print(rr2.status, rr2.text[:200])
time.sleep(40)
a = n_deploys(); print("deploy logs before/after", b, a)
check("S13.9b", a[0] - b[0] <= 1 and a[1] - b[1] <= 1 and (a[0] > b[0] or a[1] > b[1]), f"deploying the tag through the API queued each once; the second call did not double-queue (new app logs {a[0]-b[0]}, service logs {a[1]-b[1]})")
