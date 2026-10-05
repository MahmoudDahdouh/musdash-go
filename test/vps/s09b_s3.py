from lib import *
c = owner_client(); st = state(); sid = st["minio"]; db = st["pg"]; hosts = st["minio_hosts"]
pg = c.get(f"/services/{sid}").text
copies = [v for v in re.findall(r'data-copy="([^"]+)"', pg) if "." not in v]
print("generated:", [(v[:4] + "…") for v in copies])
# the S3 API host is the one that answers 403 on /
api = None
for h in dict.fromkeys(hosts + re.findall(r"([a-z0-9\-]+\.168\.235\.65\.204\.sslip\.io)", re.sub('<[^>]+>', ' ', pg))):
    try:
        if Client(f"http://{h}", timeout=15).get("/").status == 403: api = h
    except Exception: pass
print("S3 api host:", api)
user, pw = copies[0], copies[1]
def rc(cmd): return sh(f"docker run --rm rclone/rclone:1 {cmd} 2>&1", timeout=120)[1]
conf = f":s3,provider=Minio,endpoint=http://{api},access_key_id={user},secret_access_key={pw},region=us-east-1:"
print(rc(f"mkdir '{conf}t-backups'"))
print(rc(f"lsd '{conf}'")[:200])
# storage form
r = c.submit("/settings/storages", action="/settings/storages", name="t-minio", endpoint=f"http://{api}", region="us-east-1", bucket="t-backups", prefix="musdash", access_key=user, secret_key=pw)
pgs = c.get("/settings/storages").text; ids = list(dict.fromkeys(re.findall(r"/settings/storages/([a-z2-7]{12})/test", pgs)))
print(ids, flash(r)[-120:])
mine = ids[-1]
tr = c.post(f"/settings/storages/{mine}/test", dict(_csrf=csrf_of(c, "/settings/storages")), follow=True)
check("S9.7a", "works" in flash(tr).lower() or "ok" in flash(tr).lower() or "succe" in flash(tr).lower(), f"Test of the backup storage: {flash(tr)[-140:]!r}", sev="S2")
check("S9.7b", user not in c.get("/settings/storages").text and pw not in c.get("/settings/storages").text, "keys not shown again on the storages page", sev="S1")
# schedule with storage, keep 2, make 4 backups
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=False, schedule="0 3 * * *", keep="2", storage_id=mine)
def nback(): return len(set(re.findall(r"/databases/%s/backups/([a-z2-7]{12})/(?:delete|restore|download)" % db, c.get(f"/databases/{db}/backups").text)))
for i in range(4):
    c.post(f"/databases/{db}/backups", dict(_csrf=csrf_of(c, f"/databases/{db}/backups"))); time.sleep(45)
    time.sleep(5)
lst = rc(f"ls '{conf}t-backups/musdash'")
files = [l for l in lst.splitlines() if ".dump.gz" in l]
check("S9.7c", len(files) >= 1, f"backups are copied to the bucket: {lst.strip()[:300]!r}", sev="S2")
check("S9.7d", len(files) <= 2 and nback() <= 2, f"retention keep=2 also prunes the bucket: {len(files)} in bucket, {nback()} on server", sev="S2")
rc_ = shout("ls /tmp/rclone* /var/lib/musdash/work 2>&1 | head -5; docker ps -a --filter ancestor=rclone/rclone:1 --format '{{.Names}}' | head -3")
rec("S9.7e", "INFO", f"rclone container/leftovers after uploads: {rc_!r}")
env_left = shout("grep -rl 'secret_access_key\\|%s' /var/lib/musdash/work /var/lib/musdash/apps 2>/dev/null | head -3" % pw[:8])
check("S9.7f", not env_left, f"the keys' temporary file is gone after the upload: {env_left!r}", sev="S1")
rc2, out = sh(f"grep -a -c '{pw}' /var/lib/musdash/musdash.db /var/lib/musdash/musdash.db-wal")
check("S9.7g", all(x.endswith(':0') for x in out.split()), "S3 secret key sealed at rest: " + out.replace("\n", " "), sev="S1")
# S9.8 SSRF on storage endpoints
bad = {"loopback": "http://127.0.0.1:9000", "localhost": "http://localhost:9000", "metadata": "http://169.254.169.254", "container ip": "http://172.18.0.2:9000", "ipv6 loopback": "http://[::1]:9000", "dns->127": "http://127.0.0.1.nip.io:9000"}
res = {}
for k, ep in bad.items():
    r = c.submit("/settings/storages", action="/settings/storages", name="t-ssrf", endpoint=ep, region="us-east-1", bucket="b", prefix="", access_key="a", secret_key="s")
    pgs2 = c.get("/settings/storages").text; ids2 = list(dict.fromkeys(re.findall(r"/settings/storages/([a-z2-7]{12})/test", pgs2)))
    new = [i for i in ids2 if i not in ids]
    verdict = "refused at save"
    if new:
        t = c.post(f"/settings/storages/{new[0]}/test", dict(_csrf=csrf_of(c, "/settings/storages")), follow=True); msg = flash(t)[-160:]
        verdict = "saved; test: " + ("REACHED" if ("works" in msg.lower() and "not" not in msg.lower()) else "refused")
        c.post(f"/settings/storages/{new[0]}/delete", dict(_csrf=csrf_of(c, "/settings/storages")))
    res[k] = verdict
check("S9.8", not any("REACHED" in v for v in res.values()), f"storage endpoints pointing at loopback/metadata/container addresses never get a connection: {res}", sev="S1")
