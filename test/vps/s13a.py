from lib_b import *
c = owner_client(); P, E = "l4u3323oxpmm", "jyrxc6zjhvih"; A = state()["b_app"]; SV = "ttd6yamy3cmb"
def setvars(path, text):
    f = [x for x in parse_forms(c.get(path, follow=True).text) if x["action"] == path][0]
    return c.post_form(f, vars=text)
setvars("/team/variables", "T_VAL=team-value\nNEST=outer-{{project.P_VAL}}\n")
setvars(f"/projects/{P}/variables", "P_VAL=project-value\nLOOPY={{project.LOOPY}}\nEMBED=has-{{team.T_VAL}}-inside\n")
setvars(f"/environments/{E}/variables", "E_VAL=env-value\n")
setvars(f"/servers/{SV}/variables", "S_VAL=server-value\n")
def deploy_and_env(appvars, A=A):
    setvars(f"/apps/{A}/environment", appvars)
    f = [x for x in parse_forms(c.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
    c.post_form(f, follow=False)
    def done():
        t = shout(f"cat $(ls -t /var/lib/musdash/logs/deployments/*.log | xargs grep -l {A} 2>/dev/null | head -1) 2>/dev/null; ls -t /var/lib/musdash/logs/deployments | head -1")
        return True
    time.sleep(2)
    ok = wait_for(lambda: re.search(r"(Deployed|Failed)", shout(f"tail -3 $(ls -t /var/lib/musdash/logs/deployments/*.log | head -1)")) and shout(f"tail -3 $(ls -t /var/lib/musdash/logs/deployments/*.log | head -1)"), 90, 3)
    return ok
ok = deploy_and_env("A_TEAM={{team.T_VAL}}\nA_PROJ={{project.P_VAL}}\nA_ENV={{environment.E_VAL}}\nA_SRV={{server.S_VAL}}\nA_EMBED=pre-{{project.P_VAL}}-post\nA_ESC=\\{{team.T_VAL}}\nA_NEST={{team.NEST}}\nA_LOOP={{project.LOOPY}}\nA_PLAIN=plain\n")
print(ok)
envs = shout(f"docker ps --format '{{{{.Names}}}}' | grep -E 'musdash-{A}' | head -1 | xargs -I{{}} docker exec {{}} env | grep '^A_' | sort")
print(envs)
