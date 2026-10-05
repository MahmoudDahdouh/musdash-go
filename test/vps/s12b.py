from lib_b import *
import base64
c = owner_client(); S = state()["b_remote"]
h = c.get("/servers", follow=True).text
print("servers with check forms:", sorted(set(re.findall(r'action="/servers/([a-z2-7]{12})/check"', h))))
pub = re.search(r"(ssh-ed25519 AAAA[A-Za-z0-9+/=]+ musdash)", h.replace("&#43;", "+")).group(1); print(pub[:50])
sh(f"docker exec t2-remote sh -c \"echo '{pub}' > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys\"")
f = [x for x in parse_forms(h) if x["action"] == f"/servers/{S}/check"][0]
rr = c.post_form(f, follow=False)
ck = re.search(r"musdash_flash=([^;]*)", rr.headers.get("set-cookie", "")); print(base64.urlsafe_b64decode(ck.group(1) + "=" * (-len(ck.group(1)) % 4)).decode() if ck else "(no flash)")
t = flash(c.get("/servers", follow=True)); i = t.find("t2-remote"); print(t[i:i+500])
