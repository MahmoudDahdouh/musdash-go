from lib_b import *
m, _ = login("t2-member@musdash.test", "T2-Member-Pass-2026!x")
oc = owner_client()
def getv(path):
    f = [x for x in parse_forms(oc.get(path, follow=True).text) if x["action"] == path][0]; return [x["value"] for x in f["fields"] if x["name"] == "vars"][0]
tb = getv("/team/variables"); sb = getv("/servers/ttd6yamy3cmb/variables")
csrf = csrf_of(m, "/account")
r1 = m.request("POST", "/team/variables", data={"_csrf": csrf, "vars": "HACK=1\n"})
r2 = m.request("POST", "/servers/ttd6yamy3cmb/variables", data={"_csrf": csrf, "vars": "HACK=1\n"})
unchanged = getv("/team/variables") == tb and getv("/servers/ttd6yamy3cmb/variables") == sb
check("S13.7a", r1.status == 403 and r2.status == 403 and unchanged, f"Member POST to team vars {r1.status}, server vars {r2.status}; data unchanged={unchanged}", sev="S1")
t = m.get("/team/variables", follow=True).text
check("S13.7b", "team-value" not in t and "TEAMV" not in t and "team-val-1" not in t, "team variable values are not shown to a Member (page has no form and no values)", sev="S1")
t = m.get("/servers/ttd6yamy3cmb/variables", follow=True).text
check("S13.7c", "server-value" not in t, "server variable values are not shown to a Member", sev="S1")
rec("S13.7d", "INFO", "project and environment variables ARE visible and editable by a Member (a Member works with what is in projects, per CLAUDE.md); team and server variables are Admin-only")
# Member names a team variable in an app of the project and it expands at deploy
A = state()["b_app"]
def setv(path, text, cl):
    f = [x for x in parse_forms(cl.get(path, follow=True).text) if x["action"] == path][0]; return cl.post_form(f, vars=text)
setv(f"/apps/{A}/environment", "A_TEAM={{team.T_VAL}}\n", m)
f = [x for x in parse_forms(m.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
n0 = shout("ls /var/lib/musdash/logs/deployments | wc -l"); m.post_form(f, follow=False)
wait_for(lambda: shout("ls /var/lib/musdash/logs/deployments | wc -l") != n0, 30, 1)
logf = shout("ls -t /var/lib/musdash/logs/deployments/*.log | head -1")
wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"tail -2 {logf}")) and 1, 90, 3)
v = shout(f"docker ps --format '{{{{.Names}}}}' | grep -E 'musdash-{A}' | head -1 | xargs -I{{}} docker exec {{}} env | grep '^A_TEAM='")
check("S13.7e", v == "A_TEAM=team-value", f"a Member can name a team variable and its value reaches the app without the Member ever seeing it: {v}")
