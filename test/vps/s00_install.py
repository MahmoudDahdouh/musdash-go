from lib import *

rc, out = sh("systemctl is-active musdash-server musdash-proxy; id musdash; /usr/local/bin/musdash version; ls -la /usr/local/bin/musdash")
check("S0.1", out.count("active") >= 2 and "inactive" not in out and "d84c23f" in out and "uid=" in out, "services active, user exists, version d84c23f", evidence=out)

out = shout("ss -tlnpH | awk '{print $4, $6}'")
ports = {l.split()[0].rsplit(":", 1)[1] for l in out.splitlines() if l.split()[0].startswith(("0.0.0.0", "*", "[::]"))}
extra = ports - {"22", "80", "443", "8000"}
check("S0.2", {"80", "443", "8000"} <= ports and not extra, f"public listeners {sorted(ports)}", evidence=out)

out = shout("stat -c '%a %U %n' /var/lib/musdash /var/lib/musdash/master.key /var/lib/musdash/musdash.db /var/lib/musdash/proxy /var/lib/musdash/proxy/routes.json 2>&1")
modes = {l.split()[2]: l.split()[0] for l in out.splitlines() if len(l.split()) == 3}
ok = modes.get("/var/lib/musdash/master.key") == "600" and not (int(modes.get("/var/lib/musdash", "700")[-1]) & 7)
check("S0.3", ok, "master.key 0600, data dir not world-accessible", evidence=out)
if "/var/lib/musdash/proxy/routes.json" in modes:
    check("S0.3b", modes["/var/lib/musdash/proxy/routes.json"] == "600", "routes.json mode " + modes["/var/lib/musdash/proxy/routes.json"], evidence=out)

time.sleep(20)
out = shout("ps -o rss=,args= -C musdash")
rss = {}
for l in out.splitlines():
    n, _, a = l.strip().partition(" ")
    rss["proxy" if "proxy" in a else "server"] = int(n) / 1024
check("S0.4", rss.get("server", 99) < 30 and rss.get("proxy", 99) < 20, f"idle RSS server {rss.get('server'):.1f} MB, proxy {rss.get('proxy'):.1f} MB", evidence=out)

out = shout("systemctl show musdash-server musdash-proxy -p NoNewPrivileges -p ProtectSystem -p ProtectHome -p PrivateTmp -p User -p Restart -p MemoryMax -p Environment 2>&1; cat /etc/systemd/system/musdash-server.service")
rec("S0.5", "INFO", "unit hardening dump", evidence=out)
r = Client().get("/healthz")
check("S0.6", r.status == 200, f"/healthz {r.status} {r.text[:40]!r}")
rc, out = sh("/usr/local/bin/musdash migrate -data /var/lib/musdash 2>&1; echo rc=$?")
check("S0.8", "rc=0" in out, "musdash migrate no-op", evidence=out)
