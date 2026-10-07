from lib import *
c = owner_client(); st = state(); sink = st["sink"]; web = st["web"]
def channels():
    pg = c.get("/notifications").text
    return list(dict.fromkeys(re.findall(r"/notifications/([a-z2-7]{12})/test", pg)))
def sinklog(since="10m"): return shout(f"docker logs --since {since} $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -E '\"path\"|\"body\"|originalUrl' | tail -30")
def add(kind, name, **cfg):
    r, forms = c.forms(f"/notifications?kind={kind}"); f = c.find_form(forms, "/notifications", has="name")
    return c.post_form(f, name=name, **{("cfg_" + k): v for k, v in cfg.items()})
def test(cid):
    return c.post(f"/notifications/{cid}/test", dict(_csrf=csrf_of(c, "/notifications")), follow=True)
def events(cid, on=("deploy", "backup", "task", "container", "disk"), enabled=True):
    pg = c.get("/notifications").text; f = [x for x in parse_forms(pg) if x["action"] == f"/notifications/{cid}"][0]
    o = {f"ev-{cid}-{k}": (k in on) for k in ("deploy", "backup", "task", "container", "disk")}; o[f"on-{cid}"] = enabled
    return c.post_form(f, _o=o)
# existing channel from exploration
ch = channels(); cid1 = ch[0] if ch else None
if not cid1:
    add("webhook", "t-hook", url=f"http://t-sink.{HOST}.sslip.io/hook1"); cid1 = channels()[0]
add("webhook", "t-hook2", url=f"http://t-sink.{HOST}.sslip.io/hook2", secret="topsecret"); cid2 = [x for x in channels() if x != cid1][0]
# S11.5 test delivery
r = test(cid1); time.sleep(3)
lg = sinklog()
check("S11.5a", "hook" in lg or "originalUrl" in lg, f"Test button delivers to the webhook sink: {flash(r)[-120:]!r}; sink saw: {lg[-200:]!r}", sev="S2")
r = test(cid2); time.sleep(3); lg2 = sinklog()
hdrs = shout("docker logs --since 2m $(docker ps -q --filter name=musdash-%s | head -1) 2>&1 | grep -i -E \"signature|authorization|x-musdash\" | head -3" % sink)
check("S11.5b", "hook2" in lg2, f"second webhook (with secret) delivered; signature headers seen: {hdrs[:200]!r}")
# secrets not shown again
pg = c.get("/notifications").text
check("S11.5c", "topsecret" not in pg and "t-sink" not in re.sub(r"<[^>]+>", " ", pg).split("Webhook URL")[0], "channel address and secret are not shown again", sev="S1")

# S11.6 event routing: hook1 hears only 'deploy', hook2 only 'task'
events(cid1, on=("deploy",)); events(cid2, on=("task",))
sh(f"docker ps -q --filter name=musdash-{sink} | xargs -r docker restart >/dev/null"); time.sleep(8)   # fresh sink log
d = deploy(c, web); dep_wait(c, web, d); time.sleep(8)
lg = shout(f"docker logs $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -E 'originalUrl|\"path\"' ")
rec("S11.6a", "PASS" if "/hook1" in lg and "/hook2" not in lg else "FAIL", f"deploy event reached only the channel that chose it (sink paths: {sorted(set(re.findall(r'/hook[12]', lg)))})", "" if "/hook1" in lg and "/hook2" not in lg else "S2")
# failed task event -> hook2 only
c.submit(f"/apps/{web}/tasks", action=f"/apps/{web}/tasks", name="t-failing", command="exit 3", schedule="@weekly", enabled=True)
tid = re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, c.get(f"/apps/{web}/tasks").text)[0]
sh(f"docker ps -q --filter name=musdash-{sink} | xargs -r docker restart >/dev/null"); time.sleep(8)
c.post(f"/apps/{web}/tasks/{tid}/run", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks"))); time.sleep(40)
lg = shout(f"docker logs $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -E 'originalUrl|\"path\"' ")
rec("S11.6b", "PASS" if "/hook2" in lg and "/hook1" not in lg else "FAIL", f"failed-task event reached only its channel (paths: {sorted(set(re.findall(r'/hook[12]', lg)))})", "" if "/hook2" in lg and "/hook1" not in lg else "S2")
c.post(f"/apps/{web}/tasks/{tid}/delete", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks")))
# container died unexpectedly -> 'container' event; throttle: kill twice
events(cid1, on=("container",)); events(cid2, on=())
sh(f"docker ps -q --filter name=musdash-{sink} | xargs -r docker restart >/dev/null"); time.sleep(8)
cn = shout(f"docker ps -q --filter name=musdash-{web} | head -1"); sh(f"docker kill {cn}"); time.sleep(20)
sh(f"docker kill $(docker ps -q --filter name=musdash-{web} | head -1) 2>/dev/null"); time.sleep(20)
lg = shout(f"docker logs $(docker ps -q --filter name=musdash-{sink} | head -1) 2>&1 | grep -c '/hook1' ")
rec("S11.6c", "PASS" if lg.strip() not in ("0", "") else "FAIL", f"`docker kill` of an app container produced a 'container stopped' notification ({lg.strip()} hits on hook1) and a second kill within 15 min did not repeat it only if count is small", "" if lg.strip() not in ("0", "") else "S2")
page = re.sub(r"<[^>]+>", " ", c.get(f"/apps/{web}").text)
rec("S17.4", "INFO", f"after docker kill the restart policy brought it back: {shout('docker ps --format {{.Names}}:{{.Status}} | grep ' + web)}; page status: {re.findall(r'Running|Stopped|Failed|Crash[a-z]*', page)[:2]}")
