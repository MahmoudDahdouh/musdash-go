from lib import *
o = state()["owner"]; PW = o["password"]
rec("S2.8d", "INFO", "one POST /account/two-step/off timed out client-side after 60s although the server logged 'second step turned off'; 2nd attempt answered in 0.6s. Not reproduced.")
cl, rr = login(o["email"], PW)
check("S2.8c", cl.get("/account").status == 200 and "/login/code" not in rr.text, "after turn-off, password alone signs in")
# S2.9: turn it on again, then clear it with the CLI
c = cl
r = c.submit("/account", action="/account/two-step/start", start_password=PW)
secret = re.search(r"((?:[A-Z2-7]{4} ){7}[A-Z2-7]{4})", re.sub(r"<[^>]+>", " ", r.text)).group(1).replace(" ", "")
conf = [f for f in parse_forms(r.text) if "confirm" in f["action"]][0]
c.post_form(conf, code=totp(secret)); save_state(totp_secret=secret, totp_last=int(time.time() // 30))
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=o["email"], password=PW)
asks = "/login/code" in r.text
rc, out = sh(f"sudo -u musdash MUSDASH_DATA=/var/lib/musdash /usr/local/bin/musdash disable-2fa {o['email']} 2>&1; echo rc=$?")
x = Client(); x.get("/login"); r = x.submit("/login", action="/login", email=o["email"], password=PW)
check("S2.9", asks and "/login/code" not in r.text and any("session" in k for k in x.cookies), f"disable-2fa CLI: asked for a code before, password-only after. CLI says: {out.strip()[:200]}")
save_state(totp_secret=None)
