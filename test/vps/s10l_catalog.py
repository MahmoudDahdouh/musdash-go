from lib_b import *
import sys
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
def deploy_tpl(tpl, name):
    forms = parse_forms(c.get(f"/projects/{P}/services/new?env={E}&template={tpl}", follow=True).text)
    f = [x for x in forms if x["action"] == f"/projects/{P}/services"][0]
    rr = c.post_form(f, follow=True, name=name, deploy=True)
    return re.search(r"/services/([a-z2-7]{12})", rr.url).group(1)
for tpl, name in [tuple(a.split(":")) for a in sys.argv[1:] if ":" in a]:
    S = deploy_tpl(tpl, name)
    def done():
        t = shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* 2>/dev/null | head -1) 2>/dev/null")
        return t if re.search(r"Failed:|Deployed\.", t) else None
    l = wait_for(done, 420, 6) or "(timeout)"
    page = flash(c.get(f"/services/{S}", follow=True))
    doms = re.findall(r"([a-z0-9]{8}\.168\.235\.65\.204\.sslip\.io) to (\w[\w\-]*:\d+)", page)
    gen = re.findall(r"(SERVICE_[A-Z0-9_]+) ••", page)
    code = []
    for d, t in doms:
        try: code.append(Client(f"http://{d}", timeout=25).get("/").status)
        except Exception as e: code.append(str(e)[:40])
    ok = "Deployed." in l and doms and all(isinstance(x, int) and x < 500 for x in code)
    check(f"S10.1-{tpl}", ok, f"deployed={'Deployed.' in l}; endpoints {doms} -> HTTP {code}; generated {gen}", evidence=l[-300:])
    save_state(**{f"b_svc_{tpl}": S})
    if "--keep" not in sys.argv:
        f = c.find_form(parse_forms(c.get(f"/services/{S}/settings", follow=True).text), f"/services/{S}/delete")
        c.post_form(f, follow=False, confirm=name, delete_data=True); time.sleep(8)
