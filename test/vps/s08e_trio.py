from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; API = Client(token=ensure_tokens(c)["read_token"])
for eng in ("keydb", "dragonfly", "clickhouse"):
    name = "t-" + eng; t0 = time.time()
    r = c.submit(f"/projects/{pid}/databases/new?env={env}&engine={eng}", action=f"/projects/{pid}/databases", name=name)
    m = re.search(r"/databases/([a-z2-7]{12})", r.url or r.text); db = m.group(1) if m else None
    if not db: rec("S8.2" + eng[:3], "FAIL", f"{eng}: not created: {flash(r)[-120:]}", "S2"); continue
    ok = wait_for(lambda: API.get(f"/api/v1/databases/{db}").json().get("status") in ("running", "failed") and API.get(f"/api/v1/databases/{db}").json().get("status") or None, 900, 5)
    check("S8.2" + eng[:3], ok == "running", f"{eng}: starts healthy ({ok}) in {time.time()-t0:.0f}s incl. image pull", sev="S2")
    bp = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get(f"/databases/{db}/backups").text))
    check("S9.5c" + eng[:3], "not backed up" in bp.lower() or "cannot be backed up" in bp.lower() or "yet" in bp.lower() and not any(f["action"].endswith("/backups") for f in parse_forms(c.get(f"/databases/{db}/backups").text) if f["action"].endswith(f"/databases/{db}/backups")), f"{eng}: Backups page says it is not backed up yet: {bp[170:330]!r}", sev="S3")
    df = [x for x in parse_forms(c.get(f"/databases/{db}/settings").text) if x["action"] == f"/databases/{db}/delete"][0]; c.post_form(df, confirm=name, delete_data=True); time.sleep(6)
    sh("docker image prune -af >/dev/null 2>&1")
