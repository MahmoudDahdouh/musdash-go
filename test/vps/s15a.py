from lib_b import *
from wsclient import WS
import json
c = owner_client(); st = state(); A = st["b_app"]; D = "qfoenpmqqgvg"
def page(path):
    h = c.get(path, follow=True)
    return re.search(r'data-terminal="([^"]+)"', h.text).group(1), re.search(r'data-csrf="([^"]+)"', h.text).group(1)
wsu, tok = page(f"/apps/{A}/terminal")
O = f"http://{HOST}:8000"
def open_ws(path, cookies=None, origin=O, hello=True, token=None, extra=None):
    w = WS(HOST, 8000, path, c.cookies if cookies is None else cookies, origin=origin, extra=extra)
    if w.status == 101 and hello:
        w.send(json.dumps({"csrf": token or tok, "cols": 80, "rows": 24}))
    return w
# S15.1 (protocol-level): shell works, TERM, tty, container identity
w = open_ws(wsu); time.sleep(1)
w.send("echo MARK$((20+22)); tty; echo $TERM; hostname; id -un\n", 2)
out, cl = w.read_until("root", 8)
txt = out.decode("utf8", "replace")
check("S15.1p", "MARK42" in txt and "/dev/pts" in txt and "xterm-256color" in txt, f"shell in app container over the WebSocket: {txt.strip()[-160:]!r}")
cid = shout(f"docker ps --format '{{{{.ID}}}} {{{{.Names}}}}' | grep {A} | head -1")
check("S15.1c", cid.split()[0] in txt or cid.split()[0][:12] in txt, f"hostname is the app container's id ({cid})")
procs = shout("pgrep -fc 'docker exec --interactive --tty'"); print("docker exec procs while open:", procs)
# resize
w.send(json.dumps({"cols": 120, "rows": 40})); time.sleep(0.5); w.send("stty size\n", 2)
out, _ = w.read_until("40 120", 6); check("S15.1r", b"40 120" in out, f"resize message changes the tty size: {out.decode()[-60:]!r}")
w.close(); time.sleep(3)
after = shout("pgrep -fc 'docker exec --interactive --tty'")
check("S15.6", after == "0" or int(after) < int(procs), f"closing the page ends the docker exec ({procs} -> {after})")
# S15.3 handshake rules
for label, kw, want in [("cross-origin", dict(origin="http://evil.example"), 403), ("no-origin", dict(origin=None), 403)]:
    w = open_ws(wsu, hello=False, **kw); check(f"S15.3-{label}", w.status == want, f"{label}: handshake {w.status}", sev="S1"); w.close()
w = open_ws(wsu, cookies={}, hello=False); check("S15.3-nosession", w.status != 101, f"no session cookie: handshake {w.status} {w.head.splitlines()[0]}", sev="S1"); w.close()
w = open_ws(wsu, hello=True, token="x" * 40); r = None
try: r = w.recv(6)
except Exception as e: r = ("exc", str(e))
check("S15.3-badtoken", w.status == 101 and r[0] == "close", f"bad form token: socket closed with {r}", sev="S1"); w.close()
w = open_ws(wsu, hello=False); time.sleep(0.2)
try: w.send("hello"); r = w.recv(6)
except Exception as e: r = ("exc", str(e))
check("S15.3-nojsonhello", r[0] in ("close", "exc"), f"non-JSON first message: {r}", sev="S1"); w.close()
# S15.2 database terminal
dws, dtok = page(f"/databases/{D}/terminal")
w = open_ws(dws, token=dtok); time.sleep(1); w.send("hostname; psql --version\n", 2)
out, _ = w.read_until("psql (PostgreSQL)", 8); txt = out.decode("utf8", "replace")
check("S15.2", "psql (PostgreSQL)" in txt, f"database terminal lands in the Postgres container: {txt.strip()[-120:]!r}"); w.close()
