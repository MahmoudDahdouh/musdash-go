from lib_b import *
st = state()
def net(D): return shout(f"docker inspect musdash-db-{D} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}} {{{{end}}}}'").split()[0]
def val(D, k): return f"$(grep ^{k}= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
D = lambda e: st[f"b_db_{e}"]
res = {}
res["redis"]  = sh(f"docker run --rm --network {net(D('redis'))} -e REDISCLI_AUTH={val(D('redis'),'REDIS_PASSWORD')} redis:7-alpine redis-cli -h t2-redis ping")[1].strip()
res["keydb"]  = sh(f"docker run --rm --network {net(D('keydb'))} -e REDISCLI_AUTH={val(D('keydb'),'REDIS_PASSWORD')} redis:7-alpine redis-cli -h t2-keydb ping")[1].strip()
res["dragonfly"] = sh(f"docker run --rm --network {net(D('dragonfly'))} -e REDISCLI_AUTH={val(D('dragonfly'),'DFLY_requirepass')} redis:7-alpine redis-cli -h t2-dragonfly ping")[1].strip()
res["mysql"]  = sh(f"docker run --rm --network {net(D('mysql'))} -e MYSQL_PWD={val(D('mysql'),'MYSQL_PASSWORD')} mysql:8.4 mysql -h t2-mysql -uapp app -N -e 'select 1'")[1].strip()
res["mariadb"] = sh(f"docker run --rm --network {net(D('mariadb'))} -e MYSQL_PWD={val(D('mariadb'),'MARIADB_PASSWORD')} mariadb:11 mariadb --skip-ssl -h t2-mariadb -uapp app -N -e 'select 1'")[1].strip()
res["mongodb"] = sh(f"docker run --rm --network {net(D('mongodb'))} mongo:8 mongosh --quiet \"mongodb://root:{val(D('mongodb'),'MONGO_INITDB_ROOT_PASSWORD')}@t2-mongodb:27017/?authSource=admin\" --eval 'db.runCommand({{ping:1}}).ok'")[1].strip()
res["clickhouse"] = sh(f"docker exec musdash-db-{D('clickhouse')} sh -c 'clickhouse-client --user \"$CLICKHOUSE_USER\" --password \"$CLICKHOUSE_PASSWORD\" -q \"select 1\"'")[1].strip()
want = {"redis": "PONG", "keydb": "PONG", "dragonfly": "PONG", "mysql": "1", "mariadb": "1", "mongodb": "1", "clickhouse": "1"}
for e, w in want.items():
    check(f"S8.2-{e}", res[e].endswith(w), f"{e} starts and accepts an authenticated connection: {res[e][-80:]!r}", evidence=res[e])
