from lib import *
import sys
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; g = st["git"]
# S7.3 build-time variables: given to the build, not to the running app, not in the process list
c.submit(f"/apps/{g}/environment", action=f"/apps/{g}/environment", vars="RUN_ONLY=run-value-1", build_vars="BUILD_ONLY=build-secret-7")
sh("rm -f /tmp/pslog3; nohup sh -c 'for i in $(seq 1 150); do ps -eo args >> /tmp/pslog3; sleep 0.2; done' >/dev/null 2>&1 &")
d = deploy(c, g); res = dep_wait(c, g, d, 300); time.sleep(3)
envs = shout(f"docker inspect $(docker ps -q --filter name=musdash-{g} | head -1) --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}'")
ps = shout("grep -c 'build-secret-7' /tmp/pslog3"); hist = shout(f"docker history --no-trunc $(docker ps --filter name=musdash-{g} --format '{{{{.Image}}}}' | head -1) | grep -c 'build-secret-7' || true")
check("S7.3", res == "success" and "RUN_ONLY=run-value-1" in envs and "BUILD_ONLY" not in envs and ps.strip() == "0", f"runtime variable reaches the app, build-time one does not ({res}); build secret in process list: {ps.strip()} hits", sev="S2")
log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{g}/deployments/{d}").text)); rec("S7.3b", "INFO", "build log: " + log[170:520])
c.submit(f"/apps/{g}/environment", action=f"/apps/{g}/environment", vars="", build_vars="")
def build_app(name, pack, extra_build="", port="5000", repo="https://github.com/heroku/node-js-sample", t=1500):
    r = c.submit(f"/projects/{pid}/apps/new?env={env}&source=git", action=f"/projects/{pid}/apps", name=name, access="public", repo=repo, branch="master", build_pack=pack, base_dir="", dockerfile_path="", publish_dir="", auto_deploy=False, port=port, domain=f"{name}.{HOST}.sslip.io", deploy=False)
    a = re.search(r"/apps/([a-z2-7]{12})", r.url or "").group(1)
    c.submit(f"/apps/{a}/environment", action=f"/apps/{a}/environment", vars="", build_vars=extra_build)
    t0 = time.time(); d = deploy(c, a); res = dep_wait(c, a, d, t)
    log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{a}/deployments/{d}").text))
    return a, d, res, time.time() - t0, log
for pack, name, bv in [("railpack", "t-railpack", "RAILPACK_NODE_VERSION=20"), ("nixpacks", "t-nixpacks", "NIXPACKS_NODE_VERSION=20")]:
    if len(sys.argv) > 1 and pack not in sys.argv[1:]: continue
    a, d, res, dt, log = build_app(name, pack, bv)
    body = Client(f"http://{name}.{HOST}.sslip.io").get("/") if res == "success" else None
    check("S7.7" if pack == "nixpacks" else "S7.8", res == "success" and body is not None and "Hello World" in body.text, f"{pack}: built a repo with no Dockerfile in {dt:.0f}s ({res}); app answers {body.status if body else None} {body.text[:30] if body else ''!r}; log: {log[170:420]}", sev="S2")
    save_state(**{name.replace('-', '_'): a})
    sh("docker builder prune -f >/dev/null 2>&1")
