from lib import *
exec(open("s10a_services.py").read().split("REFUSE = {")[0])
sid, r = new_service("t-alias", "services:\n  a:\n    image: nginx:alpine\n    networks:\n      default:\n        aliases: [t-web]\n", connect=True)
settle(sid, 150)
cid = shout(f"docker ps -q --filter name=musdash-{sid} | head -1")
nets = shout(f"docker inspect {cid} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}}={{{{$v.Aliases}}}} {{{{end}}}}'")
print(nets)
envnet = [n for n in nets.split() if n.startswith("musdash-" + state()["env"])]
hij = [n for n in envnet if "t-web" in n]
check("S10.4x", not hij, f"a Compose network alias cannot shadow an app's name on the environment network: aliases per network = {nets}", sev="S3")
# resolve from another container on the environment network
net = shout("docker network ls --format '{{.Name}}' | grep musdash-" + state()["env"])
res = shout(f"docker run --rm --network {net} alpine sh -c 'for i in 1 2 3 4 5 6; do getent hosts t-web | head -1; done' | sort | uniq -c")
print("t-web resolves to:", res)
drop(sid)
