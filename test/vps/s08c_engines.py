from lib import *
import sys
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
ENGINES = sys.argv[1:] or ["redis", "mariadb", "mysql", "mongodb"]
def make(engine, name):
    r = c.submit(f"/projects/{pid}/databases/new?env={env}&engine={engine}", action=f"/projects/{pid}/databases", name=name)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url or "") or re.search(r"/databases/([a-z2-7]{12})", r.text); return m.group(1) if m else None
API = Client(token=ensure_tokens(c)["read_token"])
def running(db, t=900):
    def f():
        j = API.get(f"/api/v1/databases/{db}").json(); s = j.get("status")
        if s in ("failed", "error"): return "failed"
        return s == "running" or None
    r = wait_for(f, t, 5); return r is True
def rows(db): return re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/databases/{db}/backups").text))
def backups(db): return list(dict.fromkeys(re.findall(r"/databases/%s/backups/([a-z2-7]{12})/(?:delete|restore|download)" % db, c.get(f"/databases/{db}/backups").text)))
def conn(db):
    cp = re.findall(r'data-copy="([^"]+)"', c.get(f"/databases/{db}").text); u = cp[0]
    m = re.match(r"(\w+)://([^:]*):([^@]*)@([^:/]+):(\d+)/?(.*)", u); return dict(url=u, scheme=m.group(1), user=m.group(2), pw=m.group(3), host=m.group(4), port=m.group(5), dbn=m.group(6))
def X(db, cmd): return sh(f"docker exec $(docker ps -q --filter name=musdash-db-{db} | head -1) sh -c {json.dumps(cmd)} 2>&1")[1].strip()
SPEC = {
 "redis":   dict(w=lambda q: f"redis-cli -a '{q['pw']}' --no-auth-warning set mark marker-1; redis-cli -a '{q['pw']}' --no-auth-warning get mark", ok="marker-1", restore=False),
 "mariadb": dict(w=lambda q: f"mariadb -u{q['user']} -p'{q['pw']}' {q['dbn']} -e \"create table if not exists t(x int); insert into t values (41),(42); select count(*) from t\"", ok="\n2", drop=lambda q: f"mariadb -u{q['user']} -p'{q['pw']}' {q['dbn']} -e 'drop table t'", chk=lambda q: f"mariadb -u{q['user']} -p'{q['pw']}' {q['dbn']} -N -e 'select count(*) from t'"),
 "mysql":   dict(w=lambda q: f"mysql -u{q['user']} -p'{q['pw']}' {q['dbn']} -e \"create table if not exists t(x int); insert into t values (41),(42); select count(*) from t\"", ok="\n2", drop=lambda q: f"mysql -u{q['user']} -p'{q['pw']}' {q['dbn']} -e 'drop table t'", chk=lambda q: f"mysql -u{q['user']} -p'{q['pw']}' {q['dbn']} -N -e 'select count(*) from t'"),
 "mongodb": dict(w=lambda q: f"mongosh --quiet '{q['url']}' --eval 'db.getSiblingDB(\"d\").t.insertMany([{{x:41}},{{x:42}}]).insertedCount'".replace("{{", "{").replace("}}", "}") .replace(f"@{q['host']}:", "@localhost:"), ok="2",
                 drop=lambda q: f"mongosh --quiet '{q['url']}' --eval 'db.getSiblingDB(\"d\").dropDatabase().ok'".replace(f"@{q['host']}:", "@localhost:"), chk=lambda q: f"mongosh --quiet '{q['url']}' --eval 'db.getSiblingDB(\"d\").t.countDocuments()'".replace(f"@{q['host']}:", "@localhost:")),
}
for eng in ENGINES:
    name = "t-" + eng; t0 = time.time()
    db = make(eng, name)
    if not db: rec("S8.2" + eng, "FAIL", f"{eng}: could not create", "S2"); continue
    ok = running(db)
    time.sleep(3)
    check("S8.2" + eng[:3], ok, f"{eng} starts healthy ({time.time()-t0:.0f}s incl. image pull)", sev="S2")
    if not ok:
        print(c.get(f"/databases/{db}/logs").status); df = [x for x in parse_forms(c.get(f"/databases/{db}/settings").text) if x["action"] == f"/databases/{db}/delete"][0]; c.post_form(df, confirm=name, delete_data=True); continue
    q = conn(db); sp = SPEC[eng]
    out = X(db, sp["w"](q)); check("S8.1" + eng[:3], sp["ok"] in out, f"{eng}: connect with the generated credentials and write: {out[-80:]!r}", sev="S2")
    # backup
    before = set(backups(db)); c.post(f"/databases/{db}/backups", dict(_csrf=csrf_of(c, f"/databases/{db}/backups")))
    bid = wait_for(lambda: [b for b in backups(db) if b not in before] or None, 90, 3)
    wait_for(lambda: "Running" not in rows(db) and "Waiting" not in rows(db), 240, 4)
    fl = shout(f"ls -la /var/lib/musdash/backups/{db}/; for f in /var/lib/musdash/backups/{db}/*; do gzip -t $f && echo GZIP_OK $(stat -c %s $f); done")
    check("S9.1" + eng[:3], bid and "GZIP_OK" in fl and int(re.findall(r"GZIP_OK (\d+)", fl)[0]) > 20, f"{eng}: backup written and is valid gzip: {fl.splitlines()[-1] if fl else ''}", sev="S2")
    if bid and sp.get("restore", True):
        X(db, sp["drop"](q)); gone = X(db, sp["chk"](q))
        rf = [f for f in parse_forms(c.get(f"/databases/{db}/backups").text) if f["action"] == f"/databases/{db}/backups/{bid[0]}/restore"]
        if rf:
            c.post_form(rf[0], confirm=name)
            back = wait_for(lambda: X(db, sp["chk"](q)).strip().endswith("2") or None, 150, 5)
            check("S9.4" + eng[:3], back, f"{eng}: dropped the data, restored from the backup, rows back (before restore: {gone[-40:]!r})", sev="S1")
        else:
            rec("S9.4" + eng[:3], "FAIL", f"{eng}: no restore form on a succeeded backup", "S2")
    elif bid and eng == "redis":
        has_restore = any(f["action"].endswith("/restore") for f in parse_forms(c.get(f"/databases/{db}/backups").text))
        check("S9.5b", not has_restore, "Redis: no restore button (RDB cannot be restored from the page), download only", sev="S3")
        r = c.get(f"/databases/{db}/backups/{bid[0]}/download"); rec("S9.6redis", "PASS" if r.status == 200 and len(r.body) > 10 else "FAIL", f"Redis backup downloads ({r.status}, {len(r.body)} bytes)", "" if r.status == 200 else "S3")
    # delete with data
    df = [x for x in parse_forms(c.get(f"/databases/{db}/settings").text) if x["action"] == f"/databases/{db}/delete"][0]
    c.post_form(df, confirm=name, delete_data=True); time.sleep(10)
    gone = not shout(f"docker ps -aq --filter name=musdash-db-{db}") and not shout(f"docker volume ls -q | grep -c musdash-db-{db}-data || true").strip().strip("0")
    vol = shout(f"docker volume ls -q | grep musdash-db-{db} || true"); bk = shout(f"ls /var/lib/musdash/backups/{db} 2>&1 | head -1")
    check("S8.10" + eng[:3], not vol and "No such" in bk, f"{eng}: delete with data removes container, volume ({vol!r}) and its backups ({bk!r})", sev="S2")
    sh("docker image prune -af >/dev/null 2>&1; docker builder prune -f >/dev/null 2>&1")
