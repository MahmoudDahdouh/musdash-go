from lib_b import *
import base64
c = owner_client(); A = state()["b_app"]; S = state()["b_svc_vars"]
# Deploy all left the tag's page on 2026-10-07: the tag is deployed through the API.
D = Client(token=ensure_tokens(c)["deploy_token"])
n = lambda: (int(shout("ls /var/lib/musdash/logs/deployments | wc -l")), int(shout("ls /var/lib/musdash/logs/services | wc -l")))
b = n()
def msg(rr): return f"{rr.status} {rr.text[:120]}"
m1 = msg(D.post("/api/v1/deploy?tag=t2-grp")); m2 = msg(D.post("/api/v1/deploy?tag=t2-grp")); m3 = msg(D.post("/api/v1/deploy?tag=t2-grp"))
time.sleep(5); mid = n()
wait_for(lambda: all(re.search(r"Deployed\.|Failed:", shout(f"tail -2 $(ls -t /var/lib/musdash/logs/{d}/*.log | head -1)")) for d in ("deployments", "services")), 120, 4)
a = n()
print(m1, "|", m2, "|", m3)
check("S13.9b", (a[0] - b[0], a[1] - b[1]) == (1, 1), f"deploying the tag through the API queued the app once and the service once; further calls queued nothing new (new logs: app {a[0]-b[0]}, service {a[1]-b[1]}); messages: {m1} / {m2} / {m3}")
