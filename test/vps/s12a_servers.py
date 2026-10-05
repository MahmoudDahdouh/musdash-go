from lib import *
c = owner_client(); st = state(); sid = "ttd6yamy3cmb"
r = c.get("/servers"); t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", r.text)); print(t[150:1500])
for p in [f"/servers/{sid}/metrics", f"/servers/{sid}/variables"]: dump_forms(c, p)
