from lib_b import *
c = owner_client(); S = state()["b_remote"]
real = shout("docker exec t2-remote sh -c 'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub'")
page = flash(c.get("/servers", follow=True)); shown = re.search(r"Host key (SHA256:\S+)", page).group(1)
check("S12.6", shown in real, f"fingerprint shown {shown} matches `ssh-keygen -lf` on the server: {real}")
check("S12.1r", "Docker 29.8.2 on linux/amd64" in page, "remote card shows Docker version and architecture")
# S12.7 host key change
sh("docker exec t2-remote sh -c 'pkill sshd; rm /etc/ssh/ssh_host_ed25519_key*; ssh-keygen -q -t ed25519 -f /etc/ssh/ssh_host_ed25519_key -N \"\"; /usr/sbin/sshd'")
time.sleep(3)
f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/check"][0]
c.post_form(f, follow=False)
page = flash(c.get("/servers", follow=True)); i = page.find("t2-remote"); seg = page[i:i+320]
check("S12.7a", not re.search(r"t2-remote Ready", seg), f"changed host key is refused: {seg}")
forms = parse_forms(c.get("/servers", follow=True).text); print([f["action"] for f in forms if S in f["action"]])
fg = [x for x in forms if x["action"].startswith(f"/servers/{S}/") and "forget" in x["action"]]
if fg:
    c.post_form(fg[0], follow=False)
    f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/check"][0]; c.post_form(f, follow=False)
    page = flash(c.get("/servers", follow=True)); i = page.find("t2-remote"); seg = page[i:i+300]
    check("S12.7b", "Ready" in seg, f"after Forget host key, Check records the new key: {seg[:200]}")
