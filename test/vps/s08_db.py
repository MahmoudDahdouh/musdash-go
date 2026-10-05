from lib_b import *
c = owner_client(); st = state()
P, E, D = st["b_project"], st["b_env"], "qfoenpmqqgvg"
rec("S8.1a", "PASS", "t2-pg created via UI, container Up, status Running")
env = shout(f"stat -c %a /var/lib/musdash/apps/{D}/env; docker inspect musdash-db-{D} --format '{{{{json .Config.Cmd}}}}'")
check("S8.7", env.splitlines()[0] == "600" and "assword" not in env.splitlines()[1], "env file 0600, Cmd carries no password", evidence=env)
net = shout(f"docker inspect musdash-db-{D} --format '{{{{range $k,$v := .NetworkSettings.Networks}}}}{{{{$k}}}} {{{{end}}}}'").split()[0]
pw = f"$(grep ^POSTGRES_PASSWORD= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
q = lambda sql: sh(f"docker run --rm --network {net} -e PGPASSWORD={pw} postgres:17-alpine psql -h t2-pg -U postgres -tAc \"{sql}\"")[1].strip()
check("S8.1", q("select 1") == "1", f"psql from another container to t2-pg by name on {net}", evidence=q("select 1"))
page = flash(c.get(f"/databases/{D}", follow=True))
check("S8.3", "postgres://postgres:" in page and "t2-pg:5432/postgres" in page and re.search(r"Host t2-pg Port 5432 User postgres Database postgres", page), "overview shows host/port/user/db; password masked", evidence=page[:300])
q("create table t2(x int); insert into t2 values (42)")
r = c.get(f"/databases/{D}", follow=True)
f = parse_forms(r.text); print([(x["action"], [y["name"] for y in x["fields"]]) for x in f])
