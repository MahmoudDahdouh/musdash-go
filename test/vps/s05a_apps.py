from lib import *
c = owner_client(); st = state(); aid = st["web"]; pid = st["proj"]
host = f"t-web.{HOST}.sslip.io"

# S5.1 first deployment already ran: check log and container
r = c.get(f"/apps/{aid}/deployments")
dep = re.findall(r"/deployments/([a-z2-7]{12})", r.text)[0]
log = c.get(f"/apps/{aid}/deployments/{dep}")
txt = re.sub(r"<[^>]+>", " ", log.text)
check("S5.1a", "Succeeded" in txt, "first deployment succeeded")
steps = [s for s in ("pull", "network", "health", "route", "stopp") if s in txt.lower()]
rec("S5.1b", "INFO", f"deploy log mentions steps {steps}", evidence=re.sub(r"\s+", " ", txt)[:700])
rc, ins = sh("docker inspect $(docker ps -q --filter label=musdash.managed=true --filter name=musdash-%s) --format '{{json .HostConfig}}' | head -c 2500" % aid)
hc = json.loads(ins.strip().splitlines()[0]) if ins.strip().startswith("{") else {}
shape = dict(priv=hc.get("Privileged"), restart=hc.get("RestartPolicy", {}).get("Name"), pid=hc.get("PidMode"), net=hc.get("NetworkMode"), ports=hc.get("PortBindings"), caps=hc.get("CapAdd"), binds=hc.get("Binds"))
port = list((hc.get("PortBindings") or {}).values() or [[{}]])[0][0]
check("S5.4", hc and not hc["Privileged"] and port.get("HostIp") == "127.0.0.1" and 20000 <= int(port.get("HostPort", 0)) <= 29999 and hc["RestartPolicy"]["Name"] in ("unless-stopped", "always", "on-failure") and not hc.get("PidMode") and not hc.get("CapAdd"),
      f"container shape: {shape}", sev="S1")

# S5.2 domain
r = c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/domains", host=host, tls=False)
check("S5.2a", host in c.get(f"/apps/{aid}/domains").text, "domain added on the Domains tab")
time.sleep(3)
out = vcurl("http://127.0.0.1/", host)
check("S5.2b", out.startswith("200") and "nginx" in out.lower(), f"through the proxy with Host header: {out[:60]!r}")
cc = Client(f"http://{HOST}"); r = cc.request("GET", "/", host=host)
check("S5.2c", r.status == 200 and "nginx" in r.text.lower(), f"from the Mac over the internet via {host}: {r.status}")
rn = Client(f"http://{host}").get("/")
check("S5.2d", rn.status == 200, f"real DNS name {host} resolves and serves: {rn.status}")
# S6.2 unknown host
r = cc.request("GET", "/", host="nothing-here.example.test")
check("S6.2", r.status in (404, 502, 503) and "t-web" not in r.text and "127.0.0.1" not in r.text, f"unknown Host -> {r.status} {re.sub(r'<[^>]+>', ' ', r.text)[:80]!r}")
rr = open(os.devnull)
rt = shout("cat /var/lib/musdash/proxy/routes.json | head -c 800; stat -c '%a' /var/lib/musdash/proxy/routes.json")
check("S6.11a", host in rt, "routes.json contains the host after the change", evidence=rt)
check("S0.3b", rt.splitlines()[-1] == "600", "routes.json mode " + rt.splitlines()[-1])

# S5.3 SSE deploy log
import socket
def sse(path, secs=8):
    s = socket.create_connection((HOST, 8000), timeout=secs); ck = "; ".join(f"{k}={v}" for k, v in c.cookies.items())
    s.sendall(f"GET {path} HTTP/1.1\r\nHost: {HOST}:8000\r\nCookie: {ck}\r\nAccept: text/event-stream\r\n\r\n".encode())
    buf = b""; end = time.time() + secs
    while time.time() < end:
        try:
            d = s.recv(4096)
            if not d: break
            buf += d
        except socket.timeout: break
    s.close(); return buf.decode("utf-8", "replace")
d2 = deploy(c, aid)
out = sse(f"/apps/{aid}/deployments/{d2}/stream", 25)
check("S5.3", "text/event-stream" in out and ("data:" in out or "event:" in out), f"SSE deploy log streams: {len(out)} bytes; sample {re.sub(chr(10), ' | ', out[-300:])!r}")
res = dep_wait(c, aid, d2); check("S5.5a", res == "success", f"redeploy finished: {res}")
save_state(dep1=dep, dep2=d2)

# S5.10 logs
lg = c.get(f"/apps/{aid}/logs"); out = sse(f"/apps/{aid}/logs/stream", 6)
check("S5.10", lg.status == 200 and "text/event-stream" in out, f"logs page {lg.status}, stream {len(out)} bytes")
