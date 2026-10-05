from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
def make(engine, name, image=""):
    r = c.submit(f"/projects/{pid}/databases/new?env={env}&engine={engine}", action=f"/projects/{pid}/databases", name=name, image=image) if image else c.submit(f"/projects/{pid}/databases/new?env={env}&engine={engine}", action=f"/projects/{pid}/databases", name=name)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url or "") or re.search(r"/databases/([a-z2-7]{12})", r.text)
    return m.group(1) if m else None, r
def wait_running(dbid, t=240):
    return wait_for(lambda: "Running" in re.sub("<[^>]+>", " ", c.get(f"/databases/{dbid}").text), t, 4)
db, r = make("postgres", "t-pg")
check("S8.1a", db is not None, f"PostgreSQL created ({db}): {flash(r)[-80:]!r}" if not db else f"PostgreSQL created ({db})")
check("S8.1b", wait_running(db), "database reaches Running (health check passed)")
save_state(pg=db)
page = c.get(f"/databases/{db}").text; txt = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", page))
conn = re.search(r"postgres(?:ql)?://\S+", txt); conn = conn.group(0) if conn else None
check("S8.3", conn and "t-pg:5432" in conn, f"connection string on Overview: {re.sub(r':[^:@/]+@', ':***@', conn) if conn else None}", sev="S3")
pw = re.search(r"://[^:]+:([^@]+)@", conn).group(1); user = re.search(r"://([^:]+):", conn).group(1)
net = shout("docker network ls --format '{{.Name}}' | grep musdash-" + env)
# S8.1 connect from another container by name
out = sh(f"docker run --rm --network {net} -e PGPASSWORD='{pw}' postgres:17-alpine psql -h t-pg -U {user} -d postgres -Atc \"create table if not exists t(x int); insert into t values (41),(42); select count(*) from t;\"")[1]
check("S8.1c", out.strip().endswith("2"), f"another container in the environment connects by name and writes: {out.strip()[-60:]!r}")
# S8.7 password handling
cid = shout("docker ps -q --filter name=musdash-db-" + db)
insp = shout(f"docker inspect {cid} --format '{{{{json .Config.Cmd}}}} {{{{json .Args}}}} {{{{json .HostConfig.Binds}}}}'"); ps = shout(f"ps -eo args | grep -c '{pw}'")
envf = shout(f"stat -c '%a %n' /var/lib/musdash/apps/{db}/env 2>&1; ls -la /var/lib/musdash/apps/{db} 2>&1 | head -4")
check("S8.7", pw not in insp and ps.strip() == "0", f"password not in docker Cmd/Args nor process list; env file: {envf.splitlines()[0]}", sev="S1")
rc, out = sh(f"grep -a -c '{pw}' /var/lib/musdash/musdash.db /var/lib/musdash/musdash.db-wal")
check("S8.7b", all(x.endswith(":0") for x in out.split()), "database password sealed at rest (not plaintext in SQLite)", sev="S1")
# S8.4 restart keeps data (stop then start)
c.post(f"/databases/{db}/stop", dict(_csrf=csrf_of(c, f"/databases/{db}"))); time.sleep(6)
stopped = not shout("docker ps -q --filter name=musdash-db-" + db)
c.post(f"/databases/{db}/start", dict(_csrf=csrf_of(c, f"/databases/{db}")))
ok = wait_running(db)
out = sh(f"docker run --rm --network {net} -e PGPASSWORD='{pw}' postgres:17-alpine psql -h t-pg -U {user} -d postgres -Atc 'select count(*) from t'")[1]
check("S8.4", stopped and ok and out.strip().endswith("2"), f"stop -> container gone={stopped}; start -> running={bool(ok)}; data still there: {out.strip()[-10:]!r}", sev="S1")
# S8.9 via API
D = Client(token=ensure_tokens(c)["deploy_token"])
r = D.post(f"/api/v1/databases/{db}/stop"); time.sleep(6); s1 = D.get(f"/api/v1/databases/{db}").json().get("status")
r2 = D.post(f"/api/v1/databases/{db}/start"); wait_running(db); s2 = D.get(f"/api/v1/databases/{db}").json().get("status")
check("S8.9", r.status in (200, 202) and s1 in ("stopped", "exited") and s2 == "running", f"API stop -> {s1}, start -> {s2}")
# S8.5 public port
page = c.get(f"/databases/{db}/settings").text
c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image="postgres:17-alpine", public=True, public_port="", memory_mb="", cpus="")
wait_running(db, 120)
ports = shout(f"docker port $(docker ps -q --filter name=musdash-db-{db} | head -1)")
m = re.search(r":(\d+)\s*$", ports.splitlines()[0]) if ports else None
check("S8.5a", bool(m), f"public port opened: {ports!r}", sev="S2")
if m:
    import socket
    s = socket.socket(); s.settimeout(8)
    try: s.connect((HOST, int(m.group(1)))); reach = True
    except Exception as e: reach = repr(e)
    check("S8.5b", reach is True, f"public port {m.group(1)} reachable from the internet (the Mac): {reach}", sev="S2")
    rr = sh(f"docker run --rm --network host -e PGPASSWORD=wrongpass postgres:17-alpine psql -h {HOST} -p {m.group(1)} -U {user} -d postgres -Atc 'select 1' 2>&1 | tail -1")[1]
    check("S8.5c", "password authentication failed" in rr or "authentication" in rr.lower(), f"wrong password refused: {rr.strip()[-90:]!r}", sev="S1")
    rr = sh(f"docker run --rm --network host -e PGPASSWORD='{pw}' postgres:17-alpine psql -h {HOST} -p {m.group(1)} -U {user} -d postgres -Atc 'select count(*) from t' 2>&1 | tail -1")[1]
    check("S8.5d", rr.strip().endswith("2"), f"right password works through the public port: {rr.strip()[-30:]!r}")
# close it again
c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image="postgres:17-alpine", public=False, public_port="", memory_mb="", cpus="")
wait_running(db, 120)
check("S8.5e", not shout(f"docker port $(docker ps -q --filter name=musdash-db-{db} | head -1)"), "closing the public port removes the published port")
# S8.6 limits applied; S8.11 one container at a time
c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image="postgres:17-alpine", public=False, public_port="", memory_mb="200", cpus="0.5")
t0 = time.time(); maxn = 0
while time.time() - t0 < 60:
    maxn = max(maxn, len(shout(f"docker ps -q --filter name=musdash-db-{db}").split())); time.sleep(0.5)
    if "Running" in re.sub("<[^>]+>", " ", c.get(f"/databases/{db}").text) and time.time() - t0 > 12: break
lim = shout(f"docker inspect $(docker ps -q --filter name=musdash-db-{db} | head -1) --format '{{{{.HostConfig.Memory}}}} {{{{.HostConfig.NanoCpus}}}}'")
check("S8.6", lim == f"{200*1024*1024} 500000000", f"saving limits restarts the container with them: {lim}")
check("S8.11", maxn <= 1, f"at most one container for the database at any moment during replacement (max seen {maxn})", sev="S2")
# S8.12 image injection
for img in ["postgres:17-alpine --privileged", "-v /:/h postgres", "postgres;id"]:
    r = c.submit(f"/databases/{db}/settings", action=f"/databases/{db}/settings", image=img, public=False, public_port="", memory_mb="200", cpus="0.5")
    cur = re.search(r'name="image"[^>]*value="([^"]*)"', c.get(f"/databases/{db}/settings").text).group(1)
    check("S8.12", cur == "postgres:17-alpine", f"engine tag {img!r} refused (stored {cur!r}, {r.status})", sev="S1")
