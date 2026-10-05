from lib import *
o = state()["owner"]; PW = o["password"]
c = owner_client()
def make_token(cl, name, ability="read", expires="90", pw=PW):
    r = cl.submit("/account", action="/account/tokens", token_name=name, token_ability=ability, token_expires=expires, token_password=pw)
    m = re.search(r"msd_[A-Za-z0-9_\-]{10,}", r.text)
    return (m.group(0) if m else None), r
tok, r = make_token(c, "t-read", "read")
r = c.get("/account")
check("S2.10c", tok and tok not in r.text and "t-read" in r.text, "token shown once only, name listed afterwards")
api = Client(token=tok)
me = api.get("/api/v1/me"); check("S2.10d", me.status == 200, f"read token works on /api/v1/me: {me.text[:120]}")
rc, out = sh(f"strings /var/lib/musdash/musdash.db* | grep -c '{tok}'")
check("S2.10e", out.strip().splitlines()[-1] == "0", "API token plaintext not in SQLite file/WAL (only a SHA-256)", sev="S1", evidence=out)

# S2.11 revoke
before = api.get("/api/v1/me").status
r = c.get("/account")
rev = [f for f in parse_forms(r.text) if f["action"].startswith("/account/tokens/") and f["action"].endswith("/delete")]
check("S2.11a", len(rev) >= 1, f"{len(rev)} revoke buttons listed")
for f in rev: c.post_form(f)
check("S2.11b", before == 200 and api.get("/api/v1/me").status == 401, "revoked token refused at once")

# long-lived tokens for later suites
rt, _ = make_token(c, "api-read", "read", "never"); dt, _ = make_token(c, "api-deploy", "deploy", "never")
save_state(read_token=rt, deploy_token=dt)
check("S2.10f", Client(token=rt).get("/api/v1/me").status == 200 and Client(token=dt).get("/api/v1/me").status == 200, "read and deploy tokens created for later suites")

# S2.8 turn two-step off: needs password and a code
r = c.get("/account")
off = [f for f in parse_forms(r.text) if f["action"] == "/account/two-step/off"][0]
print([x["name"] for x in off["fields"]])
rb = c.post_form(off, **{off["fields"][1]["name"]: "wrong-password-xx", off["fields"][2]["name"]: "000000"})
r = c.get("/account")
check("S2.8a", "/account/two-step/off" in r.text, "wrong password+code does not turn it off")
st = state()
time.sleep(30 - time.time() % 30 + 1)
off = [f for f in parse_forms(c.get("/account").text) if f["action"] == "/account/two-step/off"][0]
nm = [x["name"] for x in off["fields"] if x["type"] != "hidden"]
rb = c.post_form(off, **{nm[0]: PW, nm[1]: totp(st["totp_secret"])})
save_state(totp_last=int(time.time() // 30))
r = c.get("/account")
check("S2.8b", "/account/two-step/start" in r.text and "/account/two-step/off" not in r.text, "turned off with password + code")
cl, rr = login(o["email"], PW)
check("S2.8c", cl.get("/account").status == 200 and "/login/code" not in rr.text, "sign-in needs the password only again")
save_state(totp_secret=None)
