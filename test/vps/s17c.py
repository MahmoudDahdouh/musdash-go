exec(open("s17a.py").read().split("setvars(f\"/apps/{A}/environment\"")[0])
n = shout(f"docker ps --format '{{{{.Names}}}}' | grep musdash-{A}")
print("container", n, status())
pid = shout(f"docker inspect {n} --format '{{{{.State.Pid}}}}'"); shout(f"kill -9 {pid}")   # a crash: the main process is killed from the host
seen = []
for i in range(25):
    seen.append((status(), shout(f"docker ps -a --filter name=musdash-{A} --format '{{{{.Status}}}}'")[:30])); time.sleep(2)
uniq = [s for i, s in enumerate(seen) if i == 0 or s != seen[i - 1]]; print(uniq)
up = shout(f"docker ps --filter name=musdash-{A} --format '{{{{.Status}}}}'")
check("S17.4c", up.startswith("Up") and status() == "Running", f"crash of PID 1: restart policy brings it back ({up}); app status ends {status()}; statuses {[u[0] for u in uniq]}")
