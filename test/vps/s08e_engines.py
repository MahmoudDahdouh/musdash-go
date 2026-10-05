from lib_b import *
import sys
c = owner_client(); st = state(); P, E = st["b_project"], st["b_env"]
ENG = sys.argv[1:] or ["redis", "mysql", "mariadb", "mongodb", "keydb", "dragonfly", "clickhouse"]
for eng in ENG:
    name = f"t2-{eng}"
    r = c.submit(f"/projects/{P}/databases/new?env={E}&engine={eng}", action=f"/projects/{P}/databases", name=name)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url)
    if not m:
        rec(f"S8.2-{eng}", "FAIL", "create failed", "S2", flash(r)[:300]); continue
    D = m.group(1)
    ok = wait_for(lambda: "Running" in flash(c.get(f"/databases/{D}", follow=True)), 240, 5)
    health = shout(f"docker inspect musdash-db-{D} --format '{{{{.Config.Image}}}} {{{{if .State.Health}}}}{{{{.State.Health.Status}}}}{{{{end}}}} {{{{.State.Status}}}}'")
    keys = shout(f"docker inspect musdash-db-{D} --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | cut -d= -f1 | grep -iE 'pass|user|db|name' | tr '\\n' ' '")
    page = flash(c.get(f"/databases/{D}", follow=True))
    conn = re.search(r"Connect from this environment.*?Copy Host", page)
    print(f"--- {eng} {D}: ui_running={bool(ok)} docker={health} env={keys}\n    {conn.group(0)[-160:] if conn else page[200:400]}")
    save_state(**{f"b_db_{eng}": D})
