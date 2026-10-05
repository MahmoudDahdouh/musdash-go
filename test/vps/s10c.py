from lib import *
exec(open("s10a_services.py").read().split("REFUSE = {")[0])
VOLNAME = "musdash-db-pkzpuk2776jp-data"
cases = [
 ("plain named volume", "services:\n  a:\n    image: nginx:alpine\n    volumes: [data:/usr/share/nginx/html]\nvolumes:\n  data: {}\n", False),
 ("named volume with null body", "services:\n  a:\n    image: nginx:alpine\n    volumes: [data:/d]\nvolumes:\n  data:\n", False),
 ("alias hijack with environment network", "services:\n  a:\n    image: nginx:alpine\n    networks:\n      default:\n        aliases: [t-web]\n", True),
 ("service name equal to an app with environment network", "services:\n  t-web:\n    image: nginx:alpine\n", True),
]
for label, compose, conn in cases:
    sid, r = new_service("t-c%d" % (abs(hash(label)) % 1000), compose, connect=conn)
    if not sid: print(f"{label:50} -> refused at save: {flash(r)[-130:]}"); continue
    s = settle(sid, 150); log = shout(f"tail -5 /var/lib/musdash/logs/services/{sid}*.log 2>/dev/null | cut -c1-200")
    print(f"{label:50} -> {s} | {log[-230:]}")
    rec("S10.4w", "INFO", f"{label}: {s}; {log[-150:]}")
    drop(sid)
