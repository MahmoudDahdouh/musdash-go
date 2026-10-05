exec(open("s10d.py").read().split("# external volume, long syntax")[0])
S = state()["b_svc_minio"]
def pwhash(): return shout(f"docker inspect musdash-{S}-minio-1 --format '{{{{range .Config.Env}}}}{{{{println .}}}}{{{{end}}}}' | grep -E '^MINIO_ROOT_(USER|PASSWORD)=' | sha256sum | cut -c1-16")
def domains(): return re.findall(r"([a-z0-9]{8}\.168\.235\.65\.204\.sslip\.io) to (\w+:\d+)", flash(c.get(f"/services/{S}", follow=True)))
h0, d0 = pwhash(), domains()
f = [x for x in parse_forms(c.get(f"/services/{S}", follow=True).text) if x["action"] == f"/services/{S}/deploy"][0]
c.post_form(f, follow=False)
wait_for(lambda: "Deployed" in shout(f"tail -3 $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)") and shout(f"tail -1 $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)").endswith("Deployed.") and time.time() > 0, 180, 5)
time.sleep(10)
h1, d1 = pwhash(), domains()
check("S10.6", h0 == h1 and d0 == d1, f"generated user/password and endpoint domains stable across redeploy (hash {h0} -> {h1}); domains {d1}")
# S10.12 TLS on endpoint domain: look for a TLS toggle
dump_forms(c, f"/services/{S}/settings")
r = c.get(f"/services/{S}", follow=True); print(sorted(set(re.findall(r'href="(/services/[^"]*)"', r.text))))
