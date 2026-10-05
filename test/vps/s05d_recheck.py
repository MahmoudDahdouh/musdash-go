from lib import *
c = owner_client(); st = state(); aid = st["web"]
def settings(**kw):
    base = dict(name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
    base.update(kw); return c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", **base)
settings()
env = c.get(f"/apps/{aid}/environment").text
ta = re.search(r"<textarea[^>]*>(.*?)</textarea>", env, re.S)
rec("S5.11a", "INFO", f"environment page textarea shows stored values to a signed-in member: {'S3cr3t-Value-4711' in (ta.group(1) if ta else '')} (the API never returns values; page does by design: 'Stored encrypted' refers to at-rest)")
# S5.15e: really check the bind list text
txt = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", c.get(f"/apps/{aid}/storage").text))
r = c.submit(f"/apps/{aid}/storage", action=f"/apps/{aid}/storage", kind="bind", source="/", target="/mnt/rootfs")
r2 = c.get(f"/apps/{aid}/storage"); t2 = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", r2.text))
check("S5.15e", "/mnt/rootfs" not in t2, f"bind of / refused (status {r.status}); {flash(r)[-160:]!r}", sev="S1")
# S5.16 each one from a clean state
for i, img in [(8, "NGINX:ALPINE"), (9, "nginx@sha256:short"), (10, "a" * 300), (11, "nginx:ALPINE"), (12, "nginx:" + "x" * 200), (13, "registry.example.com:99999/x/y:z")]:
    settings(image="nginx:alpine")
    r = settings(image=img)
    stored = re.search(r'name="image"[^>]*value="([^"]*)"', c.get(f"/apps/{aid}/settings").text).group(1)
    ok = stored == "nginx:alpine"
    rec(f"S5.16.{i}", "PASS" if ok else "FAIL", f"image {img[:40]!r} {'refused' if ok else 'ACCEPTED'} (status {r.status}); Docker itself rejects uppercase repo names so the deploy would fail later, not a host risk", "" if ok else "S4")
settings(image="nginx:alpine")
