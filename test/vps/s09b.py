from lib_b import *
import gzip
c = owner_client(); st = state(); D = "qfoenpmqqgvg"
net = shout(f"docker inspect musdash-db-{D} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}} {{{{end}}}}'").split()[0]
pw = f"$(grep ^POSTGRES_PASSWORD= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
q = lambda sql: sh(f"docker run --rm --network {net} -e PGPASSWORD={pw} postgres:17-alpine psql -h t2-pg -U postgres -tAc \"{sql}\"")[1].strip()
forms = parse_forms(c.get(f"/databases/{D}/backups", follow=True).text)
rf = [f for f in forms if f["action"].endswith("/restore")][0]
# S9.6 download
dl = re.search(r'href="(/databases/%s/backups/[^"]+/download)"' % D, c.get(f"/databases/{D}/backups", follow=True).text).group(1)
r = c.get(dl)
try: raw = gzip.decompress(r.body); ok = len(raw) > 0 and b"t2" in raw
except Exception as e: raw, ok = str(e).encode(), False
check("S9.6", r.status == 200 and ok, f"download {r.status}, {len(r.body)} bytes gzip, dump mentions table t2: {ok}; headers {r.h('content-disposition')}")
# S9.5 wrong name refused
q("select count(*) from t2")
c.post_form(rf, confirm="wrong")
check("S9.5a", q("select count(*) from t2") == "1", "wrong database name does not restore (table still intact)")
q("drop table t2")
miss = q("select to_regclass('t2')")
r = c.post_form(rf, confirm="wrong")
check("S9.5b", q("select to_regclass('t2')") == "", "wrong name with table dropped: still not restored", evidence=flash(r)[:200])
# S9.4 restore
r = c.post_form(rf, confirm="t2-pg")
back = wait_for(lambda: q("select x from t2") == "42" and "42", 120, 4)
status = flash(c.get(f"/databases/{D}", follow=True))
check("S9.4", back and re.search(r"t2-pg t2-pg Running", status), f"restore with the name brings the table back; database Running", evidence=flash(r)[150:400])
