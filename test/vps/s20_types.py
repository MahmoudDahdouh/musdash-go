from lib_d import *
import sys
c = owner_client(); H = lambda n: f"{n}.{HOST}.sslip.io"
cases = [  # name, kwargs, case id, expected marker in body (or None), expected outcome
 ("t-nginx2", dict(source="image", image="nginx:alpine", port="80"), "D1 image nginx:alpine", "nginx", "success"),
 ("t-static", dict(source="git", repo="https://github.com/MahmoudDahdouh/simple-page", branch="main", pack="static", port="80"), "D2 git public, static pack", "<", "success"),
 ("t-landing", dict(source="git", repo="https://github.com/MahmoudDahdouh/Simple-Landing-page", branch="master", pack="static", port="80", spa=True), "D3 git public, static + SPA fallback", "<", "success"),
 ("t-sio-docker", dict(source="git", repo="https://github.com/MahmoudDahdouh/socket.io-chat", branch="main", pack="dockerfile", port="3000"), "D4 git public, Dockerfile pack on a repo with none (must fail cleanly)", None, "failed"),
 ("t-sio-nixpacks", dict(source="git", repo="https://github.com/MahmoudDahdouh/socket.io-chat", branch="main", pack="nixpacks", port="3000"), "D5 git public, Nixpacks (Node)", "socket", "success"),
 ("t-sio-railpack", dict(source="git", repo="https://github.com/MahmoudDahdouh/socket.io-chat", branch="main", pack="railpack", port="3000"), "D6 git public, Railpack (Node)", "socket", "success"),
]
only = sys.argv[1:]
for name, kw, cid, marker, want in cases:
    if only and name not in only: continue
    t0 = time.time(); r = new_app(c, name, **kw)
    m = re.search(r"/apps/([a-z2-7]{12})/deployments/([a-z2-7]{12})", r.url or "")
    if not m:
        rec(cid, "FAIL", f"{name}: create refused: {flash(r)[:300]}", "S2"); continue
    aid, dep = m.groups()
    res = dep_wait(c, aid, dep, 900) or "timeout"
    log = dep_log(c, aid, dep)
    ok = res == ("success" if want == "success" else "failed")
    body = ""
    if want == "success" and ok:
        body = serve(H(name))
        ok = body.startswith("200") and (marker is None or marker.lower() in body.lower())
    check(cid, ok, f"{name}: deployment {res} in {int(time.time()-t0)}s; served: {body[:90]!r}", "S2", evidence=log[-700:])
    save_state(**{"d_" + name: aid})
    print("   log tail:", log[-260:])
    sh("docker image prune -f >/dev/null 2>&1")
