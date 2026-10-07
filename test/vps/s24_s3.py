from lib import *
c = owner_client(); st = state(); sid = st["minio"]; dbid = st["pgdb"]
envs = shout(f"docker exec $(docker ps -q --filter name=musdash-{sid}-minio | head -1) printenv | grep -E '^MINIO_ROOT_(USER|PASSWORD)='")
user = re.search(r"MINIO_ROOT_USER=(.*)", envs).group(1); pw = re.search(r"MINIO_ROOT_PASSWORD=(.*)", envs).group(1)
api = None
for h in st["minio_hosts"]:
    try:
        if Client(f"http://{h}", timeout=15).get("/").status == 403: api = h
    except Exception: pass
# a bucket via rclone, then the storage in musdash
conf = f":s3,provider=Minio,endpoint=http://{api},access_key_id={user},secret_access_key={pw},region=us-east-1:"
def rc(cmd): return sh(f"docker run --rm rclone/rclone:1 {cmd} 2>&1 | grep -v NOTICE", timeout=120)[1]
rc(f"mkdir '{conf}t-backups'")
r = c.submit("/settings/storages", action="/settings/storages", name="t-minio2", endpoint=f"http://{api}", region="us-east-1", bucket="t-backups", prefix="musdash", access_key=user, secret_key=pw)
pgs = c.get("/settings/storages").text; ids = list(dict.fromkeys(re.findall(r"/settings/storages/([a-z2-7]{12})/test", pgs))); mine = ids[-1]
tr = c.post(f"/settings/storages/{mine}/test", dict(_csrf=csrf_of(c, "/settings/storages")), follow=True)
check("S9.7a", "works" in flash(tr).lower() or "reached" in flash(tr).lower() or "succe" in flash(tr).lower(), f"Test of the backup storage: {flash(tr)[-140:]!r}", "S2")
check("S9.7b", user not in c.get("/settings/storages").text and pw not in c.get("/settings/storages").text, "keys not shown again on the storages page", "S1")
# schedule with storage, then back up now
bp = f"/databases/{dbid}/backups"
r, forms = c.forms(bp); f = c.find_form(forms, bp + "/schedule")
c.post_form(f, enabled=True, schedule="0 3 * * *", keep="2", storage_id=mine)
for i in range(3):
    c.post(bp, dict(_csrf=csrf_of(c, bp))); wait_for(lambda: "Succeeded" in flash(c.get(bp + "/list")) and True, 120, 4); time.sleep(8)
objs = rc(f"ls '{conf}t-backups/musdash'")
n = len([l for l in objs.splitlines() if ".dump" in l])
check("S9.7c", n >= 1, f"backups copied to the bucket: {n} object(s): {objs.strip()[-300:]!r}", "S2")
check("S9.7d", n <= 2, f"retention keep=2 also prunes the bucket ({n} objects after 3 backups)", "S2")
left = shout("find /var/lib/musdash -name 'rclone*' -o -name '*.env' -path '*backup*' 2>/dev/null | head; docker ps -a --filter ancestor=rclone/rclone:1 -q | wc -l")
rec("S9.7f", "INFO", f"leftovers after uploads (rclone key files, containers): {left!r}")
