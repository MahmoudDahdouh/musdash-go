from lib_b import *
import base64
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"; A = state()["b_app"]; SV = "ttd6yamy3cmb"
def getvars(path):
    f = [x for x in parse_forms(c.get(path, follow=True).text) if x["action"] == path][0]
    return [x["value"] for x in f["fields"] if x["name"] == "vars"][0]
def setvars(path, text):
    f = [x for x in parse_forms(c.get(path, follow=True).text) if x["action"] == path][0]
    rr = c.post_form(f, follow=False, vars=text)
    return rr.status, re.findall(r'field-error[^>]*>([^<]*)', rr.text)
teamv = getvars("/team/variables"); print("team before:", repr(teamv))
print(setvars("/team/variables", teamv + "T_VAL=team-value\n"))
print(setvars(f"/projects/{P}/variables", "P_VAL=project-value\n"))
st, err = setvars(f"/projects/{P}/variables", "P_VAL=project-value\nNESTED={{team.T_VAL}}\n")
check("S13.4a", st == 422 and err, f"a shared variable naming another is refused at save: {err}", sev="S3")
st, err = setvars(f"/projects/{P}/variables", "P_VAL=project-value\nLIT=\\{{team.T_VAL}}\n")
print("escape in shared var:", st, err)
setvars(f"/projects/{P}/variables", "P_VAL=project-value\nLIT=\\{{team.T_VAL}}\nBRACES=a {{ b }} c\n") if st == 422 else None
def deploy_app(appvars):
    setvars(f"/apps/{A}/environment", appvars)
    f = [x for x in parse_forms(c.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
    n0 = shout("ls /var/lib/musdash/logs/deployments | wc -l"); c.post_form(f, follow=False)
    wait_for(lambda: shout("ls /var/lib/musdash/logs/deployments | wc -l") != n0, 30, 1)
    logf = shout("ls -t /var/lib/musdash/logs/deployments/*.log | head -1")
    wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"tail -2 {logf}")) and 1, 90, 3)
    return shout(f"tail -2 {logf}")
res = deploy_app("A_TEAM={{team.T_VAL}}\nA_PROJ={{project.P_VAL}}\nA_ENV={{environment.E_VAL}}\nA_SRV={{server.S_VAL}}\nA_EMBED=pre-{{project.P_VAL}}-post\nA_ESC=\\{{team.T_VAL}}\nA_PLAIN=plain\n")
print(res)
envs = shout(f"docker ps --format '{{{{.Names}}}}' | grep -E 'musdash-{A}' | head -1 | xargs -I{{}} docker exec {{}} env | grep '^A_' | sort")
print(envs)
kv = dict(l.split("=", 1) for l in envs.splitlines() if "=" in l)
check("S13.1", kv.get("A_TEAM") == "team-value" and kv.get("A_PROJ") == "project-value" and kv.get("A_ENV") == "env-value" and kv.get("A_SRV") == "server-value" and kv.get("A_EMBED") == "pre-project-value-post", f"team/project/environment/server/embedded all expand: {kv}")
check("S13.3", kv.get("A_ESC") == "{{team.T_VAL}}", f"backslash-escaped name reaches the app literally: {kv.get('A_ESC')!r}")
stored = getvars(f"/apps/{A}/environment")
check("S13.2", "{{team.T_VAL}}" in stored and "team-value" not in stored, f"stored text keeps the names: {stored[:120]!r}")
