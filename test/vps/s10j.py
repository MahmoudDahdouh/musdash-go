exec(open("s10d.py").read().split("# external volume, long syntax")[0])
S = state()["b_svc_stack"]
def cont(): return shout(f"docker ps -a --filter name=musdash-{S} --format '{{{{.Names}}}} {{{{.Status}}}}'")
def post(a, **kw):
    f = [x for x in parse_forms(c.get(f"/services/{S}", follow=True).text) + parse_forms(c.get(f"/services/{S}/settings", follow=True).text) if x["action"] == f"/services/{S}/{a}"][0]
    return c.post_form(f, follow=False, **kw)
post("stop"); wait_for(lambda: "Up" not in cont(), 90, 3)
vol = shout("docker volume ls -q | grep -c " + S)
check("S10.10a", "Up" not in cont() and int(vol) >= 1, f"stop: containers {cont()!r}, volumes kept {vol}")
post("deploy"); wait_for(lambda: cont().count("Up") == 2, 120, 3)
check("S10.10b", cont().count("Up") == 2, f"deploy after stop brings both containers back: {cont()}")
r = post("delete", confirm="t2-stack", delete_data=False); time.sleep(8)
vol = shout("docker volume ls -q | grep -c " + S)
check("S10.10c", cont() == "" and int(vol) >= 1, f"delete without data: containers gone, volumes kept ({vol})", evidence=cont())
