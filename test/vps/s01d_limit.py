from lib import *
o = state()["owner"]
c = Client()
r = c.submit("/login", action="/login", email=o["email"], password=o["password"])
msg = re.findall(r'(?:Too many|try again|wait|minutes)[^<]{0,120}', r.text)
rec("S1.7", "PASS" if r.status in (429, 401, 403) and not any("session" in k for k in c.cookies) else "FAIL",
    f"after 6+ wrong passwords even the CORRECT password for the account is refused: HTTP {r.status}, msg={msg[:1]}, Retry-After={r.h('retry-after')}. Lock is 5 tries/15 min per account and per address, in memory.",
    evidence=r.text[:200])
# a different address cannot be simulated from one host; document design tradeoff
rec("S1.7b", "INFO", "Anyone who knows the owner's email can lock the owner out of password sign-in for 15 min by sending 5 bad passwords (account limiter, by design in handlers_auth.go:329). Restart of musdash-server clears the in-memory limiter.")
print(sh("systemctl restart musdash-server; sleep 3; systemctl is-active musdash-server")[1])
