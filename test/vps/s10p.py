exec(open("s10d.py").read().split("# external volume, long syntax")[0])
comp = """services:
  web:
    image: nginx:alpine
    environment:
      - SERVICE_FQDN_WEB_80
      - SERVICE_URL_WEB
      - SERVICE_PASSWORD_DB
      - SERVICE_BASE64_KEY
      - SERVICE_USER_ADMIN
"""
S = create("t2-magic", comp)
wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)")) and 1, 120, 4)
def env(): return dict(l.split("=", 1) for l in shout(f"docker exec musdash-{S}-web-1 env | grep '^SERVICE_'").splitlines() if "=" in l)
e1 = env(); print({k: (v if "FQDN" in k or "URL" in k else f"<{len(v)} chars>") for k, v in e1.items()})
f = [x for x in parse_forms(c.get(f"/services/{S}", follow=True).text) if x["action"] == f"/services/{S}/deploy"][0]
c.post_form(f, follow=False); time.sleep(5)
wait_for(lambda: shout(f"tail -1 $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)").endswith("Deployed."), 120, 4); time.sleep(3)
e2 = env()
host = e1.get("SERVICE_FQDN_WEB_80", "")
check("S10.6-magic", e1 == e2 and len(e1) == 5 and host.endswith("sslip.io") and e1.get("SERVICE_URL_WEB", "").startswith("http"), f"FQDN/URL/PASSWORD/BASE64/USER magic variables are generated once and equal after redeploy: {sorted(e1)}; fqdn={host}")
pw = e1.get("SERVICE_PASSWORD_DB", ""); b64 = e1.get("SERVICE_BASE64_KEY", "")
rec("S10.6-shape", "INFO", f"password {len(pw)} chars charset ok={bool(re.fullmatch(r'[A-Za-z0-9]+', pw))}; base64 value {len(b64)} chars")
save_state(b_svc_magic=S)
