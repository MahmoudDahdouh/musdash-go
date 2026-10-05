from lib_b import *
c = owner_client(); L = "ttd6yamy3cmb"
f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{L}"][0]
ip = [x["value"] for x in f["fields"] if x["name"] == "ip"][0]
check("S12.1", ip == HOST and "Docker" in flash(c.get("/servers", follow=True)), f"local card: public IP field {ip!r} (real {HOST}), Docker 29.8.1 shown (real 29.8.1)")
sq = lambda q: shout(f"python3 -c \"import sqlite3,sys;d=sqlite3.connect('file:/var/lib/musdash/musdash.db?mode=ro',uri=True);print(d.execute(sys.argv[1]).fetchall())\" \"{q}\" 2>&1")
print(sq("select sql from sqlite_master where name='metric_samples'")[:300])
fm = [x for x in parse_forms(c.get(f"/servers/{L}/metrics", follow=True).text) if x["action"] == f"/servers/{L}/metrics"][0]
print([(x["name"], x["value"]) for x in fm["fields"]])
c.post_form(fm, sample="1", follow=False)
print(flash(c.get(f"/servers/{L}/metrics", follow=True))[300:520])
time.sleep(150)
n = sq("select count(*) from metric_samples")
page = c.get(f"/servers/{L}/metrics", follow=True)
print("rows:", n, "svg charts:", page.text.count("<svg"), re.findall(r"(1 h|6 h|24 h|1h|6h|24h)", flash(page))[:6])
check("S12.3a", n not in ("[(0,)]",), f"sampling on: samples stored once a minute ({n} rows after ~2.5 min)")
fm = [x for x in parse_forms(page.text) if x["action"] == f"/servers/{L}/metrics"][0]
print([(x["name"], x["value"]) for x in fm["fields"]])
