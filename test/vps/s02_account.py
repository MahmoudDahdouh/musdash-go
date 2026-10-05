from lib import *
o = state()["owner"]; PW = o["password"]
c = owner_client()

# S2.1 profile
r = c.submit("/account", action="/account/profile", name="Test Owner Renamed", email=o["email"])
r = c.get("/account")
check("S2.1", "Test Owner Renamed" in r.text, "profile name saved")
c.submit("/account", action="/account/profile", name="Test Owner", email=o["email"])

# S2.10 API token (create before password change so we can test that S2.2 ends it)
def make_token(cl, name, ability="read", expires="90", pw=PW):
    r = cl.submit("/account", action="/account/tokens", token_name=name, token_ability=ability, token_expires=expires, token_password=pw)
    m = re.search(r"msd_[A-Za-z0-9_\-]{10,}", r.text)
    return m.group(0) if m else None, r
tok, r = make_token(c, "t-read", "read")
check("S2.10a", tok is not None, "token shown once: " + (tok[:8] + "…" if tok else r.text[:200]))
bad, rb = make_token(c, "t-bad", "read", pw="wrong-password-xx")
check("S2.10b", bad is None, "token creation needs the password (wrong password refused)")
r = c.get("/account"); check("S2.10c", tok and tok not in r.text and "t-read" in r.text, "token not shown again on a later GET")
dtok, _ = make_token(c, "t-deploy", "deploy", "never")
api = Client(token=tok)
check("S2.10d", api.get("/api/v1/me").status == 200, "read token works on /api/v1/me")
save_state(read_token=tok, deploy_token=dtok)

# stored hashed only
rc, out = sh(f"strings /var/lib/musdash/musdash.db* | grep -c '{tok}' ")
check("S2.10e", out.strip().splitlines()[-1] == "0", "API token plaintext not present in SQLite file/WAL", sev="S1", evidence=out)

# S2.2 password change
NEW = "Owner-New-Pass-2027!y"
r = c.submit("/account", action="/account/password", current="wrong-current-1", password=NEW)
r2 = Client(); r2, rr = login(o["email"], NEW)
check("S2.2a", not any("session" in k for k in r2.cookies), "password change needs the current password")
other, _ = login(o["email"], PW)  # second session
r = c.submit("/account", action="/account/password", current=PW, password=NEW)
check("S2.2b", api.get("/api/v1/me").status == 401, "API tokens end on password change", sev="S1")
check("S2.2c", other.get("/account").status in (302, 303, 401, 403), "other sessions end on password change", sev="S1")
chk, rr = login(o["email"], PW)
check("S2.2d", not any("session" in k for k in chk.cookies), "old password stops working")
c, rr = login(o["email"], NEW)
check("S2.2e", c.get("/account").status == 200, "new password works")
# change back
c.submit("/account", action="/account/password", current=NEW, password=PW)
c, rr = login(o["email"], PW)
tok, _ = make_token(c, "t-read2", "read"); dtok, _ = make_token(c, "t-deploy2", "deploy", "never")
save_state(read_token=tok, deploy_token=dtok)

# S2.11 delete token
r = c.get("/account")
ids = re.findall(r"/account/tokens/([a-z2-7]{12})/delete", r.text)
check("S2.11a", len(ids) >= 2, f"tokens listed with delete buttons ({len(ids)})")
# delete t-read2: find the form containing its name's row; simplest: delete the first and see which token dies
ca = Client(token=tok)
before = ca.get("/api/v1/me").status
forms = parse_forms(r.text)
for f in forms:
    if f["action"].startswith("/account/tokens/") and f["action"].endswith("/delete"):
        c.post_form(f)
after = ca.get("/api/v1/me").status
check("S2.11b", before == 200 and after == 401, f"deleting tokens kills them at once ({before} -> {after})")
# remake for later suites
tok, _ = make_token(c, "api-read", "read", "never"); dtok, _ = make_token(c, "api-deploy", "deploy", "never")
save_state(read_token=tok, deploy_token=dtok)

# S2.3-S2.8 two-step
r = c.submit("/account", action="/account/two-step/start", start_password="wrong-password-xx")
check("S2.3a", "two-step/confirm" not in r.text, "two-step setup needs the password")
r = c.submit("/account", action="/account/two-step/start", start_password=PW)
key = re.search(r"((?:[A-Z2-7]{4} ){7}[A-Z2-7]{4})", re.sub(r"<[^>]+>", " ", r.text))
secret = key.group(1).replace(" ", "")
r2 = c.submit_page = None
bad = [f for f in parse_forms(r.text) if "confirm" in f["action"]][0]
rb = c.post_form(bad, code="000000")
check("S2.3b", "recovery" not in rb.text.lower() and not re.search(r"recovery codes", rb.text, re.I), "wrong code does not turn it on")
r = c.get("/account"); on = "turn off" in r.text.lower() or "Turn off" in r.text
check("S2.3c", not on, "two-step still off after a wrong code")
# the confirm form needs the pending state (sealed cookie); re-run start to get a fresh page
r = c.submit("/account", action="/account/two-step/start", start_password=PW)
key = re.search(r"((?:[A-Z2-7]{4} ){7}[A-Z2-7]{4})", re.sub(r"<[^>]+>", " ", r.text)); secret = key.group(1).replace(" ", "")
conf = [f for f in parse_forms(r.text) if "confirm" in f["action"]][0]
code1 = totp(secret)
rc = c.post_form(conf, code=code1)
txt = re.sub(r"<[^>]+>", " ", rc.text)
codes = re.findall(r"\b[a-z0-9]{5}-[a-z0-9]{5}\b|\b[A-Z0-9]{10}\b|\b[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}\b", txt)
check("S2.3d", len(set(codes)) >= 10 or "recovery" in txt.lower(), f"turned on; recovery codes shown: {len(set(codes))} found", evidence=txt[:700])
rcodes = list(dict.fromkeys(codes))
save_state(totp_secret=secret, recovery=rcodes)
rr = c.get("/account")
check("S2.3e", code1 not in rr.text and secret not in rr.text, "key and recovery codes not shown again on a later GET", sev="S1")

# S2.4 sign in with TOTP: password step gives no session
cl = Client(); cl.get("/login")
r = cl.submit("/login", action="/login", email=o["email"], password=PW)
check("S2.4a", not any("session" in k for k in cl.cookies) and "code" in r.text.lower(), f"password alone gives no session; asks for code (cookies {list(cl.cookies)})", sev="S1")
check("S2.4b", cl.get("/account").status in (302, 303), "no access before the code")
# S2.5 replay: code1 was used at confirm
rr = cl.submit("/login/code", action="/login/code", code=code1)
check("S2.5", not any("session" in k for k in cl.cookies), "the code used at confirmation cannot be used to sign in (replay)", sev="S1")
# wait for next step and use a fresh code
now = time.time(); time.sleep(30 - (now % 30) + 1)
code2 = totp(secret)
rr = cl.submit("/login/code", action="/login/code", code=code2)
check("S2.4c", any("session" in k for k in cl.cookies) and cl.get("/account").status == 200, "password + fresh code signs in")
# replay of code2 in another login
cl2 = Client(); cl2.get("/login"); cl2.submit("/login", action="/login", email=o["email"], password=PW)
rr = cl2.submit("/login/code", action="/login/code", code=code2)
check("S2.5b", not any("session" in k for k in cl2.cookies), "same code in a second sign-in refused", sev="S1")
# S2.7 recovery code
cl3 = Client(); cl3.get("/login"); cl3.submit("/login", action="/login", email=o["email"], password=PW)
rr = cl3.submit("/login/code", action="/login/code", code=rcodes[0]) if rcodes else None
check("S2.7a", rcodes and any("session" in k for k in cl3.cookies), "recovery code signs in")
cl4 = Client(); cl4.get("/login"); cl4.submit("/login", action="/login", email=o["email"], password=PW)
rr = cl4.submit("/login/code", action="/login/code", code=rcodes[0])
check("S2.7b", not any("session" in k for k in cl4.cookies), "the same recovery code works once only", sev="S1")
# S2.6 wrong codes lock the second step
cl5 = Client(); cl5.get("/login"); cl5.submit("/login", action="/login", email=o["email"], password=PW)
last = None
for i in range(7):
    last = cl5.submit("/login/code", action="/login/code", code="%06d" % (111111 + i))
cl6 = Client(); cl6.get("/login"); cl6.submit("/login", action="/login", email=o["email"], password=PW)
time.sleep(31 - (time.time() % 30)); good = totp(secret)
rr = cl6.submit("/login/code", action="/login/code", code=good)
check("S2.6", not any("session" in k for k in cl6.cookies), f"after 5+ wrong codes even a right code is refused: {last.status} {re.findall('Try again[^<]{0,40}', last.text)[:1]}", sev="S2")
save_state(note_lockout=True)
