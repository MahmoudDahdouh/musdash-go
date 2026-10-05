from lib import *
c = owner_client(); st = state(); web = st["web"]; token = csrf_of(c, f"/apps/{web}/terminal"); ORIGIN = f"http://{HOST}:8000"
def shells():
    return int(shout(f"docker exec $(docker ps -q --filter name=musdash-{web} | head -1) sh -c 'ps -o pid,ppid,args | grep -c \"[ ]sh$\"'"))
before = shells()
for i in range(3):
    ws = WS(f"/apps/{web}/terminal/ws", c.cookies, origin=ORIGIN); ws.send(1, json.dumps(dict(csrf=token, cols=80, rows=24)).encode()); time.sleep(1.5)
    ws.send(2, b"echo hi\n"); time.sleep(0.5); ws.close(); time.sleep(2)
after = shells()
host_exec = shout("pgrep -fc 'docker exec --interactive --tty'")
check("S15.6c", after <= before, f"3 terminals opened then closed by dropping the socket: shells in the container {before} -> {after} (host `docker exec` clients left: {host_exec}). README: 'It ends when you leave the page.'", sev="S2",
      evidence=f"orphaned `sh` processes remain inside the app container after the page is closed; each one holds memory until the container restarts")
# when the shell exits by itself (typed exit) nothing is left
before = shells(); ws = WS(f"/apps/{web}/terminal/ws", c.cookies, origin=ORIGIN); ws.send(1, json.dumps(dict(csrf=token, cols=80, rows=24)).encode()); time.sleep(1.5); ws.send(2, b"exit\n"); ws.read_until("ended", 6); ws.close(); time.sleep(2)
check("S15.6d", shells() <= before, f"a shell that the user ends with `exit` leaves nothing behind ({before} -> {shells()})", sev="S3")
