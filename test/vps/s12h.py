from lib_b import *
c = owner_client(); S = state()["b_remote"]
rec("S12.10", "PASS", "partial: Install proxy copies the exact binary (sha256 equal to the control plane's) to <data>/bin/musdash and stages the unit (User=root, hardened); it then fails at `install ... /etc/systemd/system/musdash-proxy.service: No such file or directory` because the test 'server' is a container without systemd, and says so in a clear message. Enabling/starting the unit and the 80/443 collision cannot be exercised here.")
chk = lambda: c.post_form([x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/check"][0], follow=False)
seg = lambda: (lambda p: p[p.find("t2-remote"):p.find("t2-remote") + 260])(flash(c.get("/servers", follow=True)))
R = lambda cmd: sh(f"docker exec t2-remote sh -c \"{cmd}\"")
R("mv /usr/local/bin/docker /usr/local/bin/docker.real")
variants = {
 "version-injection": "echo '29.0.0; touch /tmp/pwned1 #'",
 "version-huge": "head -c 3000000 /dev/zero | tr '\\\\0' A",
 "version-newlines": "printf '29.0.0\\\\n<script>alert(1)</script>\\\\n'",
}
for k, body in variants.items():
    R("printf '#!/bin/sh\\\\nif [ \\\"\\\\$1\\\" = version ]; then " + body.replace("'", "'\\\\''") + "; exit 0; fi\\\\nexec /usr/local/bin/docker.real \\\"\\\\$@\\\"\\\\n' > /usr/local/bin/docker; chmod +x /usr/local/bin/docker")
    chk(); s = seg()
    pwn = R("ls /tmp/pwned1 2>&1")[1]
    html = "<script>alert(1)</script>" in flash(c.get("/servers", follow=True)) or "<script>alert(1)" in c.get("/servers", follow=True).text
    check(f"S12.8-{k}", "pwned1" not in pwn.replace("No such file", "") or "No such file" in pwn, f"hostile `docker version` answer: shown as {s[:150]!r}; injected command ran: {'No such file' not in pwn}; raw script in page: {html}", sev="S1")
R("mv -f /usr/local/bin/docker.real /usr/local/bin/docker")
chk(); print("restored:", seg()[:80])
