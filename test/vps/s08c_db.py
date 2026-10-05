from lib_b import *
c = owner_client(); D = "qfoenpmqqgvg"
S = f"/databases/{D}/settings"
def settle():
    wait_for(lambda: "Running" in flash(c.get(f"/databases/{D}", follow=True)), 120)
# S8.12 injection in image tag
for bad in ["--privileged postgres:17", "postgres:17 --cap-add=ALL", "-v /:/host postgres", "postgres:17;id", "$(id)"]:
    r = c.submit(S, action=S, has="memory_mb", image=bad)
    t = flash(r)
    img = shout(f"docker inspect musdash-db-{D} --format '{{{{.Config.Image}}}} {{{{.HostConfig.Privileged}}}}'")
    check("S8.12", img == "postgres:17-alpine false", f"{bad!r}: refused, container still {img}", sev="S1", evidence=t[:300])
# S8.6 limits
r = c.submit(S, action=S, has="memory_mb", memory_mb="256", cpus="0.5", image="postgres:17-alpine")
settle(); time.sleep(3)
lim = shout(f"docker inspect musdash-db-{D} --format '{{{{.HostConfig.Memory}}}} {{{{.HostConfig.NanoCpus}}}}'")
check("S8.6", lim == "268435456 500000000", f"limits applied to container: {lim}", evidence=flash(r)[:300])
n = shout(f"docker ps -a --filter label=musdash.resource={D} --format x | wc -l")
check("S8.11b", n == "1", f"{n} container after settings change")
# S8.5 public port
r = c.submit(S, action=S, has="memory_mb", public=True, public_port="", memory_mb="256", cpus="0.5", image="postgres:17-alpine")
settle(); time.sleep(3)
ports = shout(f"docker port musdash-db-{D}")
print(ports, flash(r)[:200])
