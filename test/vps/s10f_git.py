from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
exec(open("s10a_services.py").read().split("REFUSE = {")[0].split("def new_service")[0])
def settle(sid, t=300):
    R = Client(token=ensure_tokens(c)["read_token"])
    return wait_for(lambda: (R.get(f"/api/v1/services/{sid}").json().get("status") if R.get(f"/api/v1/services/{sid}").json().get("status") in ("running", "failed", "stopped", "exited") else None), t, 5)
# S10.7 variables box and missing variable
sid2, r = None, None
r = c.submit(f"/projects/{pid}/services/new?env={env}&template=custom", action=f"/projects/{pid}/services", name="t-vars", compose="services:\n  a:\n    image: nginx:alpine\n    environment:\n      - GREETING=${GREETING}\n      - OPT=${OPTIONAL:-fallback}\n", variables="", connect_env=False, deploy=True)
sid2 = re.search(r"/services/([a-z2-7]{12})", r.url or r.text).group(1); s2 = settle(sid2, 120)
txt = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/services/{sid2}").text)); lg = shout(f"tail -3 /var/lib/musdash/logs/services/{sid2}*.log | cut -c1-200")
check("S10.7a", s2 in ("failed", "running") and ("GREETING" in txt or "GREETING" in lg), f"a ${{NAME}} with no value is reported by name ({s2}): {lg[-160:]!r}", sev="S3")
c.submit(f"/services/{sid2}/compose", action=f"/services/{sid2}/compose", variables="GREETING=hello-from-vars", connect_env=False, submit={"name": "deploy", "value": ""})
s3 = settle(sid2, 200); cid = shout(f"docker ps -q --filter name=musdash-{sid2} | head -1")
envv = shout(f"docker exec {cid} printenv GREETING OPT") if cid else ""
check("S10.7b", "hello-from-vars" in envv and "fallback" in envv, f"Variables box fills ${{GREETING}} and the :-default works: {envv!r} ({s3})", sev="S2")
pg = c.get(f"/services/{sid2}/settings").text; f = [x for x in parse_forms(pg) if x["action"] == f"/services/{sid2}/delete"][0]; c.post_form(f, confirm="t-vars", delete_data=True)
# S10.9 non-HTTP port
r = c.submit(f"/projects/{pid}/services/new?env={env}&template=custom", action=f"/projects/{pid}/services", name="t-port", compose="services:\n  a:\n    image: redis:7-alpine\n    ports: ['16379:6379']\n", variables="", connect_env=False, deploy=True)
sid3 = re.search(r"/services/([a-z2-7]{12})", r.url or r.text).group(1); s4 = settle(sid3, 200)
import socket
ok = False
try: sk = socket.create_connection((HOST, 16379), timeout=8); sk.close(); ok = True
except Exception as e: ok = repr(e)
check("S10.9", s4 == "running" and ok is True, f"a non-HTTP port 16379 from the allowed range is published on the server and reachable from the internet: {s4}, {ok}", sev="S2")
pg = c.get(f"/services/{sid3}/settings").text; f = [x for x in parse_forms(pg) if x["action"] == f"/services/{sid3}/delete"][0]; c.post_form(f, confirm="t-port", delete_data=True)
