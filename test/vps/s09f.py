from lib_b import *
import base64
c = owner_client(); S = state()["b_svc_minio"]; N = f"musdash-{S}_default"
env = shout(f"docker inspect musdash-{S}-minio-1 --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | grep -E '^MINIO_ROOT_'")
kv = dict(l.split("=", 1) for l in env.splitlines()); U, W = kv["MINIO_ROOT_USER"], kv["MINIO_ROOT_PASSWORD"]
out = sh(f"docker run --rm --network {N} -e RCLONE_CONFIG_S3_TYPE=s3 -e RCLONE_CONFIG_S3_PROVIDER=Minio -e RCLONE_CONFIG_S3_ENDPOINT=http://minio:9000 -e RCLONE_CONFIG_S3_ACCESS_KEY_ID={U} -e RCLONE_CONFIG_S3_SECRET_ACCESS_KEY={W} rclone/rclone:1 mkdir s3:t2-backups && echo bucket-ok")[1]
print(out.strip()[-120:])
host = "d6clivmt.168.235.65.204.sslip.io"
for label, ep in [("public-domain", f"http://{host}"), ("internal-name", "http://minio:9000")]:
    r = c.submit("/settings/storages", action="/settings/storages", has="bucket", name=f"t2-minio-{label}", endpoint=ep, region="us-east-1", bucket="t2-backups", prefix="db", access_key=U, secret_key=W)
    f = [f for f in parse_forms(c.get("/settings/storages", follow=True).text) if f["action"].endswith("/test")]
    rows = re.findall(r"(t2-minio-[\w\-]+) t2-backups (\S+) Test", flash(c.get("/settings/storages", follow=True)))
    for (n, e), form in zip(rows, f):
        if n == f"t2-minio-{label}":
            rr = c.post_form(form, follow=False)
            ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", ""))
            msg = base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
            print(label, "->", msg[:300])
