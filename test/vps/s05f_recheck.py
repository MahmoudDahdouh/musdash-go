from lib import *
c = owner_client(); web = state()["web"]; host = f"t-web.{HOST}.sslip.io"
t0 = shout("date '+%Y-%m-%d %H:%M:%S'")
def settings(**kw):
    base = dict(name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
    base.update(kw); return c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/settings", **base)
settings(health_path="", health_cmd="", health_timeout="30"); d = deploy(c, web); r = dep_wait(c, web, d, 200)
log = shout(f"tail -4 /var/lib/musdash/logs/deployments/{d}.log | cut -c1-160")
restarts = shout(f"journalctl -u musdash-server --since '{t0}' --no-pager | grep -c 'Started musdash-server'").strip()
check("S5.8c", r == "success", f"port-only check passes on an undisturbed clean build ({r}; server restarts in window: {restarts}); log tail {log[-120:]!r}", sev="S2")
settings(); d = deploy(c, web); dep_wait(c, web, d, 200)
c.post(f"/apps/{web}/stop", dict(_csrf=csrf_of(c, f"/apps/{web}"))); time.sleep(5)
left = shout(f"docker ps -q --filter name=musdash-{web} | wc -l").strip(); out = vcurl("http://127.0.0.1/", host)
check("S5.9a", left == "0" and not out.startswith("200"), f"Stop removes the container ({left} left) and the proxy stops serving ({out[:3]!r}); server restarts in window: {shout(f'journalctl -u musdash-server --since {chr(39)}{t0}{chr(39)} --no-pager | grep -c Started').strip()}", sev="S2")
d = deploy(c, web); dep_wait(c, web, d, 200)
