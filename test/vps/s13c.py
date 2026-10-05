exec(open("s13b.py").read().split("teamv = getvars")[0])
def deploy_app(appvars):
    setvars(f"/apps/{A}/environment", appvars)
    f = [x for x in parse_forms(c.get(f"/apps/{A}/environment", follow=True).text) if x["action"] == f"/apps/{A}/deploy"][0]
    n0 = shout("ls /var/lib/musdash/logs/deployments | wc -l"); c.post_form(f, follow=False)
    wait_for(lambda: shout("ls /var/lib/musdash/logs/deployments | wc -l") != n0, 30, 1)
    logf = shout("ls -t /var/lib/musdash/logs/deployments/*.log | head -1")
    wait_for(lambda: re.search(r"Deployed\.|Failed:", shout(f"tail -2 {logf}")) and 1, 90, 3)
    return shout(f"tail -2 {logf}")
envof = lambda: dict(l.split("=", 1) for l in shout(f"docker ps --format '{{{{.Names}}}}' | grep -E 'musdash-{A}' | head -1 | xargs -I{{}} docker exec {{}} env | grep '^A_'").splitlines() if "=" in l)
# S13.4b: the value of a shared var holding escaped text is not expanded again
res = deploy_app("A_LIT={{project.LIT}}\n"); kv = envof()
check("S13.4b", kv.get("A_LIT") not in (None, "team-value") and "team-value" not in kv.get("A_LIT", ""), f"expanded value is not read again: A_LIT={kv.get('A_LIT')!r}", evidence=res)
# S13.5 missing name
res = deploy_app("A_MISS={{project.NOPE_NOT_HERE}}\n")
check("S13.5a", "Failed" in res and "NOPE_NOT_HERE" in res and "project-value" not in res, f"missing name fails the deployment naming the variable, not a value: {res[-220:]}")
bad = shout(f"docker ps --format '{{{{.Names}}}}' | grep -c 'musdash-{A}'")
check("S13.5b", bad == "1", f"old container still serving after a failed deployment ({bad})")
# S13.6 no inheritance: a container that names nothing sees nothing
res = deploy_app("A_PLAIN=plain\n"); kv = envof()
alle = shout(f"docker ps --format '{{{{.Names}}}}' | grep -E 'musdash-{A}' | head -1 | xargs -I{{}} docker exec {{}} env")
check("S13.6", "T_VAL" not in alle and "P_VAL" not in alle and "team-value" not in alle and "project-value" not in alle and "server-value" not in alle, "an app that names no shared variable receives none of them")
