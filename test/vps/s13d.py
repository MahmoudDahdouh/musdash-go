exec(open("s13b.py").read().split("teamv = getvars")[0])
# S13.8 service variables file
forms = parse_forms(c.get(f"/projects/{P}/services/new?env={E}&template=custom", follow=True).text)
f = [x for x in forms if x["action"] == f"/projects/{P}/services"][0]
comp = "services:\n  a:\n    image: nginx:alpine\n    environment:\n      GOT: ${GOT}\n      ENVV: ${ENVV}\n"
rr = c.post_form(f, follow=True, name="t2-svcvars", compose=comp, variables="GOT={{project.P_VAL}}\nENVV={{environment.E_VAL}}-{{team.T_VAL}}\n", deploy=True)
S = re.search(r"/services/([a-z2-7]{12})", rr.url).group(1)
wait_for(lambda: re.search(r"Failed:|Deployed\.", shout(f"cat $(ls -t /var/lib/musdash/logs/*/*{S}* | head -1)")) and 1, 120, 4)
env = shout(f"docker exec musdash-{S}-a-1 env | grep -E '^(GOT|ENVV)='")
check("S13.8", "GOT=project-value" in env and "ENVV=env-value-team-value" in env, f"{{{{...}}}} expands in a service's variables: {env!r}")
# S13.9 tags
dump_forms(c, f"/services/{S}/settings")
for pth in [f"/apps/{A}/settings", "/tags"]:
    print(pth, [(f["action"], [x["name"] for x in f["fields"]]) for f in parse_forms(c.get(pth, follow=True).text) if "tag" in f["action"]])
save_state(b_svc_vars=S)
