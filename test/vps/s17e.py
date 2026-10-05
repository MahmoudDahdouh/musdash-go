exec(open("s17a.py").read().split("setvars(f\"/apps/{A}/environment\"")[0])
rec("S17.1", "FAIL", "kill -9 of musdash-server while a deploy is waiting on the health check: after systemd restarts it (2 s) the deployment is marked failed (consistent) and the old container keeps serving, BUT the half-started new container (musdash-<app>-<deployment>, on a loopback port) is left running and nothing removes it (`docker ps` shows two containers for the app 3+ min later). Recover fails the row but does not remove the container. Stale containers are only cleaned when? see S17.1b.", "S3", evidence="docker ps: musdash-tq22fowx7la7-qdexmb5azxt2 Up 3 minutes (deployment qdexmb5azxt2 'failed'), musdash-tq22fowx7la7-eds7ie2saajl Up 7 minutes (serving)")
f = [x for x in parse_forms(c.get(f"/apps/{A}/settings", follow=True).text) if x["action"] == f"/apps/{A}/settings"][0]
c.post_form(f, health_path="", health_timeout="60")
print(deploy()); time.sleep(4)
cs = shout(f"docker ps -a --format '{{{{.Names}}}} {{{{.Status}}}}' | grep musdash-{A}"); print(cs)
check("S17.1b", cs.count("\n") == 0, f"the next successful deployment removes the orphan: {cs!r}", sev="S4")
