from lib_b import *
c = owner_client(); P = "l4u3323oxpmm"
link, r = invite(c, "t2-member@musdash.test", "member")
print(link)
mc, r = accept(link, "T2 Member", "T2-Member-Pass-2026!x")
print(r.status, [k for k in mc.cookies])
mc2, r = login("t2-member@musdash.test", "T2-Member-Pass-2026!x")
print("member signed in:", any("session" in k for k in mc2.cookies))
m = mc2
# S13.7: values hidden, cannot change team/server variables, can name them in own app
for path in ["/team/variables", f"/projects/{P}/variables", "/servers/ttd6yamy3cmb/variables", f"/environments/jyrxc6zjhvih/variables"]:
    r = m.get(path, follow=True)
    t = flash(r)
    forms = [f for f in parse_forms(r.text) if f["action"] == path]
    val = forms[0]["fields"] if forms else []
    shown = [x["value"] for x in val if x["name"] == "vars"]
    print(path, r.status, "form" if forms else "no form", "values visible:", ("team-value" in r.text or "project-value" in r.text or "server-value" in r.text or "env-value" in r.text))
    if forms:
        rr = m.request("POST", path, data={"_csrf": csrf_of(m, path), "vars": "HACK=1\n"})
        print("   POST", rr.status)
