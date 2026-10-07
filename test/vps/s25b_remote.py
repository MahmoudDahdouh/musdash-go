from lib_d import *
c = owner_client(); st = state(); sid = st["remote"]
def check_server():
    c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
    t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote"); return t[i:i + 800]
print(sh("docker exec t-remote docker version --format '{{.Server.Version}}'")[1])
sh("docker exec t-remote sh -c 'true'")
t = check_server(); check("S12.6c", all(w in t for w in ("Docker", "Compose")) and "git" in t.lower() and "Needs attention" not in t, f"after allowing forwarding, Check reports Docker, Compose and git: {t[:300]!r}", "S3")
# deploy an app to the remote
r, forms = c.forms(base() + "/apps/new"); f2 = c.find_form(forms, base() + "/apps")
print("fields:", [x["name"] for x in f2["fields"]])
rr = c.post_form(f2, name="t-rapp", image="nginx:alpine", port="80", domain="", deploy=True, server=sid)
m = re.search(r"/apps/([a-z2-7]{12})/deployments/([a-z2-7]{12})", rr.url or "")
if not m: rec("S12.9", "FAIL", "app on remote not created: " + flash(rr)[-250:], "S2")
else:
    aid, dep = m.groups(); res = dep_wait(c, aid, dep, 400)
    ins = shout(f"docker exec t-remote docker ps --format '{{{{.Names}}}} {{{{.Image}}}} {{{{.Ports}}}}'")
    local = shout(f"docker ps -q --filter name=musdash-{aid} | wc -l")
    check("S12.9", res == "success" and aid in ins and local == "0", f"app deployed to the remote Docker only: deployment {res}; remote has {ins!r}; local containers {local}", "S2", evidence=shout(f"tail -c 500 /var/lib/musdash/logs/deployments/{dep}.log"))
    port = re.search(r"127.0.0.1:(\d+)->", ins)
    body = shout(f"docker exec t-remote curl -s -m5 -o /dev/null -w %{{http_code}} http://127.0.0.1:{port.group(1)}/") if port else ""
    check("S12.9b", body == "200", f"remote app answers on its loopback port inside the remote server ({body})", "S2")
    rr = c.get(f"/servers")
    # server cannot be removed while it has resources
    f3 = [x for x in parse_forms(c.get("/servers").text) if x["action"] == f"/servers/{sid}/delete"]
    if f3:
        d = c.post_form(f3[0], follow=True, confirm="t-remote")
        gone = "t-remote" not in flash(c.get("/servers"))
        check("S12.12a", not gone, f"a server with an app is not removed ({flash(d)[-140:]!r})", "S2")
    save_state(remote_app=aid)
    # metrics of the remote server
    mp = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/servers/{sid}").text)) if c.get(f"/servers/{sid}").status == 200 else ""
    rec("S12.3", "INFO", f"server page for the remote: status {c.get(f'/servers/{sid}').status}; {mp[200:500]!r}")
