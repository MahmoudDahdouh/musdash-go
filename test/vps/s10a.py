from lib_b import *
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
r = c.submit(f"/projects/{P}/services/new?env={E}&template=minio", action=f"/projects/{P}/services", name="t2-minio", deploy=True)
m = re.search(r"/services/([a-z2-7]{12})", r.url); print(r.status, r.url)
S = m.group(1); save_state(b_svc_minio=S)
def stat():
    t = flash(c.get(f"/services/{S}", follow=True)); mm = re.search(r"t2-minio (\w+)", t); return mm.group(1) if mm else "?"
print(wait_for(lambda: stat() in ("Running", "Failed", "Error") and stat(), 360, 6))
t = flash(c.get(f"/services/{S}", follow=True)); print(t[170:1300])
print(shout("docker ps --format '{{.Names}} | {{.Image}} | {{.Status}} | {{.Ports}}' | grep -v musdash-db"))
