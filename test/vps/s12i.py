from lib_b import *
import base64
c = owner_client(); S = state()["b_remote"]; A = state()["b_remote_app"]
def msg(rr):
    ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", "")); return base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
fd = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/delete"][0]
print([x["name"] for x in fd["fields"]])
before = shout("docker exec t2-remote sh -c 'docker ps -q | wc -l; ls /var/lib/musdash | tr \"\\n\" \" \"'")
rr = c.post_form(fd, follow=False); m1 = msg(rr); print(rr.status, m1)
still = "t2-remote" in flash(c.get("/servers", follow=True))[:3000] and f"/servers/{S}/check" in c.get("/servers", follow=True).text
print("server still listed:", still)
