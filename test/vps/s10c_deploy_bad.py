from lib_b import *
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"
r = c.get(f"/projects/{P}?env={E}", follow=True)
ids = dict((n, i) for i, n in re.findall(r'href="/services/([a-z2-7]{12})"[^>]*>\s*(?:<[^>]+>\s*)*(t2-bad-[\w\-]+)', r.text))
before = set(shout("docker ps -aq").split())
for name, S in sorted(ids.items()):
    f = [x for x in parse_forms(c.get(f"/services/{S}", follow=True).text) if x["action"] == f"/services/{S}/deploy"][0]
    c.post_form(f, follow=False)
def settled(S):
    t = flash(c.get(f"/services/{S}", follow=True))
    return not re.search(r"Waiting|Running deployment|Deploying|Queued|Starting", t[:900])
for name, S in sorted(ids.items()):
    wait_for(lambda: settled(S), 120, 3)
    t = flash(c.get(f"/services/{S}", follow=True))
    i = t.find("Latest deployment")
    k = name[len("t2-bad-"):].replace("-", "_")
    msg = t[i:i+260]
    refused = bool(re.search(r"refus|not allowed|may not|cannot|Failed|failed", msg, re.I))
    rec(f"S10.4d-{k}", "PASS" if refused else "FAIL", msg, "" if refused else "S1")
after = set(shout("docker ps -aq").split())
new = after - before
img = shout("docker ps -a --format '{{.Names}} {{.Image}}' | grep -E 'musdash-(%s)' " % "|".join(ids.values())) if ids else ""
check("S10.4-nocontainers", img == "", f"no hostile stack started a container: {img!r}", sev="S1")
