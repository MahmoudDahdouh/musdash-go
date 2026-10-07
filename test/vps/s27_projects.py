"""Projects and environments on the redesigned UI."""
from lib_d import *
c = owner_client()
def pid_of(name):
    r = c.get("/projects", follow=True)
    m = re.search(r'/projects/([a-z2-7]{12})[^<]*(?:<[^>]+>\s*)*' + re.escape(name), r.text); return m.group(1) if m else None
r = c.submit("/projects", action="/projects", name="t-proj-one", description="first test project")
p = re.search(r"/projects/([a-z2-7]{12})", r.url).group(1)
check("S4.1 create project", bool(p) and "t-proj-one" in flash(c.get("/projects")), f"project {p} created and listed")
page = c.get(f"/projects/{p}", follow=True)
envs = re.findall(r"/projects/%s/e/([a-z2-7]{12})" % p, page.text)
check("S4.2 default environment", bool(envs) and "production" in flash(page).lower(), f"a 'production' environment exists ({envs[:1]})")
hostile = {"empty": "", "script": "<script>alert(1)</script>", "long": "x" * 300, "newline": "a\nb", "emoji/unicode": "项目 🚀"}
res = {}
for k, nm in hostile.items():
    rr = c.post("/projects", dict(_csrf=csrf_of(c, "/projects"), name=nm, description=""), follow=False)
    res[k] = rr.status
    listing = c.get("/projects", follow=True).text
    if "<script>alert(1)</script>" in listing: res[k] = "UNESCAPED"
check("S4.4 hostile project names", "UNESCAPED" not in res.values() and res["empty"] == 422 and res["long"] == 422, f"statuses {res}; markup is escaped on the list", "S2")
# a second environment, then namespaces
er = c.submit(f"/projects/{p}/settings", action=f"/projects/{p}/environments", name="staging")
envs2 = re.findall(r"/projects/%s/e/([a-z2-7]{12})" % p, c.get(f"/projects/{p}/settings", follow=True).text + er.text + er.url)
check("S4.3 second environment", len(set(envs2)) >= 2, f"environments of the project: {sorted(set(envs2))}")
e2 = [e for e in dict.fromkeys(envs2) if e != envs[0]][0]
# the same app name may exist in two environments, not twice in one
def mk(env, name):
    r, forms = c.forms(f"/projects/{p}/e/{env}/apps/new"); f = c.find_form(forms, f"/projects/{p}/e/{env}/apps")
    return c.post_form(f, name=name, image="nginx:alpine", port="80", domain="", deploy=False)
a1 = mk(envs[0], "t-same"); a2 = mk(e2, "t-same"); a3 = mk(envs[0], "t-same")
check("S4.5 names unique per environment", "/apps/" in a1.url and "/apps/" in a2.url and a3.status == 422, f"same name: env1 ok={'/apps/' in a1.url}, env2 ok={'/apps/' in a2.url}, env1 again -> {a3.status}")
# deletion asks for the name
pg = c.get(f"/projects/{p}/settings", follow=True).text
dels = [f for f in parse_forms(pg) if f["action"] == f"/projects/{p}/delete"]
rec("S4.6 delete asks for the name", "PASS" if dels and any(x["name"] == "confirm" for x in dels[0]["fields"]) else "FAIL", f"delete form with a 'confirm' field: {bool(dels)}", "" if dels else "S3")
if dels:
    bad = c.post_form(dels[0], confirm="wrong"); still = pid_of("t-proj-one") is not None or p in c.get("/projects").text
    good = c.post_form(dels[0], confirm="t-proj-one")
    gone = p not in c.get("/projects").text
    check("S4.6b delete with the name removes the project and its apps", still and gone, f"wrong name kept it ({still}); right name removed it ({gone}); containers left for its apps: {shout('docker ps -aq --filter name=musdash-' + 'zzz | wc -l')}", "S2")
