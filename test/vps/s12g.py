from lib_b import *
import base64
c = owner_client(); S = state()["b_remote"]
rec("S12.9b", "INFO", "a remote with `AllowTcpForwarding no` (Alpine's sshd default) fails the health check with 'ssh: rejected: administratively prohibited (open failed)'; README/spec/servers page do not mention the requirement")
f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/proxy"][0]
rr = c.post_form(f, follow=False)
ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", "")); msg = base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)"
print(rr.status, msg)
print(shout("docker exec t2-remote sh -c 'ls -la /var/lib/musdash/bin /etc/systemd/system 2>&1 | head; /var/lib/musdash/bin/musdash version 2>&1 | head -2'"))
