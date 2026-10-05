from lib_b import *
from wsclient import WS
import json, socket
c = owner_client(); A = state()["b_app"]
h = c.get(f"/apps/{A}/terminal", follow=True)
wsu = re.search(r'data-terminal="([^"]+)"', h.text).group(1); tok = re.search(r'data-csrf="([^"]+)"', h.text).group(1); O = f"http://{HOST}:8000"
def op():
    w = WS(HOST, 8000, wsu, c.cookies, origin=O); w.send(json.dumps({"csrf": tok, "cols": 80, "rows": 24})); return w
def drain(w, secs=6):
    end = time.time() + secs
    while time.time() < end:
        try: r = w.recv(max(0.2, end - time.time()))
        except socket.timeout: continue
        except (EOFError, ConnectionError, OSError): return ("eof",)
        if r[0] == "close": return r
        if r[0] == "bin" and b"\x1b[6n" in r[1]:
            try: w.send(b"\x1b[1;1R", 2)
            except OSError: return ("eof",)
    return None
for label, size, op_ in [("70 KB", 70000, 2), ("1 MB", 1 << 20, 2)]:
    w = op(); time.sleep(1)
    try: w.send(b"A" * size, op_)
    except Exception as e: pass
    r = drain(w); alive = Client().get("/healthz").status
    check(f"S15.7-{size}", (r is not None and r[0] in ("close", "eof")) and alive == 200, f"{label} message: {r}; /healthz {alive}")
    w.close(); time.sleep(2)
w = op(); time.sleep(1)
w.s.sendall(bytes([0x82, 0x05]) + b"hello"); r = drain(w)
check("S15.7b", r is not None and r[0] in ("close", "eof"), f"unmasked client frame: {r}"); w.close()
w = op(); time.sleep(1); w.send(b"x" * 100, 9)  # oversized control frame (>125)
r = drain(w); check("S15.7c", r is not None and r[0] in ("close", "eof"), f"ping frame with 100 bytes payload is fine, 200 is not: {r}")
w.close(); w = op(); time.sleep(1); w.send(b"x" * 200, 9); r = drain(w); check("S15.7d", r is not None and r[0] in ("close", "eof"), f"control frame >125 bytes closes: {r}"); w.close()
print("procs left:", shout("pgrep -fc 'docker exec --interactive --tty'"))
