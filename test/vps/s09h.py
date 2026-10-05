from lib_b import *
c = owner_client(); st = state(); S = st["b_svc_minio"]; N = f"musdash-{S}_default"; D = "qfoenpmqqgvg"
sid = st["b_storage_ids"]["t2-minio-public-domain"]
env = shout(f"docker inspect musdash-{S}-minio-1 --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | grep -E '^MINIO_ROOT_'")
kv = dict(l.split("=", 1) for l in env.splitlines()); U, W = kv["MINIO_ROOT_USER"], kv["MINIO_ROOT_PASSWORD"]
ls = lambda: sh(f"docker run --rm --network {N} -e RCLONE_CONFIG_S3_TYPE=s3 -e RCLONE_CONFIG_S3_PROVIDER=Minio -e RCLONE_CONFIG_S3_ENDPOINT=http://minio:9000 -e RCLONE_CONFIG_S3_ACCESS_KEY_ID={U} -e RCLONE_CONFIG_S3_SECRET_ACCESS_KEY={W} rclone/rclone:1 lsf -R s3:t2-backups 2>/dev/null")[1].strip().splitlines()
S_ = f"/databases/{D}/backups/schedule"
r = c.submit(f"/databases/{D}/backups", action=S_, enabled=True, schedule="* * * * *", keep="1", storage_id=sid)
n = wait_for(lambda: len([x for x in ls() if x.endswith(".gz")]) >= 1 and ls(), 200, 8)
check("S9.7a", bool(n), f"backup copied to MinIO bucket: {n}")
time.sleep(150)
files = [x for x in ls() if x.endswith(".gz")]
local = int(shout(f"ls /var/lib/musdash/backups/{D}/*.gz | wc -l"))
check("S9.7b", len(files) == 1 and local == 1, f"keep=1: bucket has {len(files)} object(s), disk has {local}", evidence="\n".join(ls()))
c.submit(f"/databases/{D}/backups", action=S_, enabled=False, schedule="* * * * *", keep="1", storage_id=sid)
t = flash(c.get(f"/databases/{D}/backups", follow=True)); print(re.findall(r"Succeeded[^D]{0,40}", t)[:4], re.findall(r"Kept on[^D]{0,80}", t)[:1])
