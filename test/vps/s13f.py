from lib_b import *
import base64
c = owner_client(); A = state()["b_app"]; S = state()["b_svc_vars"]
r = c.get("/tags/t2-grp", follow=True)
f = [x for x in parse_forms(r.text) if x["action"] == "/tags/t2-grp/deploy"][0]
n = lambda: (int(shout("ls /var/lib/musdash/logs/deployments | wc -l")), int(shout("ls /var/lib/musdash/logs/services | wc -l")))
b = n()
def msg(rr):
    ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", "")); return base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
m1 = msg(c.post_form(f, follow=False)); m2 = msg(c.post_form(f, follow=False)); m3 = msg(c.post_form(f, follow=False))
time.sleep(5); mid = n()
wait_for(lambda: all(re.search(r"Deployed\.|Failed:", shout(f"tail -2 $(ls -t /var/lib/musdash/logs/{d}/*.log | head -1)")) for d in ("deployments", "services")), 120, 4)
a = n()
print(m1, "|", m2, "|", m3)
check("S13.9b", (a[0] - b[0], a[1] - b[1]) == (1, 1), f"Deploy all queued the app once and the service once; further clicks queued nothing new (new logs: app {a[0]-b[0]}, service {a[1]-b[1]}); messages: {m1} / {m2} / {m3}")
