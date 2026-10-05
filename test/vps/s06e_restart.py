from lib import *
import socket, base64, os as _os
H = f"t-web.{HOST}.sslip.io"
def hits(n=300, delay=0.05):
    sh("rm -f /tmp/hits2; nohup sh -c 'for i in $(seq 1 %d); do curl -s -m 3 -o /dev/null -w \"%%{http_code}\\n\" -H \"Host: %s\" http://127.0.0.1/; sleep %s; done > /tmp/hits2' >/dev/null 2>&1 &" % (n, H, delay))
def result():
    time.sleep(1); out = shout("sort /tmp/hits2 | uniq -c"); return out.replace("\n", "; "), sum(int(l.split()[0]) for l in out.splitlines() if l.split()[1] != "200"), sum(int(l.split()[0]) for l in out.splitlines())
# S0.9 restart the control plane while the app is being hit
hits(300); time.sleep(2)
sh("systemctl restart musdash-server"); time.sleep(15)
s, bad, tot = result()
check("S0.9", bad == 0 and tot > 100, f"musdash-server restart: {tot} requests through the proxy, {bad} failed ({s})", sev="S1")
check("S0.9b", Client().get("/healthz").status == 200, "control plane back after restart")
# S0.10 proxy restart: routes reload from routes.json
t0 = time.time(); sh("systemctl restart musdash-proxy")
ok = wait_for(lambda: Client(f"http://{H}").get("/").status == 200, 30, 0.5)
check("S0.10", ok, f"after `systemctl restart musdash-proxy` the app answers again after {time.time()-t0:.1f}s", sev="S2")
# S17.3 kill -9 proxy: systemd restarts, server unaffected
pid = shout("systemctl show musdash-proxy -p MainPID --value"); sh(f"kill -9 {pid}")
ok = wait_for(lambda: Client(f"http://{H}").get("/").status == 200, 30, 0.5)
npid = shout("systemctl show musdash-proxy -p MainPID --value")
check("S17.3", ok and npid != pid and Client().get("/healthz").status == 200, f"kill -9 proxy ({pid}) -> restarted as {npid}, app serves again, control plane untouched", sev="S2")
# S6.11b SIGHUP reload picks up an edited routes.json? (server rewrites atomically and signals)
out = shout("journalctl -u musdash-proxy --since '-1 min' --no-pager | tail -3"); rec("S6.11b", "INFO", out)

# S6.12 WebSocket through the proxy (whoami /echo is a websocket echo)
def ws_echo(host, path="/echo"):
    s = socket.create_connection((HOST, 80), timeout=10)
    key = base64.b64encode(_os.urandom(16)).decode()
    s.sendall((f"GET {path} HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nOrigin: http://{host}\r\n\r\n").encode())
    hdr = s.recv(4096).decode(errors="replace")
    if " 101 " not in hdr.split("\r\n")[0]: return hdr.split("\r\n")[0]
    msg = b"hello-ws"; mask = _os.urandom(4)
    frame = bytes([0x81, 0x80 | len(msg)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(msg))
    s.sendall(frame); time.sleep(0.5); data = s.recv(4096); s.close()
    return data[2:] if data else b""
c = owner_client(); who = state()["who"]
for f in parse_forms(c.get(f"/apps/{who}/settings").text):
    if f["action"].startswith(f"/apps/{who}/domains/") and f["action"].endswith("/delete"): c.post_form(f)
HW = f"t-who.{HOST}.sslip.io"
c.submit(f"/apps/{who}/settings", action=f"/apps/{who}/domains", host=HW, tls=False); time.sleep(3)
r = ws_echo(HW)
check("S6.12", r == b"hello-ws", f"WebSocket upgrade through the proxy echoes: {r!r}", sev="S2")

# S6.15 large upload streams through the proxy; proxy RSS stays flat
rss0 = shout("ps -o rss= -p $(systemctl show musdash-proxy -p MainPID --value)")
out = sh(f"head -c 300000000 /dev/zero | curl -s -m 120 -o /dev/null -w '%{{http_code}} %{{size_upload}}' -H 'Host: {HW}' -X POST --data-binary @- http://127.0.0.1/")[1]
rss1 = shout("ps -o rss= -p $(systemctl show musdash-proxy -p MainPID --value)")
check("S6.15", int(rss1) < 60000, f"300 MB POST through the proxy: {out.strip()}; proxy RSS {int(rss0)/1024:.1f} -> {int(rss1)/1024:.1f} MB", sev="S3")
