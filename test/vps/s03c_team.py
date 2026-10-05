from lib import *
o = state()["owner"]; st = state(); ids = st["member_ids"]; PW = st["member1_pw"]
owner = owner_client(); m1 = "member1@musdash.test"
def form_for(c, action):
    for f in parse_forms(c.get("/team").text):
        if f["action"] == action: return f
c1, _ = login(m1, PW)
r = c1.submit("/account", action="/account/tokens", token_name="m1-deploy", token_ability="deploy", token_expires="90", token_password=PW)
t1 = re.search(r"msd_[A-Za-z0-9_\-]{10,}", r.text).group(0)
a = Client(token=t1).get("/api/v1/me").status
wrong = owner.post_form(form_for(owner, f"/team/members/{ids[m1]}/delete"), confirm="nobody@x.test")
still = m1 in owner.get("/team").text
check("S3.7d", still, "removal needs the member's address typed; wrong text removes nothing")
owner.post_form(form_for(owner, f"/team/members/{ids[m1]}/delete"), confirm=m1)
check("S3.7a", a == 200 and Client(token=t1).get("/api/v1/me").status == 401 and c1.get("/account").status in (302, 303, 401, 403), "removed member: session and API token dead", sev="S1")
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=m1, password=PW)
check("S3.7b", not any("session" in k for k in x.cookies), "removed member cannot sign in")
check("S3.7c", m1 not in owner.get("/team").text, "removed member no longer listed")
# S3.8 strict
oid = [k for k, v in ids.items() if v == o["email"]] or [re.findall(r"/team/members/([a-z2-7]+)/role", owner.get("/team").text)[0]]
oid = oid[0] if isinstance(oid[0], str) else oid
oid = re.findall(r"/team/members/([a-z2-7]+)/role", owner.get("/team").text)[0]
tk = csrf_of(owner)
d = owner.post(f"/team/members/{oid}/delete", dict(_csrf=tk, confirm=o["email"]))
r = owner.post(f"/team/members/{oid}/role", dict(_csrf=tk, role="member"), follow=True)
f = [x for x in parse_forms(owner.get("/team").text) if x["action"] == f"/team/members/{oid}/role"][0]
role = [x for x in f["fields"] if x["name"] == "role"][0]["value"]
check("S3.8", role == "owner" and owner.get("/settings").status == 200, f"last owner: delete {d.status}, demote -> role still {role!r}; msg: {flash(r)[-160:]}", sev="S1")
