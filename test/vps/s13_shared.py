from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; web = st["web"]; who = st["who"]
sid = "ttd6yamy3cmb"
c.submit("/team/variables", action="/team/variables", vars="TEAMV=team-val-1\n")
c.submit(f"/projects/{pid}/variables", action=f"/projects/{pid}/variables", vars="PROJV=proj-val-2\n")
c.submit(f"/environments/{env}/variables", action=f"/environments/{env}/variables", vars="ENVV=env-val-3\n")
c.submit(f"/servers/{sid}/variables", action=f"/servers/{sid}/variables", vars="SRVV=srv-val-4\n")
def deploy_wait(app):
    d = deploy(c, app); return d, dep_wait(c, app, d)
def cenv(app):
    cid = shout(f"docker ps -q --filter name=musdash-{app} | head -1"); return shout(f"docker inspect {cid} --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}'")
VARS = "A_TEAM={{team.TEAMV}}\nA_PROJ=pre-{{project.PROJV}}-post\nA_ENV={{environment.ENVV}}\nA_SRV={{server.SRVV}}\nA_ESC=\\{{environment.ENVV}}\nA_PLAIN=just text\nA_UNKNOWN_STYLE={{ .Name }}\n"
c.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars=VARS)
d, res = deploy_wait(web); e = cenv(web)
check("S13.1", res == "success" and "A_TEAM=team-val-1" in e and "A_PROJ=pre-proj-val-2-post" in e and "A_ENV=env-val-3" in e and "A_SRV=srv-val-4" in e, f"four scopes expand at deploy ({res}): " + " | ".join(l for l in e.splitlines() if l.startswith("A_")), sev="S2")
check("S13.3", "A_ESC={{environment.ENVV}}" in e, "backslash escape reaches the app literally: " + str([l for l in e.splitlines() if l.startswith("A_ESC")]), sev="S3")
rn = c.submit("/team/variables", action="/team/variables", vars="TEAMV=team-val-1\nNESTED={{team.TEAMV}}\n")
check("S13.4", rn.status == 422, f"a shared variable whose value names another is refused on save: {rn.status}", sev="S3")
check("S13.3b", "A_UNKNOWN_STYLE={{ .Name }}" in e, "template-engine style `{{ .Name }}` left alone: " + str([l for l in e.splitlines() if l.startswith("A_UNKNOWN")]), sev="S3")
stored = c.get(f"/apps/{web}/environment").text
check("S13.2", "{{team.TEAMV}}" in re.sub("&#\\d+;|&lt;|&gt;", "", stored.replace("&#123;", "{").replace("&#125;", "}")) and "team-val-1" not in re.search(r"<textarea[^>]*>(.*?)</textarea>", stored, re.S).group(1), "stored variable keeps the {{name}}, not the value", sev="S3")
# S13.6 no inheritance
ew = cenv(who) if shout(f"docker ps -q --filter name=musdash-{who}") else ""
check("S13.6", "TEAMV" not in ew and "team-val-1" not in ew and "ENVV" not in ew, f"a container that named nothing sees no shared variable (t-who env: {[l.split('=')[0] for l in ew.splitlines()]})", sev="S2")
# S13.5 missing name
c.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars=VARS + "BAD={{team.DOESNOTEXIST}}\n")
page = re.sub("<[^>]+>", " ", c.get(f"/apps/{web}/environment").text)
rec("S13.5a", "PASS" if "DOESNOTEXIST" in page else "FAIL", "saving warns about an unknown shared name: " + re.sub(r"\s+", " ", page[page.find("DOESNOTEXIST")-120: page.find("DOESNOTEXIST")+80]), "" if "DOESNOTEXIST" in page else "S3")
before = shout(f"docker ps --format '{{{{.Names}}}}' | grep musdash-{web}")
d, res = deploy_wait(web); log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{web}/deployments/{d}").text))
after = shout(f"docker ps --format '{{{{.Names}}}}' | grep musdash-{web}")
check("S13.5b", res == "failed" and "DOESNOTEXIST" in log and before == after, f"deploy naming a missing variable fails and names it ({res}); old container kept: {before == after}; log: {log[150:420]}", sev="S2")
c.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars=VARS)

# S13.7 roles: admin2 is a Member now
m = state()["users"]; PW = state()["user_pw"]
mc, _ = login("admin2@musdash.test", PW)
tv = mc.get("/team/variables"); body = tv.text
check("S13.7a", tv.status == 200 and "team-val-1" not in body, f"Member sees the team variables page without values (status {tv.status})", sev="S2")
check("S13.7b", mc.post("/team/variables", dict(_csrf=csrf_of(mc), vars="X=1")).status == 403, "Member cannot change team variables", sev="S1")
sv = mc.get(f"/servers/{sid}/variables"); check("S13.7c", "srv-val-4" not in sv.text, f"Member sees server variables without values ({sv.status})", sev="S2")
# Member can name a project variable and change it
mp = mc.submit(f"/projects/{pid}/variables", action=f"/projects/{pid}/variables", vars="PROJV=proj-val-2\nMEMBER_ADDED=1\n")
check("S13.7d", "MEMBER_ADDED" in mc.get(f"/projects/{pid}/variables").text, "Member can change project variables")
mc2 = mc.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars=VARS)
# S13.8 service variables expand (needs a service: done in S10)
# S13.9 tags
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/tags", tags="nightly t-group")
c.submit(f"/apps/{who}/settings", action=f"/apps/{who}/tags", tags="nightly")
tp = c.get("/tags"); check("S13.9a", "nightly" in tp.text and "t-group" in tp.text, "Tags page lists the tags")
tg = c.get("/tags/nightly"); fm = [f for f in parse_forms(tg.text) if f["action"] == "/tags/nightly/deploy"]
check("S13.9b", len(fm) == 1, "tag page has Deploy all")
r1 = c.post_form(fm[0]); r2 = c.post_form([f for f in parse_forms(c.get("/tags/nightly").text) if f["action"] == "/tags/nightly/deploy"][0])
n = shout(f"journalctl -u musdash-server --since '-1 min' --no-pager | grep -ci 'deploy' || true")
rec("S13.9c", "INFO", "deploy-all twice: " + flash(r1)[-120:] + " || " + flash(r2)[-120:])
a = Client(token=state()["read_token"]).get("/api/v1/apps?tag=nightly").json()
check("S13.9d", {x["id"] for x in a} == {web, who}, f"API ?tag=nightly returns both apps ({[x['name'] for x in a]})")
time.sleep(30)
deps = len(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{who}/deployments").text))
rec("S13.9e", "INFO", f"t-who has {deps} deployments after two Deploy-all clicks (one is the initial)")
