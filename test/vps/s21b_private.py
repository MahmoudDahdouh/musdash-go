from lib_d import *
import sys
c = owner_client(); H = lambda n: f"{n}.{HOST}.sslip.io"
repos = [("docker-app","dockerfile","3000"),("test-deploy","nixpacks","3000")]
only = sys.argv[1:]
for repo, pack, port in repos:
    if only and repo not in only: continue
    name = "t-p2-" + repo.lower().replace("_", "-")
    r = new_app(c, name, source="git", repo=f"https://github.com/MahmoudDahdouh/{repo}", branch="main", access="source:" + state()["ghsrc"], pack=pack, port=port)
    m = re.search(r"/apps/([a-z2-7]{12})/deployments/([a-z2-7]{12})", r.url or "")
    if not m:
        rec("P " + repo, "FAIL", f"create refused: {flash(r)[-300:]}", "S2"); continue
    aid, dep = m.groups(); t0 = time.time()
    res = dep_wait(c, aid, dep, 900) or "timeout"
    log = shout(f"tail -c 900 /var/lib/musdash/logs/deployments/{dep}.log")
    body = serve(H(name), n=5) if res == "success" else ""
    rec("P " + repo, "PASS" if res == "success" and body.startswith("200") else "FAIL",
        f"private repo via GitHub App, {pack}: deployment {res} in {int(time.time()-t0)}s; served {body[:70]!r}", "" if res == "success" else "S3", evidence=log[-700:])
    print("   log:", log[-500:].replace("\n", " | "))
    save_state(**{"d_" + name: aid}); sh("docker image prune -f >/dev/null 2>&1")
