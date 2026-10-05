from lib import *
host = f"t-web.{HOST}.sslip.io"
before_pid = shout("systemctl show musdash-server -p MainPID --value; systemctl show musdash-proxy -p MainPID --value").split()
key = shout("sha256sum /var/lib/musdash/master.key | cut -c1-16; stat -c %Y /var/lib/musdash/musdash.db")
sh("rm -f /tmp/hits3; nohup sh -c 'for i in $(seq 1 400); do curl -s -m 3 -o /dev/null -w \"%{http_code}\\n\" -H \"Host: " + host + "\" http://127.0.0.1/; sleep 0.1; done > /tmp/hits3' >/dev/null 2>&1 &")
time.sleep(2)
out = sh("cd /root && ./install/install.sh ./musdash-linux-amd64 2>&1 | tail -15", timeout=300)[1]
time.sleep(12)
hits = shout("sort /tmp/hits3 | uniq -c"); bad = sum(int(l.split()[0]) for l in hits.splitlines() if l.split()[1] != "200")
after_pid = shout("systemctl show musdash-server -p MainPID --value; systemctl show musdash-proxy -p MainPID --value").split()
key2 = shout("sha256sum /var/lib/musdash/master.key | cut -c1-16")
c = owner_client(); R = Client(token=ensure_tokens(c)["read_token"])
check("S0.7a", "is running" in out or "running" in out, f"installer re-run completes: {out.strip().splitlines()[-6:]}", sev="S2")
check("S0.7b", key.splitlines()[0] == key2 and "msd_" not in out, "master key unchanged by the re-run", sev="S1")
check("S0.7c", bad == 0, f"apps kept serving while the installer restarted the services: {hits.replace(chr(10), '; ')}", sev="S2")
check("S0.7d", R.get("/api/v1/apps").status == 200 and len(R.get("/api/v1/apps").json()) > 3, "data intact after the re-run (apps, accounts, tokens still valid)", sev="S1")
rec("S0.7e", "INFO", f"PIDs before {before_pid}, after {after_pid} (the proxy restarts too: {'same' if before_pid[1] == after_pid[1] else 'new'})")
