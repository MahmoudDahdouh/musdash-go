from lib import *
c = owner_client(); st = state(); sink = st["sink"]; web = st["web"]
def channels():
    pg = c.get("/notifications").text; return list(dict.fromkeys(re.findall(r"/notifications/([a-z2-7]{12})/test", pg)))
for cid in channels(): c.post(f"/notifications/{cid}/delete", dict(_csrf=csrf_of(c, "/notifications")))
def add(name, path):
    r, forms = c.forms("/notifications?kind=webhook"); f = c.find_form(forms, "/notifications", has="name"); c.post_form(f, name=name, cfg_url=f"http://t-sink.{HOST}.sslip.io{path}")
    return [x for x in channels()][0] if len(channels()) == 1 else None
add("t-hook1", "/hook1"); cid1 = channels()[0]
add("t-hook2", "/hook2"); cid2 = [x for x in channels() if x != cid1][0]
def events(cid, on):
    pg = c.get("/notifications").text; f = [x for x in parse_forms(pg) if x["action"] == f"/notifications/{cid}"][0]
    o = {f"ev-{cid}-{k}": (k in on) for k in ("deploy", "backup", "task", "container", "disk")}; o[f"on-{cid}"] = True; return c.post_form(f, _o=o)
T0 = [0]
def fresh(): T0[0] = int(shout("date +%s")) + 1; time.sleep(2)
def paths(): return sorted(set(re.findall(r"/hook[12]", shout(f"docker logs --since {T0[0]} $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -E 'originalUrl|\"path\"'"))))
events(cid1, ("deploy",)); events(cid2, ("task",)); fresh()
d = deploy(c, web); dep_wait(c, web, d); time.sleep(10)
p = paths(); check("S11.6a", p == ["/hook1"], f"deploy event reached only the channel that chose it (sink saw {p})", sev="S2")
c.submit(f"/apps/{web}/tasks", action=f"/apps/{web}/tasks", name="t-failing", command="exit 3", schedule="@weekly", enabled=True)
tid = re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, c.get(f"/apps/{web}/tasks").text)[0]
fresh(); c.post(f"/apps/{web}/tasks/{tid}/run", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks"))); time.sleep(40)
p = paths(); check("S11.6b", p == ["/hook2"], f"failed-task event reached only its channel (sink saw {p})", sev="S2")
c.post(f"/apps/{web}/tasks/{tid}/delete", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks")))
events(cid1, ("container",)); events(cid2, ()); fresh()
cn = shout(f"docker ps -q --filter name=musdash-{web} | head -1"); sh(f"docker kill {cn}"); time.sleep(25)
p1 = paths()
print(shout("journalctl -u musdash-server --since '-1 min' --no-pager | tail -6"))
check("S11.6c", p1 == ["/hook1"], f"`docker kill` of the app container -> 'container stopped' notification on the channel that chose it (sink saw {p1})", sev="S2")
fresh(); cn = shout(f"docker ps -q --filter name=musdash-{web} | head -1"); sh(f"docker kill {cn}"); time.sleep(25)
p2 = paths(); check("S11.7", p2 == [], f"a second crash within 15 minutes does not notify again (sink saw {p2})", sev="S3")
