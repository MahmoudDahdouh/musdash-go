from lib import *
import threading
c = owner_client(); st = state(); aid = st["web"]; host = f"t-web.{HOST}.sslip.io"

# S5.5 zero dropped requests during a redeploy
sh("rm -f /tmp/hits; nohup sh -c 'for i in $(seq 1 600); do curl -s -m 3 -o /dev/null -w \"%{http_code}\\n\" -H \"Host: " + host + "\" http://127.0.0.1/; sleep 0.05; done > /tmp/hits' >/dev/null 2>&1 &")
time.sleep(2)
d = deploy(c, aid); res = dep_wait(c, aid, d)
time.sleep(max(0, 3))
sh("pkill -f 'seq 1 600' ; true"); time.sleep(1)
hits = shout("sort /tmp/hits | uniq -c")
bad = sum(int(l.split()[0]) for l in hits.splitlines() if l.split()[1] != "200")
tot = sum(int(l.split()[0]) for l in hits.splitlines())
check("S5.5", res == "success" and tot > 100 and bad == 0, f"redeploy under load: {tot} requests, {bad} not-200 ({hits.replace(chr(10), '; ')})", sev="S2")

# S5.6 failed deploy keeps the old container serving
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:this-tag-does-not-exist-9", name="t-web")
before = shout("docker ps --format '{{.Names}} {{.Image}}' | grep " + aid)
d = deploy(c, aid); res = dep_wait(c, aid, d)
after = shout("docker ps -a --format '{{.Names}} {{.Image}} {{.Status}}' | grep " + aid)
out = vcurl("http://127.0.0.1/", host)
check("S5.6", res == "failed" and out.startswith("200") and len(after.splitlines()) == 1, f"bad image tag: deployment {res}; old still serving ({out[:12]!r}); containers: {after!r}", sev="S2")
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", name="t-web")

# S5.7 health check path
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", health_path="/definitely-missing", health_timeout="15")
d = deploy(c, aid); res = dep_wait(c, aid, d)
out = vcurl("http://127.0.0.1/", host)
check("S5.7a", res == "failed" and out.startswith("200"), f"health path that returns 404 fails the deploy ({res}); old serves ({out[:12]!r})", sev="S2")
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", health_path="/", health_timeout="30")
d = deploy(c, aid); res = dep_wait(c, aid, d); check("S5.7b", res == "success", f"health path / passes ({res})")
# S5.8 health command and port-only
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", health_path="", health_cmd="false", health_timeout="15")
d = deploy(c, aid); res = dep_wait(c, aid, d); check("S5.8a", res == "failed", f"health command `false` fails the deploy ({res})", sev="S2")
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", health_path="", health_cmd="test -f /etc/nginx/nginx.conf", health_timeout="30")
d = deploy(c, aid); res = dep_wait(c, aid, d); check("S5.8b", res == "success", f"health command `test -f …` passes ({res})")
c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", image="nginx:alpine", health_path="", health_cmd="", health_timeout="30")
d = deploy(c, aid); res = dep_wait(c, aid, d); check("S5.8c", res == "success", f"port-only check ({res})")

# S5.9 stop / start
c.post(f"/apps/{aid}/stop", dict(_csrf=csrf_of(c, f"/apps/{aid}")))
time.sleep(4)
out = vcurl("http://127.0.0.1/", host); run = shout("docker ps -q --filter name=musdash-" + aid)
page = re.sub(r"<[^>]+>", " ", c.get(f"/apps/{aid}").text)
check("S5.9a", not run and not out.startswith("200") and "Stopped" in page, f"stopped: container gone={not run}, proxy answers {out[:3]!r}, page shows Stopped={'Stopped' in page}")
d = deploy(c, aid); res = dep_wait(c, aid, d)
check("S5.9b", res == "success" and vcurl("http://127.0.0.1/", host).startswith("200"), f"deploy after stop serves again ({res})")
