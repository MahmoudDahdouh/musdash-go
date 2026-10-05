from lib import *
oc = owner_client(); tok = oc.cookies["musdash_csrf"]
a = oc.request("PUT", "/projects", data=dict(_csrf=tok)); b = oc.request("DELETE", "/", data=dict(_csrf=tok))
check("S1.12", a.status in (404, 405) and b.status in (404, 405), f"PUT with token {a.status}, DELETE with token {b.status} (403 without a token is CSRF running before routing)", sev="S4")
