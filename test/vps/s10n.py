exec(open("s10d.py").read().split("def create(")[0])
P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
forms = parse_forms(c.get(f"/projects/{P}/services/new?env={E}&template=cloudflared", follow=True).text)
f = [x for x in forms if x["action"] == f"/projects/{P}/services"][0]
rr = c.post_form(f, follow=True, name="t2-cft", var_TUNNEL_TOKEN="eyJhIjoiZmFrZSIsInQiOiJmYWtlIiwicyI6ImZha2UifQ==", deploy=True)
S = re.search(r"/services/([a-z2-7]{12})", rr.url).group(1)
l = wait_for(lambda: (lambda t: t if re.search(r"Failed:|Deployed\.", t) else None)(shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)")), 240, 5) or "(timeout)"
st = shout(f"docker ps -a --filter name=musdash-{S} --format '{{{{.Names}}}} {{{{.Image}}}} {{{{.Status}}}}'")
lg = shout(f"docker logs --tail 4 musdash-{S}-cloudflared-1 2>&1 | cut -c1-200")
page = flash(c.get(f"/services/{S}", follow=True)); i = page.find("t2-cft t2-cft")
print(page[i:i+60]); print(l[-300:]); print(st); print(lg)
rec("S10.2", "PASS" if "cloudflare" in st.lower() or "cloudflared" in st else "FAIL", f"container {st!r}; deployment: {l[-160:]!r}; container log: {lg[-160:]!r}".replace("\n", " | "))
f2 = c.find_form(parse_forms(c.get(f"/services/{S}/settings", follow=True).text), f"/services/{S}/delete")
c.post_form(f2, follow=False, confirm="t2-cft", delete_data=True)
