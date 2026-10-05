from lib_b import *
c = owner_client(); st = state()
def status(D):
    t = flash(c.get(f"/databases/{D}", follow=True)); m = re.search(r"(Starting|Running|Stopped|Failed|Unhealthy|Error)\w* (PostgreSQL|MySQL|MariaDB|MongoDB|Redis|KeyDB|Dragonfly|ClickHouse)", t)
    return m.group(1) if m else "?"
env = lambda D: shout(f"cat /var/lib/musdash/apps/{D}/env")
def net(D): return shout(f"docker inspect musdash-db-{D} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}} {{{{end}}}}'").split()[0]
def val(D, k): return f"$(grep ^{k}= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
tests = {
 "redis":    lambda D, n: f"docker run --rm --network {n} -e REDISCLI_AUTH={val(D,'REDIS_PASSWORD')} redis:7-alpine redis-cli -h t2-redis set k v",
 "keydb":    lambda D, n: f"docker run --rm --network {n} -e REDISCLI_AUTH={val(D,'REDIS_PASSWORD')} redis:7-alpine redis-cli -h t2-keydb set k v",
 "dragonfly":lambda D, n: f"docker run --rm --network {n} -e REDISCLI_AUTH={val(D,'REDIS_PASSWORD')} redis:7-alpine redis-cli -h t2-dragonfly set k v",
 "mysql":    lambda D, n: f"docker run --rm --network {n} -e MYSQL_PWD={val(D,'MYSQL_PASSWORD')} mysql:8.4 mysql -h t2-mysql -uapp app -N -e 'select 1'",
 "mariadb":  lambda D, n: f"docker run --rm --network {n} -e MYSQL_PWD={val(D,'MYSQL_PASSWORD')} mariadb:11 mariadb -h t2-mariadb -uapp app -N -e 'select 1'",
}
for eng in ["redis", "mysql", "mariadb", "mongodb", "keydb", "dragonfly", "clickhouse"]:
    D = st.get(f"b_db_{eng}")
    ok = wait_for(lambda: status(D) == "Running" and 1, 300, 5)
    keys = " ".join(l.split("=")[0] for l in env(D).splitlines()) if ok else ""
    img = shout(f"docker inspect musdash-db-{D} --format '{{{{.Config.Image}}}}'")
    out = ""
    if eng in tests:
        out = sh(tests[eng](D, net(D)))[1].strip()
    print(eng, D, "ui:", status(D), "|", img, "|", keys, "|", out[:100])
