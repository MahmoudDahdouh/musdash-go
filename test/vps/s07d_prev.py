from lib import *
import hmac, hashlib
c = owner_client(); st = state(); g = st["git"]; path = f"/webhooks/git/{g}"; REPO = "crccheck/docker-hello-world"
SEC = re.search(r'data-copy="([^"]+)"', c.get(f"/apps/{g}/webhook-secret").text).group(1)
def sig(b): return "sha256=" + hmac.new(SEC.encode(), b, hashlib.sha256).hexdigest()
def hook(b): return Client().request("POST", path, raw=b, headers={"Content-Type": "application/json", "X-Hub-Signature-256": sig(b), "X-GitHub-Event": "pull_request"})
def pr(n, action="opened", head=REPO, sha=None): return json.dumps({"action": action, "number": n, "pull_request": {"head": {"ref": "master", "sha": sha or ("%040d" % n), "repo": {"full_name": head}}, "base": {"ref": "master", "repo": {"full_name": REPO}}}, "repository": {"full_name": REPO}}).encode()
def previews():
    pg = c.get(f"/apps/{g}/settings").text
    nums = re.findall(r"/previews/(\d+)/delete", pg); ids = [i for i in dict.fromkeys(re.findall(r"/apps/([a-z2-7]{12})\b", pg)) if i != g]
    return dict(zip(nums, ids)) if len(nums) == len(ids) else {n: None for n in nums}
def status(i): return re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{i}").text))
# S7.17a (already opened in the previous run)
pv = previews(); check("S7.17a", "7" in pv, f"PR #7 opened -> preview exists, listed on the parent's settings: {pv}", sev="S2")
pid_ = pv["7"]
ok = wait_for(lambda: "Running" in status(pid_)[:400] or "Failed" in status(pid_)[:400], 240, 5)
s = status(pid_); check("S7.17b", "Running" in s[:400], f"preview built from the PR branch and running: {s[90:330]}", sev="S2")
dom = re.findall(r"([a-z0-9.\-]+\.sslip\.io)", s); dom = dom[0] if dom else None
body = Client(f"http://{dom}").get("/") if dom else None
check("S7.17c", body is not None and body.status == 200, f"preview answers at its own generated address {dom}: {body.status if body else None}", sev="S2")
envs = shout(f"docker inspect $(docker ps -q --filter name=musdash-{pid_} | head -1) --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | grep -E 'MUSDASH'")
check("S7.17d", "MUSDASH_PREVIEW=1" in envs and "MUSDASH_PULL_REQUEST=7" in envs, f"preview env carries MUSDASH_PREVIEW and MUSDASH_PULL_REQUEST: {envs.replace(chr(10), ' ')}", sev="S3")
mounts = shout(f"docker inspect $(docker ps -q --filter name=musdash-{pid_} | head -1) --format '{{{{json .Mounts}}}}'")
check("S7.19c", mounts.strip() in ("[]", "null"), f"preview has none of the app's volumes or server directories: {mounts}", sev="S2")
s1 = c.get(f"/apps/{pid_}/settings", follow=False); sp = c.post(f"/apps/{pid_}/settings", dict(_csrf=csrf_of(c), name="x", image="nginx", port="80"), follow=False)
sd = c.post(f"/apps/{pid_}/domains", dict(_csrf=csrf_of(c), host="evil.example.test"), follow=False); se = c.post(f"/apps/{pid_}/environment", dict(_csrf=csrf_of(c), vars="A=1"), follow=False)
rec("S7.19a", "PASS" if all(x.status in (302, 303, 403, 404, 409) for x in (sp, sd, se)) else "FAIL", f"configuring a preview is turned away: settings POST {sp.status}, domains POST {sd.status}, environment POST {se.status} (settings GET {s1.status})", "" if all(x.status in (302, 303, 403, 404, 409) for x in (sp, sd, se)) else "S2")
def n_deps(i): return len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{i}/deployments").text)))
n0 = n_deps(pid_); hook(pr(7, "synchronize", sha="f" * 40)); time.sleep(25); n1 = n_deps(pid_)
check("S7.17e", n1 == n0 + 1, f"push to the PR branch ('synchronize') redeploys the preview: {n0}->{n1}", sev="S2")
wait_for(lambda: "Running" in status(pid_)[:400], 120, 4)
hook(pr(7, "closed")); time.sleep(30)
check("S7.17f", "7" not in previews() and not shout(f"docker ps -aq --filter name=musdash-{pid_}"), "closing the pull request removes the preview app and its container", sev="S2")
# fork
r = hook(pr(8, head="someone/fork")); time.sleep(6); check("S7.18", "8" not in previews(), f"pull request from a fork gets no preview ({r.text.strip()})", sev="S1")
# limit of ten
for n in range(20, 33): hook(pr(n)); time.sleep(0.7)
time.sleep(20); cnt = len(previews())
check("S7.19b", cnt == 10, f"13 pull requests opened: {cnt} previews exist (limit is ten per app)", sev="S3")
for n in range(20, 33): hook(pr(n, "closed")); time.sleep(0.7)
wait_for(lambda: len(previews()) == 0, 180, 5); left = len(previews()); rec("S7.19d", "PASS" if left == 0 else "FAIL", f"all previews removed after closing: {left} left; containers: {shout('docker ps -q --filter name=musdash- | wc -l')}", "" if left == 0 else "S3")
c.submit(f"/apps/{g}/settings", action=f"/apps/{g}/previews", previews=False, preview_domain="")
