from lib_b import *
c = owner_client(); A = state()["b_remote_app"]
sh("docker exec t2-remote sh -c \"sed -i 's/^AllowTcpForwarding no/AllowTcpForwarding yes/' /etc/ssh/sshd_config; pkill sshd; sleep 1; /usr/sbin/sshd\"")
time.sleep(2)
f = [x for x in parse_forms(c.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
n0 = shout("ls /var/lib/musdash/logs/deployments | wc -l"); c.post_form(f, follow=False)
wait_for(lambda: shout("ls /var/lib/musdash/logs/deployments | wc -l") != n0, 30, 1)
logf = shout("ls -t /var/lib/musdash/logs/deployments/*.log | head -1")
wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"tail -2 {logf}")) and 1, 150, 4)
print(shout(f"tail -4 {logf}"))
rem = shout("docker exec t2-remote docker ps --format '{{.Names}} {{.Image}} {{.Status}} {{.Ports}}'")
loc = shout(f"docker ps --format '{{{{.Names}}}}' | grep -c {A}")
check("S12.9a", A in rem and loc == "0", f"app runs on the remote's Docker, not on the main host: remote={rem!r} host matches={loc}")
