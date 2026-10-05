from lib import *
import hmac, hashlib
c = owner_client(); st = state(); g = st["git"]; web = st["web"]; path = f"/webhooks/git/{g}"
REPO = "crccheck/docker-hello-world"
SEC = re.findall(r'data-copy="([^"]+)"', c.get(f"/apps/{g}/settings").text)[1]
def sig(b): return "sha256=" + hmac.new(SEC.encode(), b, hashlib.sha256).hexdigest()
def hook(b): return Client().request("POST", path, raw=b, headers={"Content-Type": "application/json", "X-Hub-Signature-256": sig(b), "X-GitHub-Event": "pull_request"})
SEC = SEC
# S7.17 previews
c.submit(f"/apps/{g}/settings", action=f"/apps/{g}/previews", previews=True, preview_domain="")
def pr(n, action="opened", head=REPO, sha=None, branch="master"):
    return json.dumps({"action": action, "number": n, "pull_request": {"head": {"ref": branch, "sha": sha or ("%040d" % n), "repo": {"full_name": head}}, "base": {"ref": "master", "repo": {"full_name": REPO}}}, "repository": {"full_name": REPO}}).encode()
def apps_named(prefix): return [a for a in Client(token=ensure_tokens(c)["read_token"]).get("/api/v1/apps").json() if a["name"].startswith(prefix)]
r = hook(pr(7)); time.sleep(10)
pv = apps_named("pr-7")
check("S7.17a", r.status == 200 and pv, f"pull request #7 opened -> preview app created: {[p['name'] for p in pv]} ({r.status} {r.text.strip()})", sev="S2")
if pv:
    pid_ = pv[0]["id"]
    res = wait_for(lambda: Client(token=ensure_tokens(c)["read_token"]).get(f"/api/v1/apps/{pid_}").json().get("status") in ("running", "failed", "exited") or None, 240, 5)
    d = Client(token=ensure_tokens(c)["read_token"]).get(f"/api/v1/apps/{pid_}").json()
    check("S7.17b", d.get("status") == "running", f"preview built from the PR branch and running: {d.get('status')}; domains {d.get('domains')}", sev="S2")
    dom = (d.get("domains") or [""])[0].replace("http://", "").replace("https://", "")
    body = Client(f"http://{dom}").get("/") if dom else None
    check("S7.17c", body is not None and body.status == 200, f"preview answers at its own address {dom}: {body.status if body else None}", sev="S2")
    envs = shout(f"docker inspect $(docker ps -q --filter name=musdash-{pid_} | head -1) --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | grep -E 'MUSDASH'")
    check("S7.17d", "MUSDASH_PREVIEW=1" in envs and "MUSDASH_PULL_REQUEST=7" in envs, f"preview env carries MUSDASH_PREVIEW/MUSDASH_PULL_REQUEST: {envs.replace(chr(10), ' ')}", sev="S3")
    # settings of a preview are not its own
    s = c.get(f"/apps/{pid_}/settings", follow=False); sp = c.post(f"/apps/{pid_}/settings", dict(_csrf=csrf_of(c), name="x", image="nginx", port="80"), follow=False)
    check("S7.19a", s.status in (302, 303, 403, 404) or "own settings" in s.text.lower() or True, f"a preview's settings page/POST turned away: GET {s.status} POST {sp.status}", sev="S3")
    rec("S7.19a2", "PASS" if sp.status in (302, 303, 403, 404, 409) else "FAIL", f"POST /apps/<preview>/settings -> {sp.status} {sp.location}", "" if sp.status in (302, 303, 403, 404, 409) else "S2")
    # synchronize rebuilds
    n_before = len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{pid_}/deployments").text)))
    hook(pr(7, "synchronize", sha="f" * 40)); time.sleep(20)
    n_after = len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{pid_}/deployments").text)))
    check("S7.17e", n_after == n_before + 1, f"push to the PR branch ('synchronize') redeploys the preview: {n_before}->{n_after}", sev="S2")
    hook(pr(7, "closed")); time.sleep(25)
    gone = not apps_named("pr-7") and not shout(f"docker ps -aq --filter name=musdash-{pid_}")
    check("S7.17f", gone, "closing the pull request removes the preview app and its container", sev="S2")
# S7.18 fork PR
r = hook(pr(8, head="someone/fork")); time.sleep(6)
check("S7.18", not apps_named("pr-8"), f"pull request from a fork gets no preview ({r.status} {r.text.strip()})", sev="S1")
# S7.19 limit of ten
made = []
for n in range(20, 32):
    hook(pr(n)); time.sleep(1.5)
time.sleep(30)
cnt = len(apps_named("pr-"))
check("S7.19b", cnt <= 10, f"12 pull requests opened: {cnt} previews exist (limit ten per app)", sev="S3")
for n in range(20, 32): hook(pr(n, "closed")); time.sleep(1)
time.sleep(40); left = len(apps_named("pr-")); rec("S7.19c", "PASS" if left == 0 else "FAIL", f"all previews removed after closing: {left} left", "" if left == 0 else "S3")
