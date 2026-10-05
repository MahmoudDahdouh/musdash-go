exec(open("s10d.py").read().split("def create(")[0])
P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
def git_svc(name, path):
    forms = parse_forms(c.get(f"/projects/{P}/services/new?env={E}&template=git", follow=True).text)
    f = [x for x in forms if x["action"] == f"/projects/{P}/services"][0]
    rr = c.post_form(f, follow=True, name=name, access="public", repo="https://github.com/docker/awesome-compose", branch="master", compose_path=path, deploy=True)
    m = re.search(r"/services/([a-z2-7]{12})", rr.url)
    if not m: return None, flash(rr)[:300]
    S = m.group(1)
    l = wait_for(lambda: (lambda t: t if re.search(r"Failed:|Deployed\.", t) else None)(shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)")), 400, 6) or "(timeout)"
    return S, l
S, l = git_svc("t2-git-ng", "nginx-golang/compose.yaml"); print(S, l[-700:])
print(shout(f"ls /var/lib/musdash/apps/{S}/ 2>&1; docker ps -a --filter name=musdash-{S} --format '{{{{.Names}}}} {{{{.Status}}}}'"))
save_state(b_svc_git=S)
