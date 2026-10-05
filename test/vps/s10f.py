exec(open("s10d.py").read().split("# external volume, long syntax")[0])
def last(S): return log(S)[-500:].replace("\n", " | ")
# S10.5 sandbox: include / extends / env_file / ${} of host paths
cases = {
 "include": "include:\n  - /etc/passwd\nservices:\n  a:\n    image: nginx:alpine\n",
 "env_file": "services:\n  a:\n    image: nginx:alpine\n    env_file: /etc/passwd\n",
 "env_file_data": "services:\n  a:\n    image: nginx:alpine\n    env_file: /var/lib/musdash/master.key\n",
 "extends": "services:\n  a:\n    extends:\n      file: /etc/hostname\n      service: x\n",
 "dotdot": "services:\n  a:\n    image: nginx:alpine\n    env_file: ../../../../etc/shadow\n",
 "envhost": "services:\n  a:\n    image: nginx:alpine\n    environment:\n      X: ${HOME}${USER}${PATH}\n",
}
for k, comp in cases.items():
    S = create(f"t2-sb-{k}".replace("_", "-"), comp); l = last(S)
    leaked = bool(re.search(r"root:x:|daemon:|master\.key|[A-Za-z0-9+/]{43}=|/usr/local/sbin", l))
    started = shout(f"docker ps -q --filter name=musdash-{S} | wc -l")
    rec(f"S10.5-{k}", "FAIL" if leaked or started != "0" else "PASS", f"{l[-260:]} | containers={started}", "S1" if leaked or started != "0" else "")
# S10.7b missing variable
S = create("t2-missing-var", "services:\n  a:\n    image: nginx:alpine\n    environment:\n      X: ${NOT_SET_ANYWHERE:?must be set}\n      Y: ${ALSO_MISSING}\n")
l = last(S); rec("S10.7b", "PASS" if "NOT_SET_ANYWHERE" in l or "ALSO_MISSING" in l else "FAIL", l[-300:], "" if "NOT_SET" in l or "ALSO" in l else "S3")
# S10.9 ports
for p, ok in [("30001:80", True), ("23000:80", False), ("80:80", False), ("1000:80", False), ("65535:80", True), ("2000:80", True), ("127.0.0.1:30002:80", None)]:
    S = create(f"t2-port-{p.replace(':','-').replace('.','')}"[:30], f"services:\n  a:\n    image: nginx:alpine\n    ports:\n      - \"{p}\"\n"); l = last(S)
    up = shout(f"docker ps -q --filter name=musdash-{S} | wc -l") != "0"
    refused = "Failed" in l and ("port" in l.lower())
    res = "PASS" if (ok is None) or (ok and up) or ((not ok) and refused and not up) else "FAIL"
    rec(f"S10.9-{p}", res, f"expect {'accepted' if ok else 'refused' if ok is False else 'info'}: up={up} {l[-200:]}", "" if res == "PASS" else "S2")
    if up: shout(f"docker rm -f $(docker ps -aq --filter name=musdash-{S}) >/dev/null 2>&1")
