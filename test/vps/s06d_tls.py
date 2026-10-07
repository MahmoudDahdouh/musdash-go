from lib import *
import ssl, socket, subprocess
c = owner_client(); web = state()["web"]
HT = "t-tls." + HOST.replace(".", "-") + ".traefik.me"
for f in parse_forms(c.get(f"/apps/{web}/domains").text):
    if f["action"].startswith(f"/apps/{web}/domains/") and f["action"].endswith("/delete") and "t-tls" in c.get(f"/apps/{web}/domains").text[c.get(f"/apps/{web}/domains").text.find(f["action"])-400:c.get(f"/apps/{web}/domains").text.find(f["action"])]: c.post_form(f)
c.submit(f"/apps/{web}/settings", action=f"/apps/{web}/domains", host=HT, tls=True); time.sleep(3)
# S6.4 HTTP redirects a TLS host
r = Client(f"http://{HT}").get("/page?x=1", follow=False)
check("S6.4a", r.status in (301, 302, 307, 308) and r.location.startswith("https://" + HT) and "x=1" in r.location, f"HTTP -> HTTPS redirect: {r.status} {r.location}")
# S6.3 certificate issuance on first request
t0 = time.time()
ctx = ssl.create_default_context()
def fetch():
    s = socket.create_connection((HOST, 443), timeout=30)
    ss = ctx.wrap_socket(s, server_hostname=HT)
    cert = ss.getpeercert(); ver = ss.version(); ss.close(); return cert, ver
got = None; err = None
try:
    got = fetch()
except Exception as e:
    err = repr(e)
dt = time.time() - t0
if got:
    cert, ver = got
    issuer = dict(x[0] for x in cert["issuer"]); sans = [v for k, v in cert["subjectAltName"]]
    check("S6.3a", HT in sans and "Let's Encrypt" in issuer.get("organizationName", ""), f"valid chain, issuer {issuer.get('organizationName')} / {issuer.get('commonName')}, SAN {sans}, TLS {ver}, first handshake took {dt:.1f}s", sev="S2")
    check("S6.12a", ver in ("TLSv1.2", "TLSv1.3"), f"TLS version {ver}", sev="S2")
else:
    rec("S6.3a", "FAIL", f"HTTPS handshake with verification failed after {dt:.0f}s: {err}", "S2")
    print(shout("journalctl -u musdash-proxy --since '-3 min' --no-pager | tail -15"))
rr = Client(f"https://{HT}").get("/") if got else Resp(0, {}, b"")
check("S6.3b", rr.status == 200 and "nginx" in rr.text.lower(), f"HTTPS request served by the app: {rr.status}")
# S6.5 SNI of an unrouted name: refused, no ACME order
before = shout("journalctl -u musdash-proxy --no-pager | grep -ci acme")
p = subprocess.run(["openssl", "s_client", "-connect", f"{HOST}:443", "-servername", f"unrouted-{int(time.time())}.{HOST}.sslip.io"], input=b"", capture_output=True, timeout=30)
out = (p.stdout + p.stderr).decode(errors="replace")
refused = ("alert" in out.lower() or "no peer certificate" in out.lower() or "handshake failure" in out.lower()) and "BEGIN CERTIFICATE" not in out
check("S6.5a", refused, "TLS to an unrouted name is refused without a certificate", sev="S2", evidence=out[-300:])
time.sleep(2); after = shout("journalctl -u musdash-proxy --no-pager | grep -ci acme")
check("S6.5b", before == after, f"no ACME activity for the unrouted name (log lines {before} -> {after})", sev="S2")
# ACME challenge path on port 80 answers for a TLS host (404 for an unknown token is fine)
r = Client(f"http://{HT}").get("/.well-known/acme-challenge/doesnotexist", follow=False)
rec("S6.4b", "INFO", f"/.well-known/acme-challenge/<unknown> on a TLS host -> {r.status}")
# HTTP-only host proxies instead of redirecting
r = Client(f"http://t-web.{HOST}.sslip.io").get("/", follow=False); check("S6.4c", r.status == 200, f"HTTP-only host serves over HTTP: {r.status}")
# TLS protocol/cipher sanity
for proto in ("-tls1", "-tls1_1"):
    p = subprocess.run(["openssl", "s_client", "-connect", f"{HOST}:443", "-servername", HT, proto], input=b"", capture_output=True, timeout=30)
    o = (p.stdout + p.stderr).decode(errors="replace")
    check("S16.12" + proto.replace("-", ""), "BEGIN CERTIFICATE" not in o and "Cipher is (NONE)" in o or "alert" in o.lower() or "no protocols available" in o.lower(), f"{proto} refused", sev="S3", evidence=o[-150:])
