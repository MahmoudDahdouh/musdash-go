from lib_b import *
import base64
c = owner_client(); c.timeout = 150
r = c.get("/settings/storages", follow=True)
tests = [f for f in parse_forms(r.text) if f["action"].endswith("/test")]
rows = re.findall(r"(t2-s3-[\w.\-]+) t2-bucket (\S+) Test", flash(r))
for (name, ep), f in zip(rows, tests):
    k = name[6:]; t0 = time.time()
    rr = c.post_form(f, follow=False)
    ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", ""))
    msg = base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
    refused = "this address cannot be used" in msg
    want = k not in ("control", "privatelan")
    ok = refused == want
    rec(f"S9.8t-{k}", "PASS" if ok else "FAIL", f"Test {ep} ({time.time()-t0:.0f}s): {msg}", "" if ok else "S1")
