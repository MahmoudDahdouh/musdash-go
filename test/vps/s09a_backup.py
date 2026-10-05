from lib import *
c = owner_client(); st = state(); db = st["pg"]; env = st["env"]
copy = re.findall(r'data-copy="([^"]+)"', c.get(f"/databases/{db}").text); pw = copy[1]
net = shout("docker network ls --format '{{.Name}}' | grep musdash-" + env)
def psql(q): return sh(f"docker run --rm --network {net} -e PGPASSWORD='{pw}' postgres:17-alpine psql -h t-pg -U postgres -d postgres -Atc \"{q}\"")[1].strip()
def backups(): 
    t = c.get(f"/databases/{db}/backups").text
    return list(dict.fromkeys(re.findall(r"/databases/%s/backups/([a-z2-7]{12})/(?:delete|restore|download)" % db, t)))
def rows(): 
    return re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/databases/{db}/backups").text))
before = set(backups())
r = c.post(f"/databases/{db}/backups", dict(_csrf=csrf_of(c, f"/databases/{db}/backups")), follow=True)
bid = wait_for(lambda: [b for b in backups() if b not in before] or None, 120, 3)
check("S9.1a", bid, f"manual backup queued and listed: {bid}")
bid = bid[0]
def done(): 
    t = rows(); return "Succeeded" in t or "Done" in t or "Failed" in t
wait_for(lambda: "Running" not in rows() and "Waiting" not in rows(), 180, 3)
t = rows(); i = t.find("Backups"); rec("S9.1b", "INFO", "backups page: " + t[i:i+300])
fl = shout(f"ls -la /var/lib/musdash/backups/{db}/ ; for f in /var/lib/musdash/backups/{db}/*; do gzip -t $f && echo GZIP_OK $f; zcat $f | head -c 8 | strings 2>/dev/null; zcat $f | head -c 8 | od -c | head -1; done")
check("S9.1c", "GZIP_OK" in fl and ("PGDMP" in fl or "P   G   D   M   P" in fl or "PostgreSQL database dump" in fl), f"backup file is a valid gzip of a pg_dump: {fl[-300:]!r}", sev="S2")
mode = shout(f"stat -c '%a %U' /var/lib/musdash/backups/{db} /var/lib/musdash/backups/{db}/*")
check("S9.1d", all(l.startswith(("600", "700")) for l in mode.splitlines()), f"backup files not world-readable: {mode.replace(chr(10), ' | ')}", sev="S1")
# S9.4 restore: drop the table, restore, table back
psql("drop table t"); gone = psql("select to_regclass('t')")
rf = [f for f in parse_forms(c.get(f"/databases/{db}/backups").text) if f["action"] == f"/databases/{db}/backups/{bid}/restore"][0]
wrong = c.post_form(rf, confirm="not-the-name"); still_gone = psql("select to_regclass('t')")
check("S9.5a", still_gone == gone, f"restore with the wrong name typed does nothing ({flash(wrong)[-100:]!r})", sev="S1")
c.post_form(rf, confirm="t-pg"); wait_for(lambda: psql("select to_regclass('t')") == "t", 120, 4)
out = psql("select count(*) from t")
check("S9.4", out.endswith("2") or out == "2", f"restore after typing the name brings the table back with its rows: {out!r}; database stayed running", sev="S1")
# S9.6 download
r = c.get(f"/databases/{db}/backups/{bid}/download")
import gzip, io
ok = False
try: z = gzip.decompress(r.body)[:2000]; ok = z.startswith(b"PGDMP") or b"PostgreSQL database dump" in z
except Exception as e: pass
check("S9.6", r.status == 200 and ok, f"download streams a valid gzip ({r.status}, {len(r.body)} bytes, disposition {r.h('content-disposition')!r})", sev="S2")
# S9.2 schedule + S9.3 retention
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=True, schedule="* * * * *", keep="2", storage_id="")
time.sleep(190)
n = len(backups()); files = shout(f"ls /var/lib/musdash/backups/{db} | wc -l")
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=False, schedule="* * * * *", keep="2", storage_id="")
check("S9.2", n >= 2, f"* * * * * schedule produced backups on its own ({n} listed)", sev="S2")
check("S9.3", int(files) <= 2 and n <= 2, f"retention keep=2: {n} listed, {files.strip()} files on disk after 3+ runs", sev="S2")
# validation
for label, sched in {"bad": "99 * * * *", "words": "nightly"}.items():
    r = c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=True, schedule=sched, keep="2", storage_id="")
    check("S9.2v" + label[0], r.status == 422, f"schedule {sched!r} refused ({r.status})", sev="S3")
r = c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=True, schedule="0 3 * * *", keep="0", storage_id="")
rec("S9.2k", "PASS" if r.status == 422 else "INFO", f"keep=0 -> {r.status}")
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=False, schedule="0 3 * * *", keep="7", storage_id="")
