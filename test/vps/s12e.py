from lib_b import *
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"; R = state()["b_remote"]
f = [x for x in parse_forms(c.get(f"/projects/{P}/apps/new?env={E}&source=image", follow=True).text) if x["action"] == f"/projects/{P}/apps"][0]
rr = c.post_form(f, follow=True, name="t2-remote-app", image="nginx:alpine", port="80", domain="", server=R, deploy=True)
A = re.search(r"/apps/([a-z2-7]{12})", rr.url).group(1); save_state(b_remote_app=A)
logf = lambda: shout(f"ls -t /var/lib/musdash/logs/deployments/*.log | xargs grep -l 'nginx:alpine' 2>/dev/null | head -1")
l = wait_for(lambda: (lambda t: t if re.search(r"Deployed\.|Failed:", t) else None)(shout(f"cat $(ls -t /var/lib/musdash/logs/deployments/*.log | head -1)")), 240, 5) or "(timeout)"
print(l[-500:])
rem = shout("docker exec t2-remote docker ps --format '{{.Names}} {{.Image}} {{.Status}} {{.Ports}}'")
loc = shout(f"docker ps --format '{{{{.Names}}}}' | grep -c {A}")
check("S12.9a", A in rem and loc == "0", f"app runs on the remote's Docker, not on the main host: remote={rem!r} host matches={loc}")
# where does the data go on the remote
print(shout("docker exec t2-remote sh -c 'ls -la /var/lib/musdash /var/lib/musdash/apps 2>&1 | head -20'"))
