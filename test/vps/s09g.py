from lib_b import *
import base64
c = owner_client(); S = state()["b_svc_minio"]; N = f"musdash-{S}_default"
def flashmsg(rr):
    ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", ""))
    return base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
r = c.get("/settings/storages", follow=True)
rows = re.findall(r"(t2-minio-[\w\-]+) t2-backups/db (\S+) Test", flash(r)); tests = [f for f in parse_forms(r.text) if f["action"].endswith("/test")]
ids = {}
for (n, e), f in zip(rows, tests):
    ids[n] = f["action"].split("/")[-2]
    m = flashmsg(c.post_form(f, follow=False)); print(n, e, "->", m[:260])
    rec(f"S9.7t-{n[9:]}", "INFO", m[:260])
save_state(b_storage_ids=ids)
