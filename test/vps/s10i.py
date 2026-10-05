exec(open("s10d.py").read().split("# external volume, long syntax")[0])
def create2(name, compose, **kw):
    forms = parse_forms(c.get(f"/projects/{P}/services/new?env={E}&template=custom", follow=True).text)
    f = [x for x in forms if x["action"] == f"/projects/{P}/services"][0]
    rr = c.post_form(f, follow=True, name=name, compose=compose, **kw)
    m = re.search(r"/services/([a-z2-7]{12})", rr.url); return (m.group(1) if m else None), rr
# S10.8 collision with an existing database name on the environment network
S, rr = create2("t2-collide", "services:\n  t2-pg:\n    image: nginx:alpine\n", connect_env=True, deploy=True)
l = log(S) if S else flash(rr)
rec("S10.8a", "PASS" if re.search(r"Failed|already|name", l) and "Deployed." not in l else "FAIL", f"service named like an existing database on the connected env network: {l[-300:]}".replace("\n", " | "), "" if "Deployed." not in l else "S2")
# S10.8b a service alias of the stack that collides with an app name (t-web is the other session's: use own name t2-pg only)
S, rr = create2("t2-connect", "services:\n  uniqweb:\n    image: nginx:alpine\n", connect_env=True, deploy=True)
l = log(S)
ok = "Deployed." in l
nets = shout(f"docker inspect musdash-{S}-uniqweb-1 --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}}:{{{{$v.Aliases}}}} {{{{end}}}}'")
check("S10.8b", ok and "musdash-jyrxc6zjhvih" in nets, f"service joined the environment network and is reachable there by service name: {nets}")
r = sh(f"docker run --rm --network musdash-jyrxc6zjhvih curlimages/curl -s -o /dev/null -w '%{{http_code}}' http://uniqweb")[1].strip()
check("S10.8c", r == "200", f"another container on the env network reaches the service by name: http {r}")
save_state(b_svc_connect=S)
