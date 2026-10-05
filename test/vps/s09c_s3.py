from lib import *
c = owner_client(); st = state(); sid = st["minio"]; db = st["pg"]; mine = "rfh3pwxnwwaz"
pg = c.get(f"/services/{sid}").text; copies = [v for v in re.findall(r'data-copy="([^"]+)"', pg) if "." not in v]; pw, user = copies[0], copies[1]
api = "jstmztuw.168.235.65.204.sslip.io"
def rc(args): return sh(f"docker run --rm -e RCLONE_CONFIG_M_TYPE=s3 -e RCLONE_CONFIG_M_PROVIDER=Minio -e RCLONE_CONFIG_M_ENDPOINT=http://{api} -e RCLONE_CONFIG_M_ACCESS_KEY_ID='{user}' -e RCLONE_CONFIG_M_SECRET_ACCESS_KEY='{pw}' -e RCLONE_CONFIG_M_REGION=us-east-1 rclone/rclone:1 {args} 2>&1", timeout=120)[1]
print(rc("mkdir M:t-backups")); print("buckets:", rc("lsd M:").strip()[:120])
tr = c.post(f"/settings/storages/{mine}/test", dict(_csrf=csrf_of(c, "/settings/storages")), follow=True)
check("S9.7a", "works" in flash(tr).lower(), f"Test of my backup storage: {flash(tr)[-110:]!r}", sev="S2")
c.post(f"/settings/storages/{mine}/delete", dict(_csrf=csrf_of(c, "/settings/storages")))
c.submit("/settings/storages", action="/settings/storages", name="t-minio", endpoint=f"http://{api}", region="us-east-1", bucket="t-backups", prefix="musdash", access_key=user, secret_key=pw)
mine = [i for i in dict.fromkeys(re.findall(r"/settings/storages/([a-z2-7]{12})/test", c.get("/settings/storages").text)) if i != "kg26enzzdjc6"][0]
tr = c.post(f"/settings/storages/{mine}/test", dict(_csrf=csrf_of(c, "/settings/storages")), follow=True)
check("S9.7a", "works" in flash(tr).lower(), f"Test of my backup storage: {flash(tr)[-110:]!r}", sev="S2")
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=False, schedule="0 3 * * *", keep="2", storage_id=mine)
def listing(): return rc("ls M:t-backups")
for i in range(4):
    c.post(f"/databases/{db}/backups", dict(_csrf=csrf_of(c, f"/databases/{db}/backups"))); time.sleep(40)
time.sleep(10); lst = listing(); files = [l for l in lst.splitlines() if ".dump.gz" in l]
n_server = len(set(re.findall(r"/databases/%s/backups/([a-z2-7]{12})/(?:delete|restore|download)" % db, c.get(f"/databases/{db}/backups").text)))
check("S9.7c", len(files) >= 1, f"backups are copied to my bucket: {len(files)} object(s): {[l.strip()[-60:] for l in files]}", sev="S2")
check("S9.7d", len(files) <= 2 and n_server <= 2, f"retention keep=2 also prunes the bucket: {len(files)} in bucket, {n_server} on server", sev="S2")
left = shout("docker ps -a --filter ancestor=rclone/rclone:1 --format '{{.Names}}' | wc -l; ls /var/lib/musdash/work | head -3; find /var/lib/musdash -newer /tmp -name '*rclone*' 2>/dev/null | head -3")
rec("S9.7e", "INFO", f"leftovers after uploads (rclone containers, work dir): {left!r}")
dl = Client().get("/")  # placeholder
# deleting a backup removes its bucket copy?
bf = [f for f in parse_forms(c.get(f"/databases/{db}/backups").text) if re.match(r"/databases/%s/backups/[a-z2-7]{12}/delete" % db, f["action"])]
c.post_form(bf[0]); time.sleep(15); l2 = [l for l in listing().splitlines() if ".dump.gz" in l]
rec("S9.7h", "PASS" if len(l2) < len(files) else "INFO", f"deleting a backup in the UI: bucket objects {len(files)} -> {len(l2)} (a copy off the server may be left on purpose)")
# restore from the server copy still works afterwards; disable schedule storage
c.submit(f"/databases/{db}/backups", action=f"/databases/{db}/backups/schedule", enabled=False, schedule="0 3 * * *", keep="7", storage_id="")
