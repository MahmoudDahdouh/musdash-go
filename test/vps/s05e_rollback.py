from lib import *
import threading
c = owner_client(); st = state(); aid = st["web"]; host = f"t-web.{HOST}.sslip.io"
def settings(**kw):
    base = dict(name="t-web", image="nginx:alpine", port="80", memory_mb="", cpus="", health_path="/", health_cmd="", health_timeout="30", start_command="", docker_options="")
    base.update(kw); return c.submit(f"/apps/{aid}/settings", action=f"/apps/{aid}/settings", **base)
# remove the file mount so the page content comes from the image: use two distinct images as 'versions'
for f in parse_forms(c.get(f"/apps/{aid}/storage").text):
    if f["action"].startswith(f"/apps/{aid}/storage/") and f["action"].endswith("/delete"): c.post_form(f)

# S5.18 two deploys queued back-to-back: serialised, none lost
d1 = deploy(c, aid); d2 = deploy(c, aid)
r1 = dep_wait(c, aid, d1); r2 = dep_wait(c, aid, d2)
cs = shout("docker ps --format '{{.Names}}' | grep musdash-" + aid)
check("S5.18", r1 and r2 and len(cs.splitlines()) == 1, f"two queued deploys: {r1}/{r2} (ids {d1},{d2}); exactly one app container left: {cs.splitlines()}", sev="S2", evidence=cs)
rec("S5.18b", "INFO", "if both ids were identical the second POST was coalesced into the waiting one: ids " + str((d1, d2)))

# versions: v1 = nginx:1.27-alpine, v2 = nginx:alpine (different image content)
settings(image="nginx:1.27-alpine"); dA = deploy(c, aid); rA = dep_wait(c, aid, dA)
verA = vcmd = shout(f"docker exec $(docker ps -q --filter name=musdash-{aid}) nginx -v 2>&1")
settings(image="nginx:alpine"); dB = deploy(c, aid); rB = dep_wait(c, aid, dB)
verB = shout(f"docker exec $(docker ps -q --filter name=musdash-{aid}) nginx -v 2>&1")
check("S5.19a", rA == "success" and rB == "success" and verA != verB, f"two deployments with different images: {verA} -> {verB}")
imgs = shout("docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^musdash/' ")
rec("S5.19b", "INFO", "kept images named musdash/: " + imgs.replace("\n", ", "))
# S5.20 rollback
page = c.get(f"/apps/{aid}/deployments/{dA}")
rb = [f for f in parse_forms(page.text) if f["action"].endswith("/rollback")]
check("S5.20a", len(rb) == 1, f"deployment page has a Roll back form: {[f['action'] for f in rb]}")
settings(image="nginx:alpine", port="80")
pulls_before = shout("journalctl -u musdash-server --since '-2 min' --no-pager | grep -ci pull")
r = c.post_form(rb[0], follow=False)
m = re.search(r"/deployments/([a-z2-7]{12})", r.location or ""); dR = m.group(1) if m else None
res = dep_wait(c, aid, dR) if dR else None
verR = shout(f"docker exec $(docker ps -q --filter name=musdash-{aid}) nginx -v 2>&1")
log = re.sub(r"<[^>]+>", " ", c.get(f"/apps/{aid}/deployments/{dR}").text) if dR else ""
check("S5.20b", res == "success" and verR == verA, f"rollback re-ran the earlier image: {verR} (wanted {verA}); status {res}", sev="S2")
check("S5.20c", "Pulling" not in log and "pull" not in log.lower().replace("--pull never", "") , "rollback pulled/built nothing: " + re.sub(r"\s+", " ", log)[200:600], sev="S3")
# settings unchanged
check("S5.20d", re.search(r'name="image"[^>]*value="([^"]*)"', c.get(f"/apps/{aid}/settings").text).group(1) == "nginx:alpine", "settings (image field) unchanged by a rollback")

# S5.21 tamper: roll back using another app's deployment id / random id
other = deploy.__name__ and None
r = c.post(f"/apps/{aid}/deployments/zzzzzzzzzzzz/rollback", dict(_csrf=csrf_of(c, f"/apps/{aid}")))
check("S5.21a", r.status in (404, 422, 403), f"rollback with an unknown deployment id -> {r.status}", sev="S1")
