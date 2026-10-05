from lib_b import *
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
r = c.get(f"/projects/{P}?env={E}", follow=True)
svcs = re.findall(r'href="/services/([a-z2-7]{12})"[^>]*>\s*(?:<[^>]+>\s*)*(t2-(?:bad|sb|port|missing|ext|collide|connect)[\w\-]*)', r.text)
n = 0
for S, name in svcs:
    f = c.find_form(parse_forms(c.get(f"/services/{S}/settings", follow=True).text), f"/services/{S}/delete")
    if f: c.post_form(f, follow=False, confirm=name, delete_data=True); n += 1
time.sleep(10)
print("deleted", n, "left:", len(re.findall(r'href="/services/', c.get(f"/projects/{P}?env={E}", follow=True).text)))
print(shout("docker ps --format '{{.Names}} {{.Image}}' | grep -v -E 'musdash-(db|vnk|cma|ucd)'; docker network ls -q | wc -l; free -m | sed -n 2,3p"))
