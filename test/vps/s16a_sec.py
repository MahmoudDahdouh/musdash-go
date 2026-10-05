from lib import *
import threading
c = owner_client(); st = state(); web = st["web"]; who = st["who"]; pid = st["proj"]; env = st["env"]
d = deploy(c, web); dep_wait(c, web, d)
def settings(**kw):
    base = dict(name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
    base.update(kw); return c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/settings", **base)
def field(name, page=None):
    m = re.search(r'name="%s"[^>]*value="([^"]*)"' % name, page or c.get(f"/apps/{web}/settings").text); return m.group(1) if m else None
# S16.1 app names that reach container names / DNS
bad_names = ["t;id", "t web", "T-UPPER", "../x", "-leading", "a" * 80, "t$(id)", "t`id`", "t\nx", "ünï", "t_underscore", "xn--a", "con"]
res = {}
for n in bad_names:
    r = c.submit(f"/projects/{pid}/apps/new?env={env}", action=f"/projects/{pid}/apps", name=n, image="nginx:alpine", port="80", domain="", deploy=False)
    ok = r.status == 200 and "/apps/" in r.url
    res[n[:20]] = "ACCEPTED" if ok else r.status
    if ok:
        aid = re.findall(r"/apps/([a-z2-7]{12})", r.url)[0]; c.post(f"/apps/{aid}/delete", dict(_csrf=csrf_of(c, f"/apps/{aid}"), confirm=n))
rec("S16.1a", "INFO", "app name handling: " + json.dumps(res, ensure_ascii=False))
acc = [k for k, v in res.items() if v == "ACCEPTED"]
check("S16.1b", not [k for k in acc if any(ch in k for ch in ";$`\n /")], f"app names with shell metacharacters/space/newline refused; accepted: {acc}", sev="S1")
# health command / start command: execute only inside the container
sh("rm -f /tmp/pwned-host /tmp/pwned-host2")
settings(health_path="", health_cmd="true; touch /tmp/pwned-host; echo $(id) >/tmp/in-container")
d = deploy(c, web); dep_wait(c, web, d)
check("S16.1c", shout("test -e /tmp/pwned-host && echo HOST-PWNED || echo ok") == "ok", "health command with `;` runs inside the container only (no file on the host)", sev="S1")
settings(health_cmd="", health_path="/", start_command="nginx -g 'daemon off;' ; touch /tmp/pwned-host2")
d = deploy(c, web); dep_wait(c, web, d)
check("S16.1d", shout("test -e /tmp/pwned-host2 && echo HOST-PWNED || echo ok") == "ok", "start command with `;` stays in the container", sev="S1")
settings()
# domains
doms = ["evil.com; id", "a b.com", "localhost", "127.0.0.1", "10.0.0.5", "*.example.com", "-bad.example.com", "x" * 260 + ".com", "ünï.example.com", "example.com:8080", "user@example.com", "http://example.com", "example.com/path", "EXAMPLE.COM", "example.com.", "a..b.com", "[::1]", "nodot", "t-web.168.235.65.204.sslip.io"]
res = {}
for dm in doms:
    before = len(re.findall(r"/domains/[a-z2-7]{12}/delete", c.get(f"/apps/{web}/settings").text))
    c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/domains", host=dm, tls=False)
    pg = c.get(f"/apps/{web}/settings").text; after = len(re.findall(r"/domains/[a-z2-7]{12}/delete", pg))
    res[dm[:30]] = after > before
    for f in parse_forms(pg):
        if f["action"].startswith(f"/apps/{web}/domains/") and f["action"].endswith("/delete") and after > before and dm.lower().split("/")[0] in pg[pg.find(f["action"]) - 600: pg.find(f["action"])].lower(): c.post_form(f); break
acc = [k for k, v in res.items() if v]
bad_acc = [k for k in acc if k in ("evil.com; id", "a b.com", "localhost", "127.0.0.1", "*.example.com", "-bad.example.com", "ünï.example.com", "example.com:8080", "user@example.com", "http://example.com", "example.com/path", "a..b.com", "[::1]", "nodot") or len(k) > 253]
check("S16.2", not bad_acc, f"invalid/unsafe domains refused. Accepted: {acc}; wrongly accepted: {bad_acc}", sev="S2", evidence=res)
