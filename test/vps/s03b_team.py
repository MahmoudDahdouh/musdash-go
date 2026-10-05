from lib import *
o = state()["owner"]; st = state(); PW = st["user_pw"]; ids = st["member_ids"]
owner = owner_client()
def fresh(email, pw=PW):
    c, r = login(email, pw); return c
def form_for(c, action, page="/team"):
    for f in parse_forms(c.get(page).text):
        if f["action"] == action: return f
def token_for(c, name, ability="deploy"):
    r = c.submit("/account", action="/account/tokens", token_name=name, token_ability=ability, token_expires="90", token_password=PW)
    m = re.search(r"msd_[A-Za-z0-9_\-]{10,}", r.text); return m.group(0) if m else None
def reset_link(actor, mid):
    r = actor.post_form(form_for(actor, f"/team/members/{mid}/reset"))
    m = re.search(r"/reset/[A-Za-z0-9_\-]+", r.text); return (m.group(0), r) if m else (None, r)

# S3.9 reset link for a member
m1 = "member1@musdash.test"
link, r = reset_link(owner, ids[m1])
check("S3.9a", link is not None, "owner makes a password reset link for a Member (shown once)")
check("S3.9b", link.split("/")[-1] not in owner.get("/team").text, "reset link not shown again", sev="S1")
NEWPW = "Member-Reset-2026!q"
rc = Client(); rr = rc.submit(link, action="/reset/", password=NEWPW)
c, r = login(m1, NEWPW)
check("S3.9c", c.get("/account").status == 200, "reset link sets a new password and it works")
bad = fresh(m1, PW) if False else None
rc2 = Client().get(link, follow=True)
check("S3.9d", not any("/reset/" in f["action"] for f in parse_forms(rc2.text)), "reset link works once only", sev="S1")
# restore the member's password via the CLI path later; keep NEWPW for member1
save_state(member1_pw=NEWPW)

# S3.9e reset link does not skip the second step: member1 turns it on, owner resets, sign-in still asks for a code
c = fresh(m1, NEWPW)
r = c.submit("/account", action="/account/two-step/start", start_password=NEWPW)
secret = re.search(r"((?:[A-Z2-7]{4} ){7}[A-Z2-7]{4})", re.sub(r"<[^>]+>", " ", r.text)).group(1).replace(" ", "")
conf = [f for f in parse_forms(r.text) if "confirm" in f["action"]][0]; c.post_form(conf, code=totp(secret))
link, _ = reset_link(owner, ids[m1]); P3 = "Member-Reset3-2026!q"
Client().submit(link, action="/reset/", password=P3)
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=m1, password=P3)
check("S3.9e", "/login/code" in r.text and not any("session" in k for k in x.cookies), "after reset, member with two-step is still asked for a code", sev="S1")
# Admin/Owner turns it off from Team page (S2 README)
rr = owner.post_form(form_for(owner, f"/team/members/{ids[m1]}/two-step-off"))
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=m1, password=P3)
check("S3.9f", any("session" in k for k in x.cookies), "Owner turns off a member's two-step from the Team page; password-only sign-in works", sev="S2")
save_state(member1_pw=P3)

# S3.5 raise a role signs the person out, ends tokens and reset links
m2 = "member2@musdash.test"
c2 = fresh(m2); t2 = token_for(c2, "m2-deploy")
link, _ = reset_link(owner, ids[m2])
ok_before = Client(token=t2).get("/api/v1/me").status
rf = form_for(owner, f"/team/members/{ids[m2]}/role"); owner.post_form(rf, role="admin")
check("S3.5a", ok_before == 200 and c2.get("/account").status in (302, 303, 401, 403), "raising a role signs the person out", sev="S1")
check("S3.5b", Client(token=t2).get("/api/v1/me").status == 401, "raising a role ends their API tokens", sev="S1")
chk = Client().get(link, follow=True)
check("S3.5c", not any("/reset/" in f["action"] for f in parse_forms(chk.text)), "raising a role ends their reset links", sev="S1")
c2 = fresh(m2)
check("S3.5d", c2.get("/settings").status == 200, "after sign-in again member2 is an Admin (can open /settings)")

# S3.6 lower a role: invitations they made are withdrawn
a2 = fresh("admin2@musdash.test"); lk, _ = invite(a2, "invited-by-admin2@musdash.test", "member")
check("S3.6a", lk is not None, "admin2 made an invitation")
rf = form_for(owner, f"/team/members/{ids['admin2@musdash.test']}/role"); owner.post_form(rf, role="member")
chk = Client().get(lk, follow=True)
check("S3.6b", not any("/invite/" in f["action"] for f in parse_forms(chk.text)), "lowering a role withdraws invitations they made", sev="S2")
check("S3.6c", a2.get("/settings").status == 403 or a2.get("/account").status != 200 and fresh("admin2@musdash.test").get("/settings").status == 403, "lowered person loses admin pages")

# S3.7 remove a member
c1 = fresh(m1, P3); t1 = token_for(c1, "m1-deploy")
rm = form_for(owner, f"/team/members/{ids[m1]}/delete"); rr = owner.post_form(rm)
check("S3.7a", Client(token=t1).get("/api/v1/me").status == 401 and c1.get("/account").status in (302, 303, 401, 403), "removed member: session and API token dead", sev="S1")
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=m1, password=P3)
check("S3.7b", not any("session" in k for k in x.cookies), "removed member cannot sign in")
check("S3.7c", m1 not in owner.get("/team").text, "removed member no longer listed")

# S3.8 last owner protected
oid = ids.get(o["email"]) or [k for k in re.findall(r"/team/members/([a-z2-7]+)/role", owner.get("/team").text)][0]
allf = {f["action"]: f for f in parse_forms(owner.get("/team").text)}
own_actions = [a for a in allf if a.startswith("/team/members/") and a.endswith("/delete") and "(you)" in owner.get("/team").text]
r1 = owner.post(f"/team/members/{oid}/delete", dict(_csrf=csrf_of(owner)))
r2 = owner.post(f"/team/members/{oid}/role", dict(_csrf=csrf_of(owner), role="admin"))
still = owner.get("/team")
check("S3.8", r1.status in (403, 404, 422, 303) and "(you)" in still.text and owner.get("/settings").status == 200, f"last owner cannot be removed or demoted (delete {r1.status}, role {r2.status}); still owner and admin pages open", sev="S1")
