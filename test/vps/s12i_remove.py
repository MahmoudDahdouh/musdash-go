from lib import *
c = owner_client(); st = state(); sid = st["remote"]; R = Client(token=ensure_tokens(c)["read_token"])
a = [x for x in R.get("/api/v1/apps").json() if x["name"] == "t-rweb"][0]["id"]
r = c.post(f"/servers/{sid}/delete", dict(_csrf=csrf_of(c, "/servers"), confirm="t-remote"), follow=True)
t = flash(r); still = sid in [s_["id"] for s_ in R.get("/api/v1/servers").json()]
check("S12.12a", still, f"removing a server that still runs an app is refused (the UI hides the button too): {t[-200:]!r}", sev="S2")
# delete the app (also removes its remote container), then remove the server
af = [x for x in parse_forms(c.get(f"/apps/{a}/settings").text) if x["action"] == f"/apps/{a}/delete"][0]; c.post_form(af, confirm="t-rweb"); time.sleep(8)
pg = c.get("/servers").text; f = [x for x in parse_forms(pg) if x["action"] == f"/servers/{sid}/delete"][0]
r = c.post_form(f, **{x["name"]: "t-remote" for x in f["fields"] if x["type"] == "text"}); time.sleep(3)
gone = sid not in [s["id"] for s in R.get("/api/v1/servers").json()]
mach = shout("docker exec t-remote sh -c 'ls /var/lib/musdash >/dev/null 2>&1 && echo datadir-kept; docker images -q | wc -l'")
check("S12.12b", gone and "datadir-kept" in mach, f"server removed from musdash; the machine's data directory and Docker are left alone ({mach!r})", sev="S2")
sh("docker rm -f t-remote >/dev/null 2>&1; docker image prune -af >/dev/null 2>&1")
