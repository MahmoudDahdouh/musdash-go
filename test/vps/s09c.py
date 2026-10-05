from lib_b import *
c = owner_client(); st = state(); D = "qfoenpmqqgvg"
# S9.5 redis restore not offered; clickhouse not backed up
rf = [f for f in parse_forms(c.get(f"/databases/{st['b_db_redis']}/backups", follow=True).text) if f["action"].endswith("/restore")]
check("S9.5c", not rf, "Redis offers no restore form")
t = flash(c.get(f"/databases/{st['b_db_clickhouse']}/backups", follow=True))
i = t.find("Backups"); print("clickhouse backups page:", t[170:520])
check("S9.5d", re.search(r"not (backed up|supported)|isn.t backed up|no backups? (for|of)|are not made", t, re.I) is not None, "ClickHouse says it is not backed up", sev="S3", evidence=t[170:600])
# S9.2 schedule
S = f"/databases/{D}/backups/schedule"
r = c.submit(f"/databases/{D}/backups", action=S, enabled=True, schedule="* * * * *", keep="2", storage_id="")
t = flash(r); m = re.search(r"Next scheduled backup:? ([^B]{0,60})", t)
check("S9.2a", m and "Not scheduled" not in m.group(1), f"next_run set: {m.group(1).strip() if m else t[300:500]}")
n0 = int(shout(f"ls /var/lib/musdash/backups/{D}/*.gz | wc -l"))
wait_for(lambda: int(shout(f"ls /var/lib/musdash/backups/{D}/*.gz | wc -l")) > n0, 150, 5)
n1 = int(shout(f"ls /var/lib/musdash/backups/{D}/*.gz | wc -l"))
check("S9.2b", n1 > n0, f"due schedule created a backup on its own ({n0}->{n1})")
time.sleep(130)
n2 = int(shout(f"ls /var/lib/musdash/backups/{D}/*.gz | wc -l"))
check("S9.3", n2 == 2, f"retention keeps newest 2 on disk (have {n2}) while schedule keeps making them", evidence=shout(f"ls -la /var/lib/musdash/backups/{D}/"))
rows = len(re.findall(r"Succeeded", flash(c.get(f"/databases/{D}/backups", follow=True))))
rec("S9.3b", "INFO", f"rows listing Succeeded on page: {rows}")
c.submit(f"/databases/{D}/backups", action=S, enabled=False, schedule="* * * * *", keep="2", storage_id="")
