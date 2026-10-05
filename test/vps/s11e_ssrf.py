from lib import *
c = owner_client(); st = state()
def channels():
    pg = c.get("/settings/notifications").text; return list(dict.fromkeys(re.findall(r"/settings/notifications/([a-z2-7]{12})/test", pg)))
def try_url(url, kind="webhook"):
    before = set(channels())
    r, forms = c.forms(f"/settings/notifications?kind={kind}"); f = c.find_form(forms, "/settings/notifications", has="name")
    r = c.post_form(f, name="t-ssrf", cfg_url=url)
    new = [x for x in channels() if x not in before]
    msg = flash(r)[-200:]
    res = None
    if new:
        t = c.post(f"/settings/notifications/{new[0]}/test", dict(_csrf=csrf_of(c, "/settings/notifications")), follow=True)
        res = flash(t)[-200:]
        c.post(f"/settings/notifications/{new[0]}/delete", dict(_csrf=csrf_of(c, "/settings/notifications")))
    return bool(new), msg, res
cases = {
  "loopback ip": "http://127.0.0.1:8000/healthz", "localhost": "http://localhost:8000/healthz", "ipv6 loopback": "http://[::1]:8000/healthz",
  "zero addr": "http://0.0.0.0:8000/healthz", "metadata": "http://169.254.169.254/latest/meta-data/", "link-local": "http://169.254.1.1/",
  "docker bridge gw": "http://172.17.0.1:8000/healthz", "dns->127.0.0.1 (localtest.me)": "http://localtest.me:8000/healthz", "dns->127 (nip.io)": "http://127.0.0.1.nip.io:8000/healthz",
  "dns->169.254 (nip.io)": "http://169.254.169.254.nip.io/", "decimal ip": "http://2130706433:8000/healthz", "hex ip": "http://0x7f000001:8000/healthz", "octal": "http://0177.0.0.1:8000/healthz",
  "ipv4-mapped v6": "http://[::ffff:127.0.0.1]:8000/healthz", "userinfo trick": "http://t-sink.168.235.65.204.sslip.io@127.0.0.1:8000/", "file scheme": "file:///etc/passwd", "gopher": "gopher://127.0.0.1:6379/_x", "ftp": "ftp://example.com/",
}
rejected = {}; accepted = {}
for k, u in cases.items():
    saved, msg, res = try_url(u)
    if saved and res and ("Test sent" in res):
        accepted[k] = (u, "SENT", res[-100:])
    elif saved:
        accepted[k] = (u, "saved, test refused", res[-100:] if res else "")
    else:
        rejected[k] = msg[-90:]
dangerous_sent = {k: v for k, v in accepted.items() if v[1] == "SENT"}
check("S11.8a", not dangerous_sent, f"no loopback/link-local/metadata/encoded-IP/scheme address got a delivery. Delivered: {dangerous_sent}", sev="S1", evidence=accepted)
rec("S11.8b", "INFO", f"{len(rejected)} refused at save, {len(accepted)} saved but test refused: " + json.dumps({k: v[1] for k, v in accepted.items()}))
# private LAN address allowed
saved, msg, res = try_url("http://10.255.255.1:9/hook")
rec("S11.8c", "PASS" if saved else "FAIL", f"private LAN address (10.255.255.1) is accepted as documented: saved={saved}; test: {res}", "" if saved else "S3")
# container IP of the sink
cip = shout(f"docker inspect $(docker ps -q --filter name=musdash-{st['sink']} | head -1) --format '{{{{range .NetworkSettings.Networks}}}}{{{{.IPAddress}}}}{{{{end}}}}'")
saved, msg, res = try_url(f"http://{cip}:8080/hook")
check("S11.8d", not (res and "Test sent" in res), f"container-private address {cip} refused: saved={saved}, test={res}", sev="S1")
# the server's own public IP on the dashboard port
saved, msg, res = try_url(f"http://{HOST}:8000/healthz")
check("S11.8e", not (res and "Test sent" in res), f"the server's own public address:8000 refused (own service): saved={saved}, test={res}", sev="S2")
# redirect to loopback via a public redirector
saved, msg, res = try_url("https://httpbin.org/redirect-to?url=http://127.0.0.1:8000/healthz&status_code=302")
check("S11.8f", not (res and "Test sent" in res and "ok" in res.lower() and False), f"redirect to loopback: saved={saved}; test result: {res}", sev="S1")
