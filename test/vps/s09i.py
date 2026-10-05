from lib_b import *
c = owner_client(); D = "qfoenpmqqgvg"
net = shout(f"docker inspect musdash-db-{D} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}} {{{{end}}}}'").split()[0]
pw = f"$(grep ^POSTGRES_PASSWORD= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
q = lambda sql: sh(f"docker run --rm --network {net} -e PGPASSWORD={pw} postgres:17-alpine psql -h t2-pg -U postgres -tAc \"{sql}\"", timeout=600)[1].strip()
print(q("create table if not exists big as select g, md5(g::text) a, md5((g*7)::text) b from generate_series(1,2500000) g; select pg_size_pretty(pg_total_relation_size('big'))"))
sq = lambda s: shout(f"python3 -c \"import sqlite3,sys;d=sqlite3.connect('file:/var/lib/musdash/musdash.db?mode=ro',uri=True);print(d.execute(sys.argv[1]).fetchall())\" \"{s}\" 2>&1")
print(sq("select sql from sqlite_master where name='backups'")[:400])
