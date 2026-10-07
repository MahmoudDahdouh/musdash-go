"""All 8 database engines: create, run, write data, stop/start, backup, restore, delete."""
from lib_d import *
import sys
c = owner_client()
Q = {
 "postgres": ('PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc ', "'create table if not exists mt(x int); insert into mt values (42); select count(*) from mt'", "'select count(*) from mt'", "'drop table mt'"),
 "mysql": ('MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -u root -N "$MYSQL_DATABASE" -e ', '"create table if not exists mt(x int); insert into mt values (42); select count(*) from mt"', '"select count(*) from mt"', '"drop table mt"'),
 "mariadb": ('MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb -u root -N "$MARIADB_DATABASE" -e ', '"create table if not exists mt(x int); insert into mt values (42); select count(*) from mt"', '"select count(*) from mt"', '"drop table mt"'),
 "mongodb": ('mongosh --quiet -u "$MONGO_INITDB_ROOT_USERNAME" -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --eval ', "'db.getSiblingDB(\"t\").mt.insertOne({x:42}); print(\"N=\"+db.getSiblingDB(\"t\").mt.countDocuments())'", "'print(\"N=\"+db.getSiblingDB(\"t\").mt.countDocuments())'", "'db.getSiblingDB(\"t\").mt.drop()'"),
 "redis": ("redis-cli ", "set mt 42", "get mt", "del mt"),
 "keydb": ("keydb-cli ", "set mt 42", "get mt", "del mt"),
 "dragonfly": ('redis-cli -a "$DFLY_requirepass" ', "set mt 42", "get mt", "del mt"),
 "clickhouse": ('clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" -q ', '"create table if not exists mt(x Int32) engine=MergeTree order by x; insert into mt values (42); select count() from mt"', '"select count() from mt"', '"drop table mt"'),
}
WANT = {"redis": "42", "keydb": "42", "dragonfly": "42"}
engines = sys.argv[1:] or list(Q)
for eng in engines:
    name = "t-" + eng
    r = c.submit(base() + f"/databases/new?engine={eng}", action=base() + "/databases", name=name, engine=eng)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url or "")
    if not m: rec(f"DB {eng}", "FAIL", f"create refused: {flash(r)[-200:]}", "S2"); continue
    D = m.group(1); cn = f"musdash-db-{D}"; t0 = time.time()
    up = wait_for(lambda: "Running" in flash(c.get(f"/databases/{D}", follow=True))[:700], 400, 4)
    img = shout(f"docker inspect {cn} --format '{{{{.Config.Image}}}} {{{{if .State.Health}}}}{{{{.State.Health.Status}}}}{{{{end}}}}'")
    pre, w, rd, drop = Q[eng]
    def ex(arg): return shout(f"docker exec {cn} sh -c '{pre}{arg}' 2>&1".replace("'" + pre, "'" + pre) if False else "docker exec " + cn + " sh -c " + __import__("shlex").quote(pre + arg) + " 2>&1")
    out = ex(w); good = (WANT.get(eng, "1") in out.splitlines()[-1:] or ("N=1" in out)) if out else False
    check(f"DB {eng} up+write", bool(up) and good, f"{img} running in {int(time.time()-t0)}s; wrote+read: {out[-80:]!r}", "S2", evidence=out)
    # stop/start (the Redis/KeyDB crash-loop fix F1 is checked here)
    c.post(f"/databases/{D}/stop", dict(_csrf=csrf_of(c, f"/databases/{D}"))); time.sleep(4)
    stopped = shout(f"docker ps -q --filter name={cn}") == ""
    c.post(f"/databases/{D}/start", dict(_csrf=csrf_of(c, f"/databases/{D}")))
    up2 = wait_for(lambda: "Running" in flash(c.get(f"/databases/{D}", follow=True))[:700], 300, 4)
    time.sleep(5); out2 = ex(rd)
    st2 = shout(f"docker inspect {cn} --format '{{{{.State.Status}}}} restarts={{{{.RestartCount}}}}'")
    check(f"DB {eng} stop/start", stopped and bool(up2) and (WANT.get(eng, "1") in out2), f"stopped={stopped}, back up={bool(up2)} ({st2}), data after restart: {out2[-60:]!r}", "S2", evidence=out2)
    # backup
    has_backup = eng in ("postgres", "mysql", "mariadb", "mongodb", "redis")
    if has_backup:
        c.post(f"/databases/{D}/backups", dict(_csrf=csrf_of(c, f"/databases/{D}/backups")))
        bk = wait_for(lambda: "Succeeded" in flash(c.get(f"/databases/{D}/backups/list")) or ("Failed" in flash(c.get(f"/databases/{D}/backups/list")) and "FAILED"), 180, 4)
        files = shout(f"ls -la /var/lib/musdash/backups/{D}* /var/lib/musdash/backups/*/{D}* 2>/dev/null | tail -3; find /var/lib/musdash/backups -name '*{D}*' -o -path '*{D}*' -type f | head -3 | xargs -r ls -la")
        check(f"DB {eng} backup", bk is True, f"backup finished: {bk}; files: {files[-160:]!r}", "S2")
        if bk is True and eng in ("postgres", "mysql", "mariadb", "mongodb"):
            ex(drop); gone = ex(rd)
            page = c.get(f"/databases/{D}/backups/list")
            bid = re.search(r"/backups/([a-z2-7]{12})/restore", page.text)
            if bid:
                c.post(f"/databases/{D}/backups/{bid.group(1)}/restore", dict(_csrf=csrf_of(c, f"/databases/{D}/backups"), confirm=name))
                back = wait_for(lambda: (lambda o: o if "1" in o.splitlines()[-1:] or "N=1" in o else None)(ex(rd)), 120, 5)
                check(f"DB {eng} restore", bool(back), f"after drop: {gone[-40:]!r}; after restore: {str(back)[-40:]!r}", "S2")
            else: rec(f"DB {eng} restore", "FAIL", "no restore form on the backups list", "S2")
    else:
        rec(f"DB {eng} backup", "NA", "engine has no dump command (by catalogue design)")
    # delete with data
    pg = c.get(f"/databases/{D}/settings"); f = [x for x in parse_forms(pg.text) if x["action"].endswith("/delete")][0]
    rd_ = c.post_form(f, delete_data=True, confirm=name)
    check(f"DB {eng} delete", shout(f"docker ps -aq --filter name={cn}") == "", f"container removed ({rd_.status})", "S2")
    sh("docker image prune -af --filter until=1h >/dev/null 2>&1; docker volume prune -f >/dev/null 2>&1")
