"""Helpers for the deploy-types run (2026-10-06): create apps from the redesigned forms and wait for them."""
from lib import *

def base():
    st = state(); return f"/projects/{st['dproj']}/e/{st['denv']}"

def new_app(c, name, *, source="image", image="", repo="", branch="", access="public", pack="dockerfile", port="", base_dir="", dockerfile="", publish="", spa=False, deploy_now=True, domain=True):
    d = dict(name=name, port=port, deploy=deploy_now)
    if domain: d["domain"] = f"{name}.{HOST}.sslip.io"
    if source == "image":
        d["image"] = image; page = base() + "/apps/new"
    else:
        d.update(repo=repo, branch=branch, build_pack=pack, base_dir=base_dir, dockerfile_path=dockerfile, publish_dir=publish, spa_fallback=spa, access=access)
        page = base() + "/apps/new?source=git"
    r = c.submit(page, action=base() + "/apps", **d)
    return r

def app_id(c, name):
    r = c.get(base().rsplit("/e/", 1)[0] + "/e/" + state()["denv"], follow=True)
    m = re.search(r'/apps/([a-z2-7]{12})"[^>]*>\s*(?:<[^>]+>\s*)*' + re.escape(name) + r'\b', r.text)
    return m.group(1) if m else None

def last_dep(c, aid):
    r = c.get(f"/apps/{aid}/deployments", follow=True)
    m = re.findall(r"/deployments/([a-z2-7]{12})", r.text)
    return m[0] if m else None

def dep_log(c, aid, dep):
    r = c.get(f"/apps/{aid}/deployments/{dep}", follow=True)
    return re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", r.text))

def serve(host, path="/", n=20):
    for i in range(n):
        out = vcurl("http://127.0.0.1" + path, host)
        if out.startswith(("200", "301", "302", "304")): return out
        time.sleep(3)
    return out
