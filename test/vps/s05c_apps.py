from lib import *
c = owner_client(); st = state(); aid = st["web"]; host = f"t-nginx2.{HOST}.sslip.io"
def settings(**kw):
    base = dict(name="t-nginx2", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
    base.update(kw)
    return c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", **base)
def redeploy():
    d = deploy(c, aid); return dep_wait(c, aid, d)
def cont():
    return shout("docker ps -q --filter name=musdash-" + aid + " | head -1")

# S5.11 environment variables
SECRET = "S3cr3t-Value-" + secret_suffix if False else "S3cr3t-Value-4711"
r = c.submit(f"/apps/{aid}/environment/edit", action=f"/apps/{aid}/environment", vars=f"PLAIN_VAR=hello world\nSECRET_TOKEN={SECRET}\n# a comment\nWITH_EQUALS=a=b=c\nEMPTY=\n")
check("S5.11a", SECRET not in c.get(f"/apps/{aid}/environment").text and SECRET not in r.text, "secret value not shown back on the environment page", sev="S1", evidence="")
res = redeploy(); cid = cont()
env = shout(f"docker exec {cid} printenv | grep -E 'PLAIN_VAR|SECRET_TOKEN|WITH_EQUALS|EMPTY'")
check("S5.11b", res == "success" and "PLAIN_VAR=hello world" in env and f"SECRET_TOKEN={SECRET}" in env and "WITH_EQUALS=a=b=c" in env, f"variables reach the container: {env.replace(chr(10), ' | ')}")
insp = shout(f"docker inspect {cid} --format '{{{{json .Config.Cmd}}}} {{{{json .Args}}}} {{{{json .Config.Labels}}}}'")
cmdline = shout("ps -eo args | grep -c '" + SECRET + "'")
check("S5.11c", SECRET not in insp, "secret not in `docker inspect` Cmd/Args/Labels", sev="S1", evidence=insp[:300])
rc, files = sh(f"grep -rl '{SECRET}' /var/lib/musdash 2>/dev/null; stat -c '%a %n' /var/lib/musdash/apps/*/* 2>/dev/null | head")
envfile = [l for l in files.splitlines() if SECRET in l or l.startswith("/var/lib")]
modes = [l for l in files.splitlines() if re.match(r"^\d{3} ", l)]
check("S5.11d", all(l.startswith("600") or l.startswith("700") for l in modes), f"env files and app dir modes: {modes}; files containing the secret in plaintext: {[l for l in files.splitlines() if l.startswith('/var')]}", sev="S1")
# at rest: sealed in SQLite
rc, out = sh(f"grep -a -c '{SECRET}' /var/lib/musdash/musdash.db /var/lib/musdash/musdash.db-wal")
check("S5.11e", all(x.endswith(":0") for x in out.split()), "secret sealed in SQLite: " + out.replace(chr(10), " "), sev="S1")

# S5.12 storage: file mount and volume
c.submit(f"/apps/{aid}/storage", action=f"/apps/{aid}/storage", kind="file", source="", target="/usr/share/nginx/html/hello.txt", content="hello-from-musdash\n")
c.submit(f"/apps/{aid}/storage", action=f"/apps/{aid}/storage", kind="volume", source="t-nginx2-data", target="/data")
res = redeploy(); time.sleep(2)
out = vcurl("http://127.0.0.1/hello.txt", host)
check("S5.12a", res == "success" and "hello-from-musdash" in out, f"file written in the UI is served from the container: {out[:60]!r}")
cid = cont(); shout(f"docker exec {cid} sh -c 'echo persisted > /data/keep.txt'")
res = redeploy(); cid = cont()
check("S5.12b", "persisted" in shout(f"docker exec {cid} cat /data/keep.txt"), "volume contents survive a redeploy")

# S5.13 limits
settings(memory_mb="96", cpus="0.5"); res = redeploy(); cid = cont()
lim = shout(f"docker inspect {cid} --format '{{{{.HostConfig.Memory}}}} {{{{.HostConfig.NanoCpus}}}}'")
check("S5.13", res == "success" and lim.split()[0] == str(96 * 1024 * 1024) and lim.split()[1] == "500000000", f"memory/CPU limits applied: {lim}")
settings(); 

# S5.14 docker options allow-list
bad_opts = ["--privileged", "-v /:/host", "--volume=/etc:/x", "--pid=host", "--cap-add=SYS_ADMIN", "--device /dev/sda", "--network host", "--net=host", "--security-opt seccomp=unconfined",
            "--userns host", "--ipc=host", "--add-host=a:b --privileged", "--mount type=bind,src=/,dst=/h", "--shm-size 1g; id", "--user 0", "--entrypoint /bin/sh", "-e FOO=bar", "--env-file /etc/passwd"]
res = {}
for o in bad_opts:
    r = settings(docker_options=o)
    saved = re.search(r'name="docker_options"[^>]*value="([^"]*)"', c.get(f"/apps/{aid}/settings").text)
    res[o] = bool(saved and saved.group(1))   # True = it was accepted and stored
    settings(docker_options="")
accepted = [k for k, v in res.items() if v]
check("S5.14a", not [k for k in accepted if k in ("--privileged", "-v /:/host", "--volume=/etc:/x", "--pid=host", "--cap-add=SYS_ADMIN", "--device /dev/sda", "--network host", "--net=host", "--security-opt seccomp=unconfined", "--userns host", "--ipc=host", "--add-host=a:b --privileged", "--mount type=bind,src=/,dst=/h", "--shm-size 1g; id")],
      f"dangerous options refused. Accepted: {accepted}", sev="S1", evidence=res)
rec("S5.14b", "INFO", f"options accepted by the allow-list (review whether they are fine): {accepted}")
r = settings(docker_options="--shm-size 128m --ulimit nofile=4096:4096"); res = redeploy(); cid = cont()
shm = shout(f"docker inspect {cid} --format '{{{{.HostConfig.ShmSize}}}}'")
check("S5.14c", res == "success" and shm == str(128 * 1024 * 1024), f"allowed options applied (shm {shm})")
settings()

# S5.15 bind guard
for src in ["/var/run/docker.sock", "/etc", "/var/lib/musdash", "/var/lib/musdash/master.key", "/", "/proc", "/var/lib/docker", "/etc/../var/run/docker.sock"]:
    r = c.submit(f"/apps/{aid}/storage", action=f"/apps/{aid}/storage", kind="bind", source=src, target="/mnt/x")
    stored = f'value="{src}"' in c.get(f"/apps/{aid}/storage").text or (src in re.sub(r"<[^>]+>", " ", c.get(f"/apps/{aid}/storage").text))
    check("S5.15" + chr(97 + ["/var/run/docker.sock", "/etc", "/var/lib/musdash", "/var/lib/musdash/master.key", "/", "/proc", "/var/lib/docker", "/etc/../var/run/docker.sock"].index(src)), not stored, f"bind source {src} refused", sev="S1")

# S5.16 image reference injection
for i, img in enumerate(["nginx --privileged", "nginx;id", "-v /:/h nginx", "nginx\n--privileged", "$(id)", "nginx`id`", "nginx:alpine --rm", "../../x", "NGINX:ALPINE", "nginx@sha256:short", "a" * 300]):
    r = settings(image=img)
    page = c.get(f"/apps/{aid}/settings").text
    stored = re.search(r'name="image"[^>]*value="([^"]*)"', page).group(1)
    check(f"S5.16.{i}", stored == "nginx:alpine", f"image {img[:30]!r:34} refused (stored: {stored!r}, status {r.status})", sev="S1")
settings()
