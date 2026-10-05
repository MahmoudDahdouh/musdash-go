from lib import *
c = owner_client(); st = state(); sink = st["sink"]; web = st["web"]
def channels():
    pg = c.get("/settings/notifications").text; return list(dict.fromkeys(re.findall(r"/settings/notifications/([a-z2-7]{12})/test", pg)))
chs = channels(); print(chs)
# find hook1 channel by its edit form having container checked? simply set both: hook1 container only
def ev(cid, on):
    pg = c.get("/settings/notifications").text; f = [x for x in parse_forms(pg) if x["action"] == f"/settings/notifications/{cid}"][0]
    o = {f"ev-{cid}-{k}": (k in on) for k in ("deploy", "backup", "task", "container", "disk")}; o[f"on-{cid}"] = True; c.post_form(f, _o=o)
t0 = int(shout("date +%s")) + 1
for i, cid in enumerate(chs): ev(cid, ("container",) if i == 0 else ())
# make sure the app is up, then crash PID 1 from inside (restart policy brings it back)
d = deploy(c, web); dep_wait(c, web, d, 200); time.sleep(3)
cn = shout(f"docker ps -q --filter name=musdash-{web} | head -1")
hostpid = shout(f"docker inspect {cn} --format '{{{{.State.Pid}}}}'"); sh(f"kill -9 {hostpid}"); time.sleep(45)
lg = shout(f"docker logs --since {t0} $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -E 'originalUrl|\"title\"|stopped' | tail -5")
st1 = shout(f"docker ps -a --filter name=musdash-{web} --format '{{{{.Names}}}} {{{{.Status}}}}'")
check("S11.6c", "stopped unexpectedly" in lg, f"in-container crash (host-side kill -9 of the main process) -> restart policy brought it back ({st1.splitlines()[:2]}) and a 'stopped unexpectedly' notification arrived: {lg[-250:]!r}", sev="S2")
page = re.sub("<[^>]+>", " ", c.get(f"/apps/{web}").text)
rec("S17.5", "INFO", f"status after in-container crash + auto-restart: {re.findall(r'Running|Not running|Exited|Crash[a-z]*', page)[:2]}")
