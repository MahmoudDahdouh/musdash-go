from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; API = Client(token=ensure_tokens(c)["read_token"])
def find(n):
    m = [x for x in API.get("/api/v1/apps").json() if x["name"] == n]; return m[0]["id"] if m else None
def build_app(name, pack, bv, port="5000"):
    a = find(name)
    if not a:
        r = c.submit(f"/projects/{pid}/apps/new?env={env}&source=git", action=f"/projects/{pid}/apps", name=name, access="public", repo="https://github.com/heroku/node-js-sample", branch="master", build_pack=pack, base_dir="", dockerfile_path="", publish_dir="", auto_deploy=False, port=port, domain=f"{name}.{HOST}.sslip.io", deploy=False)
        a = re.search(r"/apps/([a-z2-7]{12})", r.url or "").group(1)
        c.submit(f"/apps/{a}/environment", action=f"/apps/{a}/environment", vars="", build_vars=bv)
    t0 = time.time(); d = deploy(c, a); res = dep_wait(c, a, d, 1500)
    tail = shout(f"tail -5 /var/lib/musdash/logs/deployments/{d}.log | cut -c1-200")
    body = Client(f"http://{name}.{HOST}.sslip.io").get("/") if res == "success" else None
    check("S7.8" if pack == "railpack" else "S7.7", res == "success" and body is not None and "Hello World" in body.text, f"{pack}: built a repo with no Dockerfile in {time.time()-t0:.0f}s ({res}); app answers {body.status if body else None} {(body.text[:20] if body else '')!r}; tail: {tail[-150:]!r}", sev="S2")
    sh("docker builder prune -f >/dev/null 2>&1")
build_app("t-railpack", "railpack", "RAILPACK_NODE_VERSION=20")
build_app("t-nixpacks", "nixpacks", "NIXPACKS_NODE_VERSION=20")
