exec(open("s10d.py").read().split("# external volume, long syntax")[0])
import ssl, socket
S = state()["b_svc_minio"]
forms = [x for x in parse_forms(c.get(f"/services/{S}/settings", follow=True).text) if "/endpoints/" in x["action"]]
f = forms[1]
host = "t2-minio-s3.168.235.65.204.sslip.io"
r = c.post_form(f, host=host, tls=True)
print(flash(r)[:0], re.findall(r"(?:could not|cannot|must|already|invalid)[^.]{0,100}", flash(r))[:2])
time.sleep(25)
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
try:
    s = ctx.wrap_socket(socket.create_connection((HOST, 443), timeout=20), server_hostname=host)
    der = s.getpeercert(True); proto = s.version(); s.close()
    out = sh(f"echo | openssl s_client -connect 127.0.0.1:443 -servername {host} 2>/dev/null | openssl x509 -noout -issuer -subject -dates 2>&1")[1]
    check("S10.12", "Let's Encrypt" in out or "R1" in out or "R2" in out or "E5" in out or "E6" in out or "R10" in out or "R11" in out, f"TLS on service endpoint {host}: {proto}; {out.strip()[:250]}")
except Exception as e:
    rec("S10.12", "FAIL", f"no certificate for {host}: {e}", "S2")
cl = Client(f"https://{host}", timeout=20)
try: rr = cl.get("/minio/health/live"); print("https status", rr.status, rr.h("server"))
except Exception as e: print("https exc", e)
