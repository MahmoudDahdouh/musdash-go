from lib import *
import glob
repo = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
routes = []
for f in glob.glob(repo + "/internal/web/*.go"):
    if f.endswith("_test.go"): continue
    for m in re.finditer(r'handle\("([A-Z]+) ([^"]+)", *([a-zA-Z]+)', open(f).read()):
        routes.append(m.groups())
print(len(routes), "routes")
anon = Client()
bad = {}
for method, pat, who in routes:
    path = re.sub(r"\{[a-z]+\}", "aaaaaaaaaaaa", pat).replace("$", "")
    if who in ("open",):
        continue
    r = anon.request(method, path)
    if who == "signedOut":
        continue
    # signed-out clients must be redirected to login (or refused), never served the page
    if not (r.status in (301, 302, 303, 307, 401, 403) and (r.status in (401, 403) or "/login" in r.location or "/setup" in r.location)):
        bad[f"{method} {pat}"] = (r.status, r.location)
check("S1.13", not bad and len(routes) > 150, f"{len(routes)} routes in the table; every member/admin/owner route refuses a signed-out client (violations: {bad})", sev="S1")
# open routes must not leak anything without proof
opens = {}
for method, pat, who in routes:
    if who == "open" and "api/v1" in pat:
        path = re.sub(r"\{[a-z]+\}", "aaaaaaaaaaaa", pat)
        r = Client().request(method, path); opens[f"{method} {pat}"] = r.status
check("S1.13b", all(v in (401, 403, 404, 405) for v in opens.values()), f"API routes without a token: {sorted(set(opens.values()))}", sev="S1")

# S1.14 reset-password CLI
st = state(); ids = st["member_ids"]
em = "admin1@musdash.test"
c, _ = login(em, st["user_pw"]); before = c.get("/account").status
rc, out = sh(f"sudo -u musdash MUSDASH_DATA=/var/lib/musdash MUSDASH_URL=http://168.235.65.204:8000 /usr/local/bin/musdash reset-password {em} 2>&1")
m = re.search(r"https?://\S+/reset/[A-Za-z0-9_\-]+", out)
check("S1.14a", m is not None, "reset-password prints a one-time link: " + out.strip()[:120])
if m:
    path = "/reset/" + m.group(0).rsplit("/", 1)[1]
    NP = "Admin-CLI-Reset-2026!k"
    Client().submit(path, action="/reset/", password=NP)
    n, _ = login(em, NP)
    check("S1.14b", before == 200 and n.get("/account").status == 200, "link sets a new password")
    check("S1.14c", c.get("/account").status in (302, 303, 401, 403), "old sessions ended by the reset", sev="S2")
    check("S1.14d", not any("/reset/" in f["action"] for f in parse_forms(Client().get(path, follow=True).text)), "link works once", sev="S1")
    save_state(admin1_pw=NP)
