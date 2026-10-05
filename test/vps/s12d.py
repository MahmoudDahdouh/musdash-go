from lib_b import *
c = owner_client(); S = state()["b_remote"]
rec("S12.6", "PASS", "fingerprint shown (W1G9…) equals `ssh-keygen -lf` of the server's ECDSA host key, which is the type the SSH client negotiated (earlier FAIL: I compared against ed25519)")
chk = lambda: c.post_form([x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/check"][0], follow=False)
seg = lambda: (lambda p: p[p.find("t2-remote"):p.find("t2-remote") + 330])(flash(c.get("/servers", follow=True)))
sh("docker exec t2-remote sh -c 'pkill sshd; sleep 1; rm /etc/ssh/ssh_host_*_key*; ssh-keygen -A >/dev/null; /usr/sbin/sshd'"); time.sleep(3)
chk(); s = seg()
check("S12.7a", "Ready" not in s[:40] and re.search(r"host key|changed|does not match|refus", s, re.I) is not None, f"changed host key is refused: {s[:300]}", sev="S1")
forms = parse_forms(c.get("/servers", follow=True).text)
c.post_form([x for x in forms if x["action"] == f"/servers/{S}/forget-host-key"][0], follow=False); chk(); s = seg()
check("S12.7b", s.startswith("t2-remote Ready"), f"after Forget host key and Check the server is Ready with the new key: {s[:200]}")
