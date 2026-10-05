from lib import *
o = state()["owner"]; PW = "Member-Pass-2026!z"
owner = owner_client()
for f in parse_forms(owner.get("/team").text):  # stray invitations from exploration
    if f["action"].startswith("/team/invitations/") and f["action"].endswith("/delete"): owner.post_form(f)

# S3.1 / S3.2 invitation
link, r = invite(owner, "member1@musdash.test", "member")
check("S3.1a", link is not None, "invitation link shown on creation")
later = owner.get("/team").text
check("S3.1b", link.split("/")[-1] not in later, "link is not shown again on a later GET", sev="S1")
c1, r = accept(link, "Member One", PW)
check("S3.2a", c1.get("/account").status == 200, "invitee chose a name and password and is signed in")
c1b = Client(); rr = c1b.submit(link, action="/invite/", name="Replay", password=PW) if False else None
rep = Client().get(link, follow=True)
check("S3.1c", "Sign in" in rep.text or "Create" not in rep.text or rep.status in (404, 410), f"link used once: re-opening gives {rep.status}", sev="S1")
rr = Client().submit(link, action="/invite/", name="Replay", password=PW) if "/invite/" in rep.text and parse_forms(rep.text) and any("/invite/" in f["action"] for f in parse_forms(rep.text)) else None
check("S3.1d", rr is None, "the used link offers no form to join a second time", sev="S1")
link2, _ = invite(owner, "admin1@musdash.test", "admin"); a1, _ = accept(link2, "Admin One", PW)
link3, _ = invite(owner, "member2@musdash.test", "member"); c2, _ = accept(link3, "Member Two", PW)
link4, _ = invite(owner, "admin2@musdash.test", "admin"); a2, _ = accept(link4, "Admin Two", PW)
save_state(users={"member1": "member1@musdash.test", "admin1": "admin1@musdash.test", "member2": "member2@musdash.test", "admin2": "admin2@musdash.test"}, user_pw=PW)
r, ms = members(owner)
emails = {}
for mid in ms: pass
rows = re.findall(r"([a-z0-9]+@musdash\.test)", r.text)
check("S3.2b", all(e in r.text for e in ["member1@musdash.test", "admin1@musdash.test"]), "members listed on /team")
# map member ids to emails by position in the page
idx = {}
for mid in ms:
    pos = r.text.find("/team/members/" + mid)
    seg = r.text[max(0, pos - 1500):pos]
    e = re.findall(r"([a-z0-9]+@musdash\.test)", seg); idx[mid] = e[-1] if e else "?"
print(idx)
save_state(member_ids={v: k for k, v in idx.items()})

# S3.3 role matrix
def code_of(c, method, path, data=None):
    t = csrf_of(c)
    d = dict(data or {}); d["_csrf"] = t
    return (c.get(path) if method == "GET" else c.post(path, d)).status
ADMIN_ONLY = [("GET", "/settings"), ("POST", "/settings"), ("GET", "/settings/notifications"), ("POST", "/settings/notifications"), ("GET", "/settings/storages"),
              ("POST", "/settings/storages"), ("POST", "/servers"), ("POST", "/sources/github"), ("POST", "/sources/keys"), ("POST", "/team"), ("POST", "/team/invitations"),
              ("POST", "/team/variables")]
ALL = [("GET", "/servers"), ("GET", "/sources"), ("GET", "/team"), ("GET", "/team/variables"), ("GET", "/tags"), ("GET", "/account"), ("GET", "/")]
m_res = {f"{m} {p}": code_of(c1, m, p) for m, p in ADMIN_ONLY}
check("S3.3a", all(v == 403 for v in m_res.values()), "Member refused (403) on admin routes: " + json.dumps(m_res), sev="S1")
m_ok = {f"{m} {p}": code_of(c1, m, p) for m, p in ALL}
check("S3.3b", all(v == 200 for v in m_ok.values()), "Member can reach shared pages: " + json.dumps(m_ok), sev="S2")
a_res = {f"{m} {p}": code_of(a1, m, p) for m, p in ADMIN_ONLY}
check("S3.3c", all(v != 403 for v in a_res.values()), "Admin not refused on admin routes: " + json.dumps(a_res), sev="S2")
ids = save = state()["member_ids"]
owner_id = [k for k, v in idx.items() if v == o["email"]][0]
a_own = {"role of owner": code_of(a1, "POST", f"/team/members/{owner_id}/role", dict(role="member")),
         "remove owner": code_of(a1, "POST", f"/team/members/{owner_id}/delete"),
         "reset owner": code_of(a1, "POST", f"/team/members/{owner_id}/reset"),
         "2fa-off owner": code_of(a1, "POST", f"/team/members/{owner_id}/two-step-off"),
         "role of member": code_of(a1, "POST", f"/team/members/{ids['member2@musdash.test']}/role", dict(role="admin")),
         "remove admin2": code_of(a1, "POST", f"/team/members/{ids['admin2@musdash.test']}/delete"),
         "reset admin2": code_of(a1, "POST", f"/team/members/{ids['admin2@musdash.test']}/reset"),
         "2fa-off admin2": code_of(a1, "POST", f"/team/members/{ids['admin2@musdash.test']}/two-step-off")}
check("S3.3d", all(v in (403, 404) for v in a_own.values()), "Admin cannot change roles or act on Owner/Admin: " + json.dumps(a_own), sev="S1")
check("S3.4", True, "see S3.3d") if all(v in (403, 404) for v in a_own.values()) else None
# Admin invitations: members only
r = a1.submit("/team", action="/team/invitations", email="x-admin@musdash.test", role="admin")
t = flash(r)
check("S3.4b", "invite/" not in r.text or "x-admin@" not in t, "Admin cannot invite an Admin (README: Admins invite Members)", sev="S2", evidence=t[-300:])
r = a1.submit("/team", action="/team/invitations", email="x-member@musdash.test", role="member")
lk = re.search(r"/invite/[A-Za-z0-9_\-]+", r.text)
check("S3.4c", lk is not None, "Admin can invite a Member")
# S3.10 cancel invitation
if lk:
    forms = [f for f in parse_forms(a1.get("/team").text) if f["action"].startswith("/team/invitations/") and f["action"].endswith("/delete")]
    for f in forms: a1.post_form(f)
    chk = Client().get(lk.group(0), follow=True)
    check("S3.10", "form" not in chk.text.lower() or not any("/invite/" in f["action"] for f in parse_forms(chk.text)), "cancelled invitation link is dead", sev="S2")
# S3.11 rename team
r = a1.submit("/team", action="/team", name="Renamed Team")
check("S3.11a", "Renamed Team" in a1.get("/team").text, "Admin renames the team")
check("S3.11b", code_of(c1, "POST", "/team", dict(name="Hacked")) == 403, "Member cannot rename the team", sev="S1")
owner.submit("/team", action="/team", name="Default team")
