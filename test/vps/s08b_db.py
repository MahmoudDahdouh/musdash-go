from lib import *
c = owner_client(); st = state(); db = st["pg"]; env = st["env"]
def wait_running(t=240): return wait_for(lambda: "Running" in re.sub("<[^>]+>", " ", c.get(f"/databases/{db}").text), t, 4)
copy = re.findall(r'data-copy="([^"]+)"', c.get(f"/databases/{db}").text); pw = copy[1]; user = "postgres"
net = shout("docker network ls --format '{{.Name}}' | grep musdash-" + env)
def psql(q, extra=""): return sh(f"docker run --rm --network {net} -e PGPASSWORD='{pw}' postgres:17-alpine psql -h t-pg -U {user} -d postgres -Atc \"{q}\"")[1].strip()
wait_running()
out = psql("create table if not exists t(x int); insert into t values (41),(42); select count(*) from t;")
check("S8.1c", out.endswith("2"), f"another container in the environment connects by name and writes: {out[-40:]!r}")
cid = shout("docker ps -q --filter name=musdash-db-" + db)
insp = shout(f"docker inspect {cid} --format '{{{{json .Config.Cmd}}}} {{{{json .Args}}}} {{{{json .HostConfig.Binds}}}}'")
sh("rm -f /tmp/pslog2; nohup sh -c 'for i in $(seq 1 100); do ps -eo args >> /tmp/pslog2; sleep 0.2; done' >/dev/null 2>&1 &")
c.post(f"/databases/{db}/stop", dict(_csrf=csrf_of(c, f"/databases/{db}"))); time.sleep(5)
c.post(f"/databases/{db}/start", dict(_csrf=csrf_of(c, f"/databases/{db}"))); ok = wait_running(); time.sleep(10)
ps = shout(f"grep -c '{pw}' /tmp/pslog2")
check("S8.7", pw not in insp and ps.strip() == "0", f"password not in docker Cmd/Args and never in the process list while the database was started (hits {ps.strip()})", sev="S1")
out = psql("select count(*) from t")
check("S8.4", ok and out.endswith("2"), f"stop/start keeps the data: {out!r}", sev="S1")
D = Client(token=ensure_tokens(c)["deploy_token"])
D.post(f"/api/v1/databases/{db}/stop"); time.sleep(6); s1 = D.get(f"/api/v1/databases/{db}").json().get("status")
D.post(f"/api/v1/databases/{db}/start"); st2 = wait_for(lambda: D.get(f"/api/v1/databases/{db}").json().get("status") == "running", 120, 3)
check("S8.9", s1 in ("stopped", "exited") and st2, f"API stop -> {s1}; start -> running={bool(st2)} (status is 'deploying' for a few seconds first)")
# public port with the right password
c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image="postgres:17-alpine", public=True, public_port="", memory_mb="200", cpus="0.5"); wait_running(120)
port = re.search(r":(\d+)\s*$", shout(f"docker port $(docker ps -q --filter name=musdash-db-{db} | head -1)").splitlines()[0]).group(1)
rr = sh(f"docker run --rm --network host -e PGPASSWORD='{pw}' postgres:17-alpine psql -h {HOST} -p {port} -U {user} -d postgres -Atc 'select count(*) from t' 2>&1 | tail -1")[1].strip()
check("S8.5d", rr.endswith("2"), f"right password works through the public port {port}: {rr[-30:]!r}")
c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image="postgres:17-alpine", public=False, public_port="", memory_mb="200", cpus="0.5"); wait_running(120)
# logs / metrics / status pages
for sfx in ["/logs", "/metrics", "/status"]:
    r = c.get(f"/databases/{db}{sfx}"); rec("S8.8" + sfx[1], "PASS" if r.status == 200 else "FAIL", f"{sfx} -> {r.status}", "" if r.status == 200 else "S3")
import socket
ck = "; ".join(f"{k}={v}" for k, v in c.cookies.items()); s = socket.create_connection((HOST, 8000), timeout=6)
s.sendall(f"GET /databases/{db}/logs/stream HTTP/1.1\r\nHost: {HOST}:8000\r\nCookie: {ck}\r\n\r\n".encode()); time.sleep(3); d = s.recv(4000).decode(errors="replace"); s.close()
ls = re.findall(r"data: (.{0,70})", d)[:2]
check("S8.8d", "text/event-stream" in d and "ready" in d, f"database log stream shows the engine log: {ls}", sev="S3")
