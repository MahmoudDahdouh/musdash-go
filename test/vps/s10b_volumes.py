from lib import *
exec(open("s10a_services.py").read().split("REFUSE = {")[0])
VOLNAME = "musdash-db-pkzpuk2776jp-data"   # my t-pg database's data volume
tests = {
 "external named volume (another resource's data)": f"services:\n  a:\n    image: nginx:alpine\n    volumes: [v:/d]\nvolumes:\n  v:\n    external: true\n    name: {VOLNAME}\n",
 "volume name clash (declare same name)": f"services:\n  a:\n    image: nginx:alpine\n    volumes: [v:/d]\nvolumes:\n  v:\n    name: {VOLNAME}\n",
 "driver_opts bind to host path": "services:\n  a:\n    image: nginx:alpine\n    volumes: [v:/d]\nvolumes:\n  v:\n    driver: local\n    driver_opts:\n      type: none\n      o: bind\n      device: /etc\n",
 "container_name collision with an app": "services:\n  a:\n    image: nginx:alpine\n    container_name: musdash-db-pkzpuk2776jp\n",
 "network alias of another app": "services:\n  a:\n    image: nginx:alpine\n    networks:\n      default:\n        aliases: [t-web]\n",
 "tmpfs/volume with host path via long syntax": "services:\n  a:\n    image: nginx:alpine\n    volumes:\n      - type: volume\n        source: /etc\n        target: /x\n",
}
for label, compose in tests.items():
    sid, r = new_service("t-vol%d" % (abs(hash(label)) % 1000), compose)
    if not sid: print(f"{label:55} -> refused at save: {flash(r)[-100:]}"); rec("S10.4v", "PASS", f"{label}: refused at save"); continue
    s = settle(sid, 150); txt = svc_state(sid); log = shout(f"tail -4 /var/lib/musdash/logs/services/{sid}*.log 2>/dev/null | cut -c1-160")
    mounts = shout(f"for c in $(docker ps -q --filter name=musdash-{sid}); do docker inspect $c --format '{{{{json .Mounts}}}}'; done | cut -c1-300")
    hit = VOLNAME in mounts or '"/etc"' in mounts
    print(f"{label:55} -> {s} | mounts {mounts[:120]} | {log[-110:]}")
    check("S10.4v", not hit and s != "Running" or (s == "Running" and not hit), f"{label}: {s}; mounted another resource's volume or host path: {hit}", sev="S1")
    drop(sid)
