from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; R = Client(token=ensure_tokens(c)["read_token"])
def app(name, envid=env, image="nginx:alpine", deploy=False):
    r = c.submit(f"/projects/{pid}/apps/new?env={envid}", action=f"/projects/{pid}/apps", name=name, image=image, port="80", domain="", deploy=deploy)
    m = re.search(r"/apps/([a-z2-7]{12})", r.url or ""); return (m.group(1) if m else None), r
def db(name, envid=env):
    r = c.submit(f"/projects/{pid}/databases/new?env={envid}&engine=redis", action=f"/projects/{pid}/databases", name=name)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url or ""); return (m.group(1) if m else None), r
def svc(name, envid=env):
    r = c.submit(f"/projects/{pid}/services/new?env={envid}&template=custom", action=f"/projects/{pid}/services", name=name, compose="services:\n  a:\n    image: nginx:alpine\n", variables="", connect_env=False, deploy=False)
    m = re.search(r"/services/([a-z2-7]{12})", r.url or ""); return (m.group(1) if m else None), r
a1, _ = app("t-ns")
check("S4.5a", a1 is not None, "app t-ns created")
d1, rd = db("t-ns"); check("S4.5b", d1 is None, f"a database cannot take an app's name in the same environment: {flash(rd)[-90:]!r}", sev="S3")
s1, rs = svc("t-ns"); check("S4.5c", s1 is None, f"a service cannot take it either: {flash(rs)[-90:]!r}", sev="S3")
a2, ra = app("t-ns"); check("S4.5d", a2 is None, "a second app with the same name is refused", sev="S3")
# another environment: same name allowed
c.submit(f"/projects/{pid}/settings", action=f"/projects/{pid}/environments", name="staging")
envs = list(dict.fromkeys(re.findall(r"env=([a-z2-7]{12})", c.get(f"/projects/{pid}").text))); e2 = [e for e in envs if e != env][0]
a3, rb = app("t-ns", envid=e2); check("S4.5e", a3 is not None, "the same name in another environment is fine", sev="S3")
# service-internal name vs app: compose service named like an app, no environment network -> allowed (own network)
# S5.17 delete: deploy an app with a volume and domain, delete it
x, _ = app("t-del", deploy=False)
c.submit(f"/apps/{x}/storage", action=f"/apps/{x}/storage", kind="volume", source="t-del-data", target="/data")
c.submit(f"/apps/{x}/settings", action=f"/apps/{x}/domains", host=f"t-del.{HOST}.sslip.io", tls=False)
d = deploy(c, x); dep_wait(c, x, d, 200)
ok_before = Client(f"http://t-del.{HOST}.sslip.io").get("/").status
af = [f for f in parse_forms(c.get(f"/apps/{x}/settings").text) if f["action"] == f"/apps/{x}/delete"][0]
wrong = c.post_form(af, confirm="nope"); still = bool([a for a in R.get("/api/v1/apps").json() if a["id"] == x])
check("S5.17a", still, "deleting needs the app's name typed; a wrong name deletes nothing", sev="S2")
c.post_form(af, confirm="t-del"); time.sleep(8)
gone = not [a for a in R.get("/api/v1/apps").json() if a["id"] == x]
left = shout(f"docker ps -aq --filter name=musdash-{x} | wc -l"); vol = shout("docker volume ls -q | grep -c t-del-data || true"); envf = shout(f"ls /var/lib/musdash/apps/{x} 2>&1 | head -2")
route = shout("grep -c t-del /var/lib/musdash/proxy/routes.json || true")
check("S5.17b", gone and left.strip() == "0" and "No such" in envf and route.strip() == "0" and ok_before == 200, f"app removed: containers {left.strip()}, env dir {envf!r}, route entries {route.strip()}; volume kept as documented: {vol.strip()}", sev="S2")
check("S5.17c", Client(f"http://t-del.{HOST}.sslip.io").get("/").status == 404, "its domain stops answering (proxy 404)", sev="S2")
# S4.6 delete project cascade
r = c.submit("/projects/new", action="/projects", name="t-cascade", description="")
p2 = re.search(r"/projects/([a-z2-7]{12})", r.url or "").group(1); e_c = re.findall(r"env=([a-z2-7]{12})", r.text)[0]
rr = c.submit(f"/projects/{p2}/apps/new?env={e_c}", action=f"/projects/{p2}/apps", name="t-casc-app", image="nginx:alpine", port="80", domain="", deploy=True)
ca = re.search(r"/apps/([a-z2-7]{12})", rr.url).group(1); cd = re.search(r"/deployments/([a-z2-7]{12})", rr.url).group(1); dep_wait(c, ca, cd, 200)
rr = c.submit(f"/projects/{p2}/databases/new?env={e_c}&engine=redis", action=f"/projects/{p2}/databases", name="t-casc-db")
cdb = re.search(r"/databases/([a-z2-7]{12})", rr.url).group(1)
rr = c.submit(f"/projects/{p2}/services/new?env={e_c}&template=custom", action=f"/projects/{p2}/services", name="t-casc-svc", compose="services:\n  a:\n    image: nginx:alpine\n", variables="", connect_env=False, deploy=True)
csv = re.search(r"/services/([a-z2-7]{12})", rr.url).group(1)
wait_for(lambda: R.get(f"/api/v1/databases/{cdb}").json().get("status") == "running" and R.get(f"/api/v1/services/{csv}").json().get("status") == "running" or None, 300, 5)
ids = (ca, cdb, csv); before = shout("docker ps -q --filter 'name=musdash-' | wc -l"); nets_before = shout("docker network ls --format '{{.Name}}' | grep -c musdash-")
pf = [f for f in parse_forms(c.get(f"/projects/{p2}/settings").text) if f["action"] == f"/projects/{p2}/delete"][0]; c.post_form(pf, confirm="t-cascade"); time.sleep(20)
rest = shout("docker ps -aq --filter name=musdash-%s --filter name=musdash-%s --filter name=musdash-%s | wc -l" % ids)
nets = shout("docker network ls --format '{{.Name}}' | grep -c musdash-" + e_c + " || true")
api = [x for x in R.get("/api/v1/apps").json() if x["id"] == ca] + [x for x in R.get("/api/v1/databases").json() if x["id"] == cdb] + [x for x in R.get("/api/v1/services").json() if x["id"] == csv]
check("S4.6", rest.strip() == "0" and not api and nets.strip() == "0", f"deleting a project removes its app, database and service containers ({rest.strip()} left), their records ({len(api)} left) and the environment network ({nets.strip()} left)", sev="S2")
# cleanup t-ns resources and staging env
for x_ in [a1, a3]:
    if x_: 
        f = [f for f in parse_forms(c.get(f"/apps/{x_}/settings").text) if f["action"] == f"/apps/{x_}/delete"][0]; c.post_form(f, confirm="t-ns")
