exec(open("s10d.py").read().split("# external volume, long syntax")[0])
S = create("t2-ext-vol-short", "services:\n  a:\n    image: nginx:alpine\n    volumes:\n      - v:/x\nvolumes:\n  v:\n    external: true\n")
l = log(S); print(l[-700:])
print(shout(f"cat /var/lib/musdash/apps/{S}/compose.resolved.json | head -40; docker volume ls -q | grep -c '^v$'"))
