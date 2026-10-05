from lib import *
c = owner_client(); web = state()["web"]
def tasks_page(): return c.get(f"/apps/{web}/tasks").text
def create(name, cmd, sched, enabled=True):
    r = c.submit(f"/apps/{web}/tasks", action=f"/apps/{web}/tasks", name=name, command=cmd, schedule=sched, enabled=enabled)
    return r
# validation
for label, sched in {"bad cron": "61 * * * *", "words": "every day", "6 fields": "* * * * * *"}.items():
    r = create("t-bad", "true", sched)
    check("S11.0" + label[0], r.status == 422 or "t-bad" not in tasks_page(), f"schedule {sched!r} refused ({r.status})", sev="S3")
for f_ in parse_forms(tasks_page()):
    if f_["action"].endswith("/delete") and "/tasks/" in f_["action"]: c.post_form(f_)
r = create("t-hello", "echo hello-from-task; echo to-stderr 1>&2; id -u", "@daily")
tid = re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, tasks_page())[0]
check("S11.1", "t-hello" in tasks_page(), f"task created ({tid}); next run shown: {re.findall(r'[Nn]ext[^<]{0,50}', tasks_page())[:1]}")
# run now
before = shout(f"docker ps -q --filter name=musdash-{web}")
rr = c.post(f"/apps/{web}/tasks/{tid}/run", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks")))
def runs():
    r = c.get(f"/apps/{web}/tasks/{tid}").text; t = re.sub(r"<[^>]+>", " ", r)
    return t if "Succeeded" in t or "Failed" in t else None
t = wait_for(runs, 120, 3) or ""
check("S11.2", "hello-from-task" in t, "run now executed in the serving container; output recorded: " + re.sub(r"\s+", " ", t)[200:420])
check("S11.2b", "to-stderr" in t, "stderr captured too", sev="S3")
rec("S11.2c", "INFO", "exit status shown: " + str(re.findall(r"[Ee]xit[^<]{0,30}|status[^<]{0,20}", t)[:2]))
# failing command -> exit status recorded
create("t-fail", "exit 7", "@weekly"); fid = [x for x in re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, tasks_page()) if x != tid][0]
c.post(f"/apps/{web}/tasks/{fid}/run", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks"))); wait_for(lambda: "Failed" in c.get(f"/apps/{web}/tasks/{fid}").text or "Succeeded" in c.get(f"/apps/{web}/tasks/{fid}").text, 120, 3)
t2 = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", c.get(f"/apps/{web}/tasks/{fid}").text))
check("S11.2d", "7" in t2, "failing command's exit status 7 recorded: " + t2[160:320])
# S11.3 due task fires on its own
create("t-every-minute", "echo tick-$(date +%s) >> /tmp/ticks; echo tick", "* * * * *"); mid = [x for x in re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, tasks_page()) if x not in (tid, fid)][0]
t0 = time.time(); got = wait_for(lambda: "tick" in re.sub(r"<[^>]+>", " ", c.get(f"/apps/{web}/tasks/{mid}").text), 130, 5)
check("S11.3", got, f"a * * * * * task fired on its own (after {time.time()-t0:.0f}s)", sev="S2")
# no overlap: a slow task with an every-minute schedule
create("t-slow", "sleep 100", "* * * * *"); sid = [x for x in re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, tasks_page()) if x not in (tid, fid, mid)][0]
time.sleep(140)
n = len(re.findall(r"(?i)started|ago", re.sub(r"<[^>]+>", " ", c.get(f"/apps/{web}/tasks/{sid}").text)))
procs = shout(f"docker exec $(docker ps -q --filter name=musdash-{web}) sh -c 'ps | grep -c \"sleep 100\"' ")
check("S11.4a", int(procs.split()[-1]) <= 2, f"a task still running when its next time comes is not started twice (sleep processes: {procs})", sev="S3")
# edit and delete
c.post(f"/apps/{web}/tasks/{tid}", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks"), name="t-hello2", command="echo x", schedule="@hourly", enabled="on"))
check("S11.5a", "t-hello2" in tasks_page(), "task edited")
for t_ in (tid, fid, mid, sid):
    c.post(f"/apps/{web}/tasks/{t_}/delete", dict(_csrf=csrf_of(c, f"/apps/{web}/tasks")))
check("S11.5b", not re.findall(r"/apps/%s/tasks/([a-z2-7]{12})" % web, tasks_page()), "tasks deleted")
