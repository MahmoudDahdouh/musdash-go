from lib_b import *
from wsclient import WS
import json
c = owner_client(); st = state(); A = st["b_app"]; D = "qfoenpmqqgvg"; SV = st["b_svc_vars"]
h = c.get(f"/apps/{A}/terminal", follow=True)
wsu = re.search(r'data-terminal="([^"]+)"', h.text).group(1); tok = re.search(r'data-csrf="([^"]+)"', h.text).group(1)
O = f"http://{HOST}:8000"
def op(path=wsu):
    w = WS(HOST, 8000, path, c.cookies, origin=O)
    if w.status == 101: w.send(json.dumps({"csrf": tok, "cols": 80, "rows": 24}))
    return w
# S15.4 cap of 8
ws = []; refused = None
for i in range(10):
    w = op()
    if w.status != 101:
        refused = (i + 1, w.status, w.head.splitlines()[0], w.head.lower().count("retry-after")); w.close(); break
    ws.append(w); time.sleep(0.6)
check("S15.4", refused and refused[0] == 9 and refused[1] == 503, f"terminal #{refused[0] if refused else '?'} refused: {refused}")
for w in ws: w.close()
time.sleep(3)
print("docker exec left:", shout("pgrep -fc 'docker exec --interactive --tty'"))
# S15.7 oversize frame
w = op(); time.sleep(1)
try:
    w.send(b"A" * 70000, 2); time.sleep(1); r = w.recv(5)
except Exception as e: r = ("exc", str(e)[:60])
alive = Client().get("/healthz").status
check("S15.7", (r[0] in ("close", "exc")) and alive == 200, f"70 KB message: socket {r[:2]}; server still answers /healthz {alive}")
w.close(); time.sleep(2)
# oversized control frame / unmasked frame quick probe
import socket
w = op(); time.sleep(1)
try:
    w.s.sendall(bytes([0x82, 0x05]) + b"hello")  # unmasked client frame
    r = w.recv(5)
except Exception as e: r = ("exc", str(e)[:60])
check("S15.7b", r[0] in ("close", "exc"), f"unmasked client frame closes the socket: {r[:2]}"); w.close()
# S15.8 forged ids
for label, path in [("app id on /databases", f"/databases/{A}/terminal/ws"), ("db id on /apps", f"/apps/{D}/terminal/ws"), ("service id on /apps", f"/apps/{SV}/terminal/ws"), ("bogus", "/apps/zzzzzzzzzzzz/terminal/ws"), ("app id on /services", f"/services/{A}/terminal/ws")]:
    w = WS(HOST, 8000, path, c.cookies, origin=O); check(f"S15.8-{label}", w.status == 404, f"{label}: handshake {w.status}", sev="S1"); w.close()
# service terminal
sws = re.search(r'data-terminal="([^"]+)"', c.get(f"/services/{SV}/terminal", follow=True).text).group(1)
stok = re.search(r'data-csrf="([^"]+)"', c.get(f"/services/{SV}/terminal", follow=True).text).group(1)
w = WS(HOST, 8000, sws, c.cookies, origin=O); w.send(json.dumps({"csrf": stok, "cols": 80, "rows": 24})); time.sleep(1); w.send("hostname; env | grep -c GOT\n", 2)
out, _ = w.read_until("\n1", 8); txt = out.decode("utf8", "replace")
cid = shout(f"docker ps --format '{{{{.ID}}}}' --filter name=musdash-{SV}")
check("S15.2s", cid[:12] in txt, f"service terminal lands in the service's container {cid}: {txt.strip()[-100:]!r}"); w.close()
rec("S15.5", "BLOCKED", "idle close is a 30-minute timer (terminalIdle in handlers_terminal.go) with a 30 s ping; not waited for here. Code evidence only.")
