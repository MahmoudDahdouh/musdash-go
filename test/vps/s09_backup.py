from lib_b import *
import gzip, io
c = owner_client(); st = state()
DB = {"postgres": "qfoenpmqqgvg", "mysql": st["b_db_mysql"], "mariadb": st["b_db_mariadb"], "mongodb": st["b_db_mongodb"], "redis": st["b_db_redis"]}
def backups(D):
    t = c.get(f"/databases/{D}/backups", follow=True)
    return t, re.findall(r'href="(/databases/%s/backups/[^"]+)"' % D, t.text)
for eng, D in DB.items():
    r = c.submit(f"/databases/{D}/backups", action=f"/databases/{D}/backups", has=None) if False else None
    forms = parse_forms(c.get(f"/databases/{D}/backups", follow=True).text)
    f = c.find_form(forms, f"/databases/{D}/backups")
    # exact action match only
    f = [x for x in forms if x["action"] == f"/databases/{D}/backups"]
    if not f:
        rec(f"S9.1-{eng}", "INFO", "no Back up now form (engine may not offer it)", evidence=flash(c.get(f'/databases/{D}/backups', follow=True))[150:500]); continue
    c.post_form(f[0])
    def done():
        t = flash(c.get(f"/databases/{D}/backups", follow=True))
        return ("Succeeded" in t or "succeeded" in t or "Failed" in t or "failed" in t) and t
    t = wait_for(done, 180, 3) or ""
    files = shout(f"ls -la /var/lib/musdash/backups/{D}/ 2>&1")
    fl = [l.split()[-1] for l in files.splitlines() if l.strip().endswith(".gz")]
    ok = False; detail = files[-200:]
    if fl:
        out = sh(f"gzip -t /var/lib/musdash/backups/{D}/{fl[-1]} && stat -c %s /var/lib/musdash/backups/{D}/{fl[-1]}")[1].strip()
        ok = out.isdigit() and int(out) > 0; detail = f"{fl[-1]} {out} bytes"
    check(f"S9.1-{eng}", ok, f"manual backup: {detail}", evidence=files + "\n" + t[170:400])
