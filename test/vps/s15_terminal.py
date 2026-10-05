from lib import *
c = owner_client(); st = state(); web = st["web"]; token = csrf_of(c, f"/apps/{web}/terminal")
ORIGIN = f"http://{HOST}:8000"
def open_term(path=None, origin=ORIGIN, csrf=None, cookies=None):
    ws = WS(path or f"/apps/{web}/terminal/ws", cookies if cookies is not None else c.cookies, origin=origin)
    if ws.status == 101 and csrf is not False:
        ws.send(1, json.dumps(dict(csrf=csrf or token, cols=100, rows=30)).encode())
    return ws
# page itself
pg = c.get(f"/apps/{web}/terminal"); check("S15.0", pg.status == 200 and "terminal.js" in pg.text, f"terminal page renders and loads terminal.js ({pg.status})")
# S15.1 happy path
ws = open_term(); check("S15.1a", ws.status == 101, f"WebSocket handshake with same Origin + session -> {ws.status}")
time.sleep(1); ws.send(2, b"echo marker-$((6*7)); id -un; hostname; echo $TERM; exit\n")
out = ws.read_until("exit", 10) if False else ws.read_until("The shell has ended", 10)
check("S15.1b", "marker-42" in out, f"typed command ran and its output came back: {re.sub(chr(13), '', out)[:200]!r}")
check("S15.1c", "xterm-256color" in out, "TERM=xterm-256color set")
ws.close()
# S15.6 closing the page ends the exec
ws = open_term(); time.sleep(1); ws.send(2, b"sleep 300\n"); time.sleep(2)
n1 = shout("pgrep -fc 'docker exec --interactive --tty'"); ws.close(); time.sleep(4); n2 = shout("pgrep -fc 'docker exec --interactive --tty'")
check("S15.6", int(n1) > int(n2), f"closing the socket ends the `docker exec` process on the server ({n1} -> {n2})", sev="S2")
rec("S15.6b", "INFO", "sleep process left in container: " + shout(f"docker exec $(docker ps -q --filter name=musdash-{web} | head -1) sh -c 'ps | grep -c \"sleep 300\"'") + " (a PTY hangup normally ends it)")
# S15.3 handshake rules
t = {}
for label, kw in {"no origin": dict(origin=None), "other origin": dict(origin="http://evil.example"), "same host other scheme/port": dict(origin=f"http://{HOST}:8001"), "null origin": dict(origin="null"), "no cookie": dict(cookies={})}.items():
    w = open_term(**kw) if label != "no cookie" else WS(f"/apps/{web}/terminal/ws", {}, origin=ORIGIN)
    t[label] = w.status; w.close()
check("S15.3a", all(v != 101 for v in t.values()), f"handshake refused without a same-origin page and session: {t}", sev="S1")
ws = open_term(csrf="A" * 40); r = ws.read_until("zzzz", 4); check("S15.3b", "CLOSE" in r or ws.status != 101, f"wrong form token: connection closed before any shell ({r[:60]!r})", sev="S1")
ws.close()
ws = open_term(csrf=False); time.sleep(11); r = ws.read_until("zzzz", 3); check("S15.3c", "CLOSE" in r or r == "", f"no first message within 10 s -> closed ({r[:50]!r})", sev="S3"); ws.close()
# other team's container id: 404 (S16.4) ; wrong kind
w = WS(f"/apps/{st['proj']}/terminal/ws", c.cookies, origin=ORIGIN); check("S15.8", w.status == 404, f"terminal for a non-app id -> {w.status}", sev="S1"); w.close()
# S15.7 oversize frame
ws = open_term(); time.sleep(1)
try:
    ws.send(2, b"A" * 70000); r = ws.read_until("zzzz", 4)
except Exception as e: r = repr(e)
check("S15.7", "CLOSE" in r or "Broken" in r or "closed" in r.lower() or "Reset" in r, f"a 70 KB message closes the socket: {r[:80]!r}", sev="S3")
ws.close(); check("S15.7b", Client().get("/healthz").status == 200, "server unaffected")
# S16.10 what the shell can reach
ws = open_term(); time.sleep(1)
ws.send(2, b"ls -l /var/run/docker.sock 2>&1 | head -1; ls /var/lib/musdash 2>&1 | head -1; id; cat /proc/1/cgroup | head -1; mount | grep -c docker.sock; echo DONE-$((1+1))\n")
out = ws.read_until("DONE-2", 10); ws.close()
check("S16.10", "docker.sock" not in out.replace("ls -l /var/run/docker.sock", "") or "No such file" in out, f"container shell has no docker socket or host data dir: {re.sub(chr(13), '', out)[-420:]!r}", sev="S1")
# S15.4 cap of 8
socks = []
for i in range(10):
    w = open_term(); socks.append(w)
codes = [w.status for w in socks]
check("S15.4", codes.count(101) <= 8 and codes.count(503) >= 1, f"10 terminals at once: statuses {codes} (cap 8, others 503)", sev="S3")
for w in socks: w.close()
