from lib import *
o = state()["owner"]; PW = o["password"]
rk = state()["read_token"]
rc, out = sh(f"grep -a -c '{rk}' /var/lib/musdash/musdash.db /var/lib/musdash/musdash.db-wal /var/lib/musdash/musdash.db-shm; echo; grep -a -c 'owner@musdash.test' /var/lib/musdash/musdash.db")
rec("S2.10e", "PASS" if re.findall(r":(\d+)", out)[:3] == ["0", "0", "0"] else "FAIL", "API token plaintext count in db/wal/shm: " + out.replace("\n", " "), "" if re.findall(r":(\d+)", out)[:3] == ["0","0","0"] else "S1", evidence=out)

# re-enable two-step on the owner and time the turn-off request
c, _ = login(o["email"], PW)
r = c.submit("/account", action="/account/two-step/start", start_password=PW)
secret = re.search(r"((?:[A-Z2-7]{4} ){7}[A-Z2-7]{4})", re.sub(r"<[^>]+>", " ", r.text)).group(1).replace(" ", "")
conf = [f for f in parse_forms(r.text) if "confirm" in f["action"]][0]
rr = c.post_form(conf, code=totp(secret))
codes = list(dict.fromkeys(re.findall(r"\b[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}\b", rr.text)))
save_state(totp_secret=secret, recovery=codes, totp_last=int(time.time() // 30))
time.sleep(30 - time.time() % 30 + 1)
off = [f for f in parse_forms(c.get("/account").text) if f["action"] == "/account/two-step/off"][0]
c.timeout = 120
t0 = time.time()
try:
    rb = c.post_form(off, off_password=PW, code=totp(secret), follow=False)
    dt = time.time() - t0
    rec("S2.8b", "PASS" if dt < 5 else "FAIL", f"turn-off answered {rb.status} in {dt:.1f}s (location {rb.location})", "" if dt < 5 else "S2")
except Exception as e:
    rec("S2.8b", "FAIL", f"turn-off request hung/timed out after {time.time()-t0:.0f}s: {e!r}; server log says it completed", "S2")
save_state(totp_last=int(time.time() // 30))
