from lib import *
c = owner_client()
def pid_of(r):
    m = re.search(r"/projects/([a-z2-7]{12})", r.url or "") or re.search(r'href="/projects/([a-z2-7]{12})', r.text); return m.group(1) if m else None
def create(name, desc=""):
    r = c.submit("/projects/new", action="/projects", name=name, description=desc); return r
r = create("t-proj-one", "first test project")
m = re.search(r'/projects/([a-z2-7]{12})', r.text)
pid = [x for x in re.findall(r'/projects/([a-z2-7]{12})', r.text) if x][0]
check("S4.1a", "t-proj-one" in r.text, f"project created and shown (id {pid})")
env = re.findall(r"env=([a-z2-7]{12})", r.text)[0]
check("S4.1b", "production" in r.text.lower() or "environment" in r.text.lower(), "default environment exists")
home = c.get("/")
check("S4.1c", "t-proj-one" in home.text, "listed on the dashboard")
save_state(proj=pid, env=env)

# S4.2 rename
c.submit(f"/projects/{pid}/settings", action=f"/projects/{pid}", name="t-proj-renamed", description="renamed")
check("S4.2", "t-proj-renamed" in c.get(f"/projects/{pid}").text, "project renamed")

# S4.3 environments
r = c.submit(f"/projects/{pid}/settings", action=f"/projects/{pid}/environments", name="staging")
r = c.get(f"/projects/{pid}/settings")
envs = re.findall(r"/environments/([a-z2-7]{12})/delete", r.text)
check("S4.3a", len(envs) >= 1 and "staging" in r.text, f"environment 'staging' added; delete forms: {len(envs)}")
for e in envs:
    f = [x for x in parse_forms(r.text) if x["action"] == f"/environments/{e}/delete"][0]
    print("env delete fields:", [(x['name'], x['type']) for x in f["fields"]])
    c.post_form(f, **{x["name"]: "staging" for x in f["fields"] if x["type"] == "text"})
r = c.get(f"/projects/{pid}/settings")
check("S4.3b", "/delete" in r.text and "staging" not in re.sub(r"<[^>]+>", " ", r.text).split("Environments")[-1][:400] or len(re.findall(r"/environments/([a-z2-7]{12})/delete", r.text)) < len(envs) or len(envs) == 0, "environment deleted", evidence=flash(r)[-300:])

# S4.4 hostile names
cases = {"empty": "", "spaces": "   ", "long": "x" * 300, "traversal": "../../etc/passwd", "xss": "<script>alert(1)</script>", "shell": "a;rm -rf /;`id`$(id)", "unicode": "тест-проект-日本語-🙂",
         "newline": "a\nb", "null": "a\x00b", "quote": "it's \"quoted\""}
res = {}
for k, v in cases.items():
    try:
        r = create(v)
        ok_stored = (v.strip() and r.status == 200 and "New project" not in r.text[:3000])
        raw_xss = "<script>alert(1)</script>" in r.text
        res[k] = (r.status, "stored" if ok_stored else "refused", raw_xss)
    except Exception as e:
        res[k] = ("err", repr(e)[:60], False)
print(res)
check("S4.4a", not any(v[2] for v in res.values()), "no unescaped markup echoed back", sev="S1", evidence=res)
check("S4.4b", res["empty"][1] == "refused" and res["spaces"][1] == "refused" and res["long"][1] == "refused", f"empty/blank/300-char names refused: {res['empty']}, {res['spaces']}, {res['long']}", sev="S3")
dash = c.get("/")
check("S4.4c", "<script>alert(1)</script>" not in dash.text and "&lt;script&gt;" in dash.text or res["xss"][1] == "refused", "XSS name escaped or refused on the dashboard", sev="S1")
rec("S4.4d", "INFO", "name handling: " + json.dumps(res, ensure_ascii=False))
# cleanup projects we created through hostile names
home = c.get("/")
for p in set(re.findall(r'href="/projects/([a-z2-7]{12})"', home.text)):
    if p == pid: continue
    s = c.get(f"/projects/{p}/settings")
    nm = re.findall(r'name="name" type="text" value="([^"]*)"', s.text)
    f = [x for x in parse_forms(s.text) if x["action"] == f"/projects/{p}/delete"]
    if f: c.post_form(f[0], confirm=(nm[0] if nm else "x"))
