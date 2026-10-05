from lib_b import *
import base64
c = owner_client(); ip = state()["b_remote_ip"]
# S12.5 address validation first
f = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == "/servers"][0]
for bad in ["", "host with space", "a;b", "-oProxyCommand=id", "$(id)", "999.1.1.1.1", "http://x"]:
    rr = c.post_form(f, follow=False, name="t2-bad", host=bad, port="22", ssh_user="root", key="new", data_dir="")
    made = "t2-bad" in flash(c.get("/servers", follow=True))
    rec("S12.5v", "PASS" if not made else "FAIL", f"host {bad!r}: {rr.status} created={made}", "" if not made else "S1")
for badu in ["root;id", "-oX", "a b", ""]:
    rr = c.post_form(f, follow=False, name="t2-badu", host=ip, port="22", ssh_user=badu, key="new", data_dir="")
    made = "t2-badu" in flash(c.get("/servers", follow=True))
    rec("S12.5u", "PASS" if not made else "FAIL", f"ssh_user {badu!r}: {rr.status} created={made}", "" if not made else "S1")
for badp in ["0", "70000", "22;x", "abc"]:
    rr = c.post_form(f, follow=False, name="t2-badp", host=ip, port=badp, ssh_user="root", key="new", data_dir="")
    made = "t2-badp" in flash(c.get("/servers", follow=True))
    rec("S12.5p", "PASS" if not made else "FAIL", f"port {badp!r}: {rr.status} created={made}", "" if not made else "S2")
for badd in ["relative/path", "/etc", "/", "/var/lib/musdash/../../etc", "/a b;c"]:
    rr = c.post_form(f, follow=False, name="t2-badd", host=ip, port="22", ssh_user="root", key="new", data_dir=badd)
    made = "t2-badd" in flash(c.get("/servers", follow=True))
    rec("S12.5d", "PASS" if not made else "INFO", f"data_dir {badd!r}: {rr.status} created={made}")
rr = c.post_form(f, follow=True, name="t2-remote", host=ip, port="22", ssh_user="root", key="new", data_dir="/var/lib/musdash")
print(rr.url, flash(rr)[170:900])
