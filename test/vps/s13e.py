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
f = [x for x in forms if "t2-grp" in x["action"]][0]
b = n_deploys()
rr = c.post_form(f, follow=False)
import base64
ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", "")); print(base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)")
rr2 = c.post_form(f, follow=False)
ck = re.search(r"musdash_flash=([^;]*)", rr2.headers.get("set-cookie", "")); print(base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)")
time.sleep(40)
a = n_deploys(); print("deploy logs before/after", b, a)
check("S13.9b", a[0] - b[0] <= 1 and a[1] - b[1] <= 1 and (a[0] > b[0] or a[1] > b[1]), f"Deploy all queued each once; second click did not double-queue (new app logs {a[0]-b[0]}, service logs {a[1]-b[1]})")
