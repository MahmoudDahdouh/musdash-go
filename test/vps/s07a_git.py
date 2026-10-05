from lib import *
import hmac, hashlib
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
REPO = "https://github.com/crccheck/docker-hello-world"
def new_git(name, **kw):
    f = dict(access="public", repo=REPO, branch="master", build_pack="dockerfile", base_dir="", dockerfile_path="", publish_dir="", spa_fallback=False, auto_deploy=True, port="8000", domain=f"{name}.{HOST}.sslip.io", deploy=True)
    f.update(kw)
    r = c.submit(f"/projects/{pid}/apps/new?env={env}&source=git", action=f"/projects/{pid}/apps", name=name, **f)
    m = re.search(r"/apps/([a-z2-7]{12})", r.url or "") or re.search(r"/apps/([a-z2-7]{12})", r.text)
    d = re.search(r"/deployments/([a-z2-7]{12})", r.url or "")
    return (m.group(1) if m else None), (d.group(1) if d else None), r
# S7.4 validation first (no builds)
bad_repos = {"leading dash": "-x", "ext transport": "ext::sh -c id", "file url": "file:///etc", "space": "https://github.com/a/b c", "semicolon": "https://github.com/a/b;id", "backtick": "https://github.com/a/`id`", "creds in url": "https://user:pass@github.com/a/b",
             "http plain": "http://github.com/a/b", "git proto": "git://github.com/a/b", "query": "https://github.com/a/b?x=1", "newline": "https://github.com/a/b\nx", "ssh opts": "ssh://host/-oProxyCommand=id", "scp opt": "-oProxyCommand=id@h:r"}
res = {}
for k, u in bad_repos.items():
    a, d, r = new_git("t-badrepo", repo=u, deploy=False); res[k] = bool(a)
    if a: c.post(f"/apps/{a}/delete", dict(_csrf=csrf_of(c, f"/apps/{a}"), confirm="t-badrepo"))
check("S7.4a", not any(res.values()), f"hostile repository URLs refused at the form: accepted={[k for k, v in res.items() if v]}", sev="S1")
bad_br = ["-x", "--upload-pack=id", "$(id)", "a;b", "../x", "a b", "a\nb", "@{", "a..b", "x" * 300]
res = {}
for b in bad_br:
    a, d, r = new_git("t-badbranch", branch=b, deploy=False); res[b[:12]] = bool(a)
    if a: c.post(f"/apps/{a}/delete", dict(_csrf=csrf_of(c, f"/apps/{a}"), confirm="t-badbranch"))
check("S7.4b", not any(res.values()), f"hostile branch names refused: accepted={[k for k, v in res.items() if v]}", sev="S1")
res = {}
for lab, kw in {"base ..": dict(base_dir="../../etc"), "base abs": dict(base_dir="/etc"), "dockerfile ..": dict(dockerfile_path="../../etc/passwd"), "dockerfile abs": dict(dockerfile_path="/etc/passwd"), "publish ..": dict(build_pack="static", publish_dir="../x"), "base dash": dict(base_dir="-x"), "base colon": dict(base_dir=":(glob)*"), "dockerfile nl": dict(dockerfile_path="a\nb")}.items():
    a, d, r = new_git("t-badpath", deploy=False, **kw); res[lab] = bool(a)
    if a: c.post(f"/apps/{a}/delete", dict(_csrf=csrf_of(c, f"/apps/{a}"), confirm="t-badpath"))
check("S7.5", not any(res.values()), f"path traversal in base dir / Dockerfile / publish dir refused: accepted={[k for k, v in res.items() if v]}", sev="S1")
# S7.1 Dockerfile build
t0 = time.time(); a, d, r = new_git("t-git")
check("S7.1a", a and d, f"git app created and a deployment started ({a}/{d})")
res = dep_wait(c, a, d, 600); log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{a}/deployments/{d}").text))
check("S7.1b", res == "success", f"clone -> symlink check -> docker build -> start: {res} in {time.time()-t0:.0f}s; log: {log[170:520]}", sev="S2")
body = Client(f"http://t-git.{HOST}.sslip.io").get("/"); check("S7.1c", body.status == 200 and "html" in body.text.lower(), f"built app serves through the proxy: {body.status} {body.text[:60]!r}")
rec("S7.1d", "INFO", "build timing: %.0fs; commit shown: %s" % (time.time() - t0, re.findall(r"Commit\s+([0-9a-f]{7,40})", re.sub('<[^>]+>', ' ', c.get(f'/apps/{a}').text))[:1]))
save_state(git=a)
# S7.2 static pack
a2, d2, r = new_git("t-static", build_pack="static", publish_dir=".", port="80", auto_deploy=False)
res = dep_wait(c, a2, d2, 600); b2 = Client(f"http://t-static.{HOST}.sslip.io").get("/")
check("S7.2", res == "success" and b2.status == 200, f"static-site pack serves the folder via nginx: {res}, {b2.status}, {b2.h('server')}", sev="S2")
save_state(static=a2)
