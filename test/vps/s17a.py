from lib_b import *
c = owner_client(); A = state()["b_app"]
def setvars(path, text):
    f = [x for x in parse_forms(c.get(path, follow=True).text) if x["action"] == path][0]; return c.post_form(f, vars=text)
def status():
    t = flash(c.get(f"/apps/{A}", follow=True)); m = re.search(r"t2-app t2-app (\w[\w ]*?) (?:nginx|Deploy|Stop|Redeploy)", t); return m.group(1) if m else t[170:230]
def deploy():
    f = [x for x in parse_forms(c.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
    n0 = shout("ls /var/lib/musdash/logs/deployments | wc -l"); c.post_form(f, follow=False)
    wait_for(lambda: shout("ls /var/lib/musdash/logs/deployments | wc -l") != n0, 30, 1)
    logf = shout("ls -t /var/lib/musdash/logs/deployments/*.log | head -1")
    wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"tail -2 {logf}")) and 1, 90, 3)
    return shout(f"tail -2 {logf}")
setvars(f"/apps/{A}/environment", "A_PLAIN=plain\n")
print(deploy()); time.sleep(3)
print("status after deploy:", status(), "| container:", shout(f"docker ps --filter name=musdash-{A} --format '{{{{.Names}}}} {{{{.Status}}}}'"))
