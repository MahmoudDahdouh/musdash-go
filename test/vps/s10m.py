exec(open("s10l_catalog.py").read().split("for tpl, name in")[0])
S = deploy_tpl("n8n", "t2-n8n")
wait_for(lambda: "Deployed." in shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)"), 300, 6)
page = flash(c.get(f"/services/{S}", follow=True)); d = re.search(r"([a-z0-9]{8}\.168\.235\.65\.204\.sslip\.io) to n8n", page).group(1)
res = []
for i in range(12):
    try: r = Client(f"http://{d}", timeout=20).get("/"); res.append((r.status, r.h("server"), len(r.body)))
    except Exception as e: res.append(str(e)[:30])
    if res[-1][0] == 200 if isinstance(res[-1], tuple) else False: break
    time.sleep(10)
print(res)
print(shout(f"docker logs --tail 5 musdash-{S}-n8n-1 2>&1 | cut -c1-160; docker inspect musdash-{S}-n8n-1 --format '{{{{.State.Health.Status}}}}'"))
r = Client(f"http://{d}", timeout=20).get("/healthz"); print("healthz", r.status)
f = c.find_form(parse_forms(c.get(f"/services/{S}/settings", follow=True).text), f"/services/{S}/delete")
c.post_form(f, follow=False, confirm="t2-n8n", delete_data=True)
