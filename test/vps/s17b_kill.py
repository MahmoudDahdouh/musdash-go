from lib import *
c = owner_client(); st = state(); web = st["web"]; db = st["pg"]; R = Client(token=ensure_tokens(c)["read_token"])
host = f"t-web.{HOST}.sslip.io"
# S17.1 kill -9 the control plane in the middle of a deploy
d = deploy(c, web); time.sleep(2.5)
pid = shout("systemctl show musdash-server -p MainPID --value"); sh(f"kill -9 {pid}")
t0 = time.time(); back = wait_for(lambda: Client().get("/healthz").status == 200, 60, 1)
serving = Client(f"http://{host}").get("/").status
c = owner_client()
res = dep_wait(c, web, d, 240)
conts = shout(f"docker ps -a --filter name=musdash-{web} --format '{{{{.Names}}}} {{{{.Status}}}}'")
check("S17.1", back and serving == 200 and res in ("success", "failed") and len(conts.splitlines()) == 1, f"kill -9 mid-deploy: control plane back in {time.time()-t0:.0f}s, app kept serving ({serving}), deployment ended as {res} (not stuck), exactly one container left: {conts.splitlines()}", sev="S2")
# S17.1b the job queue is not stuck: another deploy works
d2 = deploy(c, web); r2 = dep_wait(c, web, d2, 240); check("S17.1b", r2 == "success", f"a new deploy works after the crash ({r2})", sev="S2")
# S17.2 kill -9 mid-backup
before = set(re.findall(r"/databases/%s/backups/([a-z2-7]{12})/(?:delete|restore|download)" % db, c.get(f"/databases/{db}/backups").text))
c.post(f"/databases/{db}/backups", dict(_csrf=csrf_of(c, f"/databases/{db}/backups"))); time.sleep(0.8)
pid = shout("systemctl show musdash-server -p MainPID --value"); sh(f"kill -9 {pid}")
wait_for(lambda: Client().get("/healthz").status == 200, 60, 1); c = owner_client(); time.sleep(40)
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/databases/{db}/backups").text))
running = "Running" in t[t.find("Backups"):t.find("Backups") + 3000] and "Running now" not in t
stuck = re.findall(r"(Running|Waiting|Queued)", t[t.find("Back up now"):])[:3]
rows = shout("ls -la /var/lib/musdash/backups/%s | tail -4" % db)
rec("S17.2", "PASS" if not stuck else "FAIL", f"kill -9 mid-backup: no backup row stays 'running' forever (rows still active: {stuck}); files: {rows.splitlines()[-2:]}", "" if not stuck else "S2")
# S17.7 RSS
rss = shout("ps -o rss=,args= -C musdash")
rec("S17.7", "INFO", "RSS after the whole run (apps/dbs/services present): " + rss.replace("\n", " | "))
procs = {("proxy" if "proxy" in l else "server"): int(l.split()[0]) / 1024 for l in rss.splitlines()}
check("S17.7b", procs.get("server", 999) < 48 and procs.get("proxy", 999) < 32, f"RSS now: server {procs.get('server', 0):.1f} MB, proxy {procs.get('proxy', 0):.1f} MB (idle targets 30/20 MB; soft limits 48/32 MiB)", sev="S3")
