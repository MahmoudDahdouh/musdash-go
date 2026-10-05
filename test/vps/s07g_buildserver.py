from lib import *
c = owner_client(); st = state(); g = st["git"]; sid = st["remote"]
bf = [f for f in parse_forms(c.get(f"/apps/{g}/settings").text) if f["action"].endswith("/build-server")][0]
c.post_form(bf, build_server=sid)
before = shout("docker exec t-remote docker images --format '{{.Repository}}:{{.Tag}}' | grep -c . || true")
d = deploy(c, g); res = dep_wait(c, g, d, 600); log = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/apps/{g}/deployments/{d}").text))
raw = shout(f"cat /var/lib/musdash/logs/deployments/{d}.log | cut -c1-160 | grep -iE 'build|save|load|server|image' | head -12")
onremote = shout("docker exec t-remote docker images --format '{{.Repository}}:{{.Tag}}' | grep musdash/ || true")
onlocal = shout(f"docker images --format '{{{{.Repository}}}}:{{{{.Tag}}}}' | grep {g} | head -3")
running_local = shout(f"docker ps --format '{{{{.Names}}}}' | grep -c {g}")
body = Client(f"http://t-git.{HOST}.sslip.io").get("/")
check("S7.20", res == "success" and running_local.strip() == "1" and body.status == 200, f"built on the remote ({onremote!r}), moved with docker save|load and run on the app's own server ({onlocal!r}); status {res}; app answers {body.status}. log: {raw[:260]!r}", sev="S2")
bf = [f for f in parse_forms(c.get(f"/apps/{g}/settings").text) if f["action"].endswith("/build-server")][0]; c.post_form(bf, build_server="")
