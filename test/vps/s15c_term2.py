from lib import *
c = owner_client(); st = state(); db = st["pg"]; sv = st["minio"]; ORIGIN = f"http://{HOST}:8000"
def term(path, csrfpage, cmd, expect):
    token = csrf_of(c, csrfpage)
    ws = WS(path, c.cookies, origin=ORIGIN)
    if ws.status != 101: ws.close(); return f"handshake {ws.status}"
    ws.send(1, json.dumps(dict(csrf=token, cols=100, rows=30)).encode()); time.sleep(1.5); ws.send(2, cmd.encode()); out = ws.read_until(expect, 10); ws.close(); return out
o = term(f"/databases/{db}/terminal/ws", f"/databases/{db}/terminal", "psql --version; echo DB-$((20+22))\n", "DB-42")
check("S15.2a", "DB-42" in o and "psql" in o, f"database terminal opens a shell in the PostgreSQL container: {re.sub(chr(13), '', o)[-140:]!r}", sev="S2")
o = term(f"/services/{sv}/terminal/ws", f"/services/{sv}/terminal", "hostname; echo SV-$((20+22))\n", "SV-42")
check("S15.2b", "SV-42" in o, f"service terminal opens a shell in the stack's container: {re.sub(chr(13), '', o)[-140:]!r}", sev="S2")
# a terminal for a stopped resource
c.post(f"/databases/{db}/stop", dict(_csrf=csrf_of(c, f"/databases/{db}"))); time.sleep(6)
ws = WS(f"/databases/{db}/terminal/ws", c.cookies, origin=ORIGIN); check("S15.2c", ws.status in (409, 404), f"terminal of a stopped database is refused with a clear status ({ws.status})", sev="S3"); ws.close()
c.post(f"/databases/{db}/start", dict(_csrf=csrf_of(c, f"/databases/{db}")))
R = Client(token=ensure_tokens(c)["read_token"]); wait_for(lambda: R.get(f"/api/v1/databases/{db}").json().get("status") == "running" or None, 120, 3)
