from lib_b import *
c = owner_client()
def sq(q): return shout(f"python3 -c \"import sqlite3,sys;d=sqlite3.connect('file:/var/lib/musdash/musdash.db?mode=ro',uri=True);print(d.execute(sys.argv[1]).fetchall())\" \"{q}\" 2>&1")
print(sq("select name from sqlite_master where name like '%shared%'"))
print(sq("select scope_kind, count(*) from shared_vars group by 1") if "scope_kind" in sq("select sql from sqlite_master where name='shared_vars'") else sq("select sql from sqlite_master where name='shared_vars'"))
r = c.submit("/projects/new", action="/projects", name="t2-casc"); PID = re.search(r"/projects/([a-z2-7]{12})", r.url).group(1)
env = re.search(r"databases/new\?env=([a-z2-7]+)", c.get(f"/projects/{PID}", follow=True).text).group(1)
def setvars(path, text):
    f = [x for x in parse_forms(c.get(path, follow=True).text) if x["action"] == path][0]; return c.post_form(f, vars=text)
setvars(f"/projects/{PID}/variables", "CASC_P=1\n"); setvars(f"/environments/{env}/variables", "CASC_E=1\n")
q = f"select count(*) from shared_vars where scope_id in ('{PID}','{env}')"
before = sq(q)
f = [x for x in parse_forms(c.get(f"/projects/{PID}/settings", follow=True).text) if x["action"] == f"/projects/{PID}/delete"][0]
c.post_form(f, confirm="t2-casc", follow=False); time.sleep(2)
after = sq(q)
others = sq("select count(*) from shared_vars")
check("S13.10", before == "[(2,)]" and after == "[(0,)]", f"project's and environment's shared variables rows {before} -> {after} after deleting the project; table total now {others}")
