from lib_b import *
import socket
c = owner_client(); D = "qfoenpmqqgvg"; S = f"/databases/{D}/settings"
s = socket.socket(); s.settimeout(8)
try: s.connect((HOST, 30000)); reach = True
except Exception as e: reach = False
s.close()
pw = f"$(grep ^POSTGRES_PASSWORD= /var/lib/musdash/apps/{D}/env | cut -d= -f2-)"
base = f"docker run --rm postgres:17-alpine"
good = sh(f"docker run --rm -e PGPASSWORD={pw} postgres:17-alpine psql -h {HOST} -p 30000 -U postgres -tAc 'select 1'")[1].strip()
bad = sh(f"docker run --rm -e PGPASSWORD=wrong postgres:17-alpine psql -h {HOST} -p 30000 -U postgres -tAc 'select 1'")[1].strip()
check("S8.5a", reach and good == "1" and "authentication failed" in bad, f"Mac reaches {HOST}:30000 tcp={reach}; right password ok={good=='1'}; wrong password refused", evidence=bad[:200])
c.submit(S, action=S, has="memory_mb", public=False, public_port="", memory_mb="256", cpus="0.5", image="postgres:17-alpine")
wait_for(lambda: "Running" in flash(c.get(f"/databases/{D}", follow=True)), 120); time.sleep(3)
ports = shout(f"docker port musdash-db-{D}")
s = socket.socket(); s.settimeout(5)
try: s.connect((HOST, 30000)); still = True
except Exception: still = False
check("S8.5b", ports == "" and not still, f"closing removes the published port (docker port: {ports!r}, reachable={still})")
