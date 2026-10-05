from lib import *
c = owner_client(); st = state(); R = Client(token=ensure_tokens(c)["read_token"])
sid = [s for s in R.get("/api/v1/servers").json() if s["name"] == "t-remote"][0]["id"]; save_state(remote=sid)
pg = c.get("/servers").text; pub = re.findall(r"(ssh-ed25519 AAAA[A-Za-z0-9+/=]+) musdash", pg)[-1]
check("S12.5a", "PRIVATE KEY" not in pg and "OPENSSH PRIVATE" not in pg, "the private key is never shown on the Servers page", sev="S1")
def check_server():
    r = c.post(f"/servers/{sid}/check", dict(_csrf=csrf_of(c, "/servers")), follow=True)
    t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text)); i = t.find("t-remote"); return t[i:i + 700], r
# before the key is installed: a clear failure
t, r = check_server(); rec("S12.6a", "PASS" if "Running" not in t[:120] else "FAIL", f"Check before the public key is installed: {t[:260]!r}", "")
sh(f"docker exec t-remote sh -c 'echo \"{pub} musdash\" > /root/.ssh/authorized_keys; chmod 600 /root/.ssh/authorized_keys'")
t, r = check_server(); print(t)
fp_real = shout("docker exec t-remote ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub")
m = re.search(r"SHA256:[A-Za-z0-9+/]{43}", t)
check("S12.6b", m and m.group(0) in fp_real, f"first Check records the host key and shows its fingerprint {m.group(0) if m else None}; the server's own says {fp_real!r}", sev="S1")
check("S12.6c", all(w in t for w in ("Docker", "Compose")) and "git" in t.lower(), f"Check reports Docker, the Compose plugin and git: {t[:300]!r}", sev="S3")
# S12.13 pooling
conns = shout(f"ss -tn state established '( dport = :2222 )' 2>/dev/null | tail -n +2 | wc -l; ss -tn | grep -c ':2222'")
rec("S12.13a", "INFO", f"established SSH connections to the remote after one Check: {conns!r}")
# S12.9 deploy an app to the remote
pid = st["proj"]; env = st["env"]
pgn = c.get(f"/projects/{pid}/apps/new?env={env}").text; f = [x for x in parse_forms(pgn) if x["action"] == f"/projects/{pid}/apps"][0]
print([ (x["name"], x["type"], x.get("options")) for x in f["fields"] if x["name"] in ("server", "server_id")])
