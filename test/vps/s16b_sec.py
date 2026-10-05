from lib import *
import socket, threading
c = owner_client(); st = state(); web = st["web"]; who = st["who"]; pid = st["proj"]; env = st["env"]; sink = st["sink"]
XSS = "<img src=x onerror=alert(1)>\"'><svg/onload=alert(2)>"
# S16.3 XSS: tags, env value, task name, channel name, project description
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/tags", tags=XSS)
c.submit(f"/apps/{web}/environment", action=f"/apps/{web}/environment", vars=f"XSSVAR={XSS}\nA_TEAM={{{{team.TEAMV}}}}\n")
c.submit(f"/apps/{web}/tasks", action=f"/apps/{web}/tasks", name=XSS, command="true", schedule="@daily")
c.submit(f"/projects/{pid}/settings", action=f"/projects/{pid}", name="t-proj-renamed", description=XSS)
leaks = {}
for p in [f"/apps/{web}/settings", f"/apps/{web}/environment", f"/apps/{web}/tasks", f"/projects/{pid}", f"/projects/{pid}/settings", "/", "/tags"]:
    t = c.get(p).text
    if "<img src=x" in t or "<svg/onload" in t: leaks[p] = True
check("S16.3a", not leaks, f"HTML in tags/env values/task names/descriptions is escaped on every page. Unescaped on: {list(leaks)}", sev="S1")
# log output escaping: container prints markup; live log stream must escape
sh(f"docker exec $(docker ps -q --filter name=musdash-{web} | head -1) sh -c 'echo \"<script>alert(9)</script>\" >> /proc/1/fd/1'")
time.sleep(1)
def sse(path, secs=5):
    s = socket.create_connection((HOST, 8000), timeout=secs); ck = "; ".join(f"{k}={v}" for k, v in c.cookies.items())
    s.sendall(f"GET {path} HTTP/1.1\r\nHost: {HOST}:8000\r\nCookie: {ck}\r\nAccept: text/event-stream\r\n\r\n".encode()); buf = b""; end = time.time() + secs
    while time.time() < end:
        try:
            d = s.recv(8192)
            if not d: break
            buf += d
        except socket.timeout: break
    s.close(); return buf.decode("utf-8", "replace")
out = sse(f"/apps/{web}/logs/stream?tail=200", 6)
check("S16.3b", "<script>alert(9)" not in out and ("&lt;script&gt;" in out or "alert(9)" not in out), "live log stream HTML-escapes container output", sev="S1", evidence=out[-200:])
# deployment log page escapes error text derived from user input
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/settings", name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/<script>alert(7)</script>", health_cmd="", health_timeout="5", start_command="", docker_options="")
d = deploy(c, web); dep_wait(c, web, d, 60)
pg = c.get(f"/apps/{web}/deployments/{d}").text
check("S16.3c", "<script>alert(7)" not in pg, "deployment page escapes values taken from settings", sev="S1")
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/settings", name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/tags", tags="")
for f in parse_forms(c.get(f"/apps/{web}/tasks").text): pass
# S16.4 IDOR across kinds
dbid = "qfoenpmqqgvg"
cross = {f"/databases/{web}": None, f"/apps/{dbid}": None, f"/services/{web}": None, f"/projects/{web}": None, f"/apps/{pid}": None, f"/environments/{web}/variables": None, f"/servers/{web}/metrics": None,
         f"/apps/{dbid}/terminal/ws": None, f"/databases/{web}/terminal": None, f"/services/{web}/terminal": None, f"/apps/{web}/deployments/{pid}": None, f"/apps/{who}/deployments/{web}": None}
for p in cross: cross[p] = c.get(p).status
check("S16.4", all(v == 404 for v in cross.values() if v is not None) or all(v in (404, 400) for v in cross.values()), f"ids of one kind used for another resolve to 404: {cross}", sev="S1")
# S16.8 log stream cap (16)
socks = []
for i in range(19):
    s = socket.create_connection((HOST, 8000), timeout=5); ck = "; ".join(f"{k}={v}" for k, v in c.cookies.items())
    s.sendall(f"GET /apps/{web}/logs/stream HTTP/1.1\r\nHost: {HOST}:8000\r\nCookie: {ck}\r\nAccept: text/event-stream\r\n\r\n".encode()); socks.append(s)
time.sleep(2)
codes = []
for s in socks:
    try:
        s.settimeout(1); d = s.recv(200).decode(errors="replace"); codes.append(d.split("\r\n")[0])
    except Exception: codes.append("open(no data)")
for s in socks: s.close()
rejected = [x for x in codes if "429" in x or "503" in x or "Too" in x]
check("S16.8", len(rejected) >= 1, f"19 simultaneous log streams: {len(rejected)} refused (cap 16). first lines: {sorted(set(codes))}", sev="S3")
# S16.11 body and header limits
big = "x" * (12 * 1024 * 1024)
r = c.post("/account/profile", dict(_csrf=csrf_of(c), name=big, email="owner@musdash.test"))
check("S16.11a", r.status in (400, 413, 422), f"12 MB form body -> {r.status}", sev="S3")
try:
    r = c.get("/", headers={"X-Big": "a" * 70000}); check("S16.11b", r.status in (400, 431, 494, 413), f"70 KB header -> {r.status}", sev="S3")
except Exception as e:
    rec("S16.11b", "PASS", f"70 KB header connection refused/reset: {e!r}")
r = c.post("/projects", dict(_csrf=csrf_of(c), name="x", description="y" * 200000)); check("S16.11c", r.status in (400, 413, 422), f"200 KB field -> {r.status}", sev="S4")
