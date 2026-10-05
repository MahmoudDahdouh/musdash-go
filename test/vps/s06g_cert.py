from lib import *
import ssl, socket
c = owner_client(); D = f"dash.{HOST}.sslip.io"
c.submit("/settings", action="/settings", instance_domain=D, acme_email="owner@musdash.test"); time.sleep(3)
ctx = ssl.create_default_context(); t0 = time.time(); got = None; err = None
for i in range(4):
    try:
        s = socket.create_connection((HOST, 443), timeout=90); ss = ctx.wrap_socket(s, server_hostname=D); got = (ss.getpeercert(), ss.version(), ss.cipher()); ss.close(); break
    except Exception as e:
        err = repr(e); print(f"try {i} after {time.time()-t0:.0f}s: {err}")
if got:
    cert, ver, ci = got; issuer = dict(x[0] for x in cert["issuer"])
    check("S6.3", "Let's Encrypt" in issuer.get("organizationName", ""), f"real Let's Encrypt certificate for {D}: issuer {issuer}, expires {cert['notAfter']}, {ver} {ci[0]}, issued after {time.time()-t0:.0f}s", sev="S2")
    r = Client(f"https://{D}").get("/login")
    check("S6.13e", r.status == 200, f"dashboard served over HTTPS on its domain: {r.status}")
    ch = Client(f"https://{D}"); ch.get("/login")
    cookies = ch.cookies; 
    rec("S1.9c", "INFO", "Set-Cookie over HTTPS (Secure flag?): " + str([k for k in ch.cookies]))
else:
    rec("S6.3", "FAIL", f"no certificate after {time.time()-t0:.0f}s for {D}: {err}", "S2")
print(shout("ls -la /var/lib/musdash/proxy/certs"))
