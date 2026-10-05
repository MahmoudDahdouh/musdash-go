from lib_b import *
c = owner_client()
cip = shout("docker inspect musdash-db-qfoenpmqqgvg --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'")
def try_ep(name, ep):
    r = c.submit("/settings/storages", action="/settings/storages", has="bucket", name=name, endpoint=ep, region="us-east-1", bucket="t2-bucket", prefix="", access_key="AKIAFAKEFAKEFAKE", secret_key="fake-secret-fake-secret")
    t = flash(r)
    return name in t, re.findall(r"(?:cannot|must|not allowed|refus|invalid|private|loopback|internal|address)[^.]{0,100}", t, re.I)[:1]
ok, m = try_ep("t2-s3-control", "https://s3.us-east-1.amazonaws.com")
rec("S9.8-control", "PASS" if ok else "FAIL", f"valid public endpoint saves: {ok} {m}", "" if ok else "S2")
cases = {"loopback": "http://127.0.0.1:9000", "localhost": "http://localhost:9000", "containerip": f"http://{cip}:9000", "metadata": "http://169.254.169.254", "ipv6loopback": "http://[::1]:9000", "unspecified": "http://0.0.0.0:9000", "decimalip": "http://2130706433:9000", "hexip": "http://0x7f000001:9000", "userinfo": "http://127.0.0.1@example.com", "filescheme": "file:///etc/passwd", "flaglike": "--endpoint-url=http://x", "nip.io": "http://127.0.0.1.nip.io:9000", "privatelan": "http://192.168.1.50:9000"}
for k, ep in cases.items():
    ok, m = try_ep(f"t2-s3-{k}", ep)
    allowed = k == "privatelan"
    rec(f"S9.8-{k}", "PASS" if ok == allowed else "FAIL", f"{ep}: {'saved' if ok else 'refused'} {m}", "" if ok == allowed else "S1")
