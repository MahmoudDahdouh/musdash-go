from lib import *
import sys
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]; R = Client(token=ensure_tokens(c)["read_token"])
exec(open("s10a_services.py").read().split("REFUSE = {")[0].split("def new_service")[0])
def svc_page(sid): return c.get(f"/services/{sid}").text
def create(tpl, name, **vars):
    r, forms = c.forms(f"/projects/{pid}/services/new?env={env}&template={tpl}")
    f = c.find_form(forms, f"/projects/{pid}/services")
    r = c.post_form(f, name=name, connect_env=False, deploy=True, **vars)
    m = re.search(r"/services/([a-z2-7]{12})", r.url or "") or re.search(r"/services/([a-z2-7]{12})", r.text); return m.group(1) if m else None, r
def state_of(sid): return (R.get(f"/api/v1/services/{sid}").json() or {}).get("status")
def settle(sid, t=900):
    return wait_for(lambda: state_of(sid) if state_of(sid) in ("running", "failed", "stopped", "exited") else None, t, 6)
def drop(sid, data=True):
    pg = c.get(f"/services/{sid}/settings").text; f = [x for x in parse_forms(pg) if x["action"] == f"/services/{sid}/delete"][0]
    nm = re.search(r"Type ([a-z0-9\-]+) to confirm", re.sub("<[^>]+>", " ", pg)); c.post_form(f, confirm=nm.group(1), delete_data=data)
TPL = sys.argv[1:] or ["uptime-kuma", "minio", "cloudflared", "wordpress", "ghost", "n8n"]
for tpl in TPL:
    name = "t-" + tpl; t0 = time.time()
    vars_ = {"var_TUNNEL_TOKEN": "eyJhIjoiZmFrZSIsInQiOiJmYWtlIiwicyI6ImZha2UifQ=="} if tpl == "cloudflared" else {}
    sid, r = create(tpl, name, **vars_)
    if not sid: rec(f"S10.1.{tpl}", "FAIL", f"{tpl}: not created: {flash(r)[-150:]}", "S2"); continue
    s = settle(sid); page = svc_page(sid); txt = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", page))
    hosts = re.findall(r"([a-z0-9\-]+\.168\.235\.65\.204\.sslip\.io)", txt)
    if tpl == "cloudflared":
        rec("S10.2", "PASS" if s in ("running", "failed", "exited") else "FAIL", f"cloudflared with a fake token: status {s} (the template starts the container; the real tunnel needs a Cloudflare token). Containers: {shout('docker ps -a --filter name=musdash-' + sid + ' --format {{.Names}}:{{.Status}}')}", "")
        drop(sid); continue
    codes = {}
    for h in dict.fromkeys(hosts):
        try: codes[h] = Client(f"http://{h}", timeout=30).get("/", follow=False).status
        except Exception as e: codes[h] = repr(e)[:40]
    ok = s == "running" and codes and all(isinstance(v, int) and v < 500 for v in codes.values())
    check(f"S10.1.{tpl}", ok, f"{tpl}: {s} after {time.time()-t0:.0f}s; endpoints {codes}", sev="S2")
    gen = re.findall(r"(SERVICE_[A-Z0-9_]+)", txt); vals1 = re.findall(r'data-copy="([^"]+)"', page)
    # S10.6 stable generated values across redeploy
    c.post(f"/services/{sid}/deploy", dict(_csrf=csrf_of(c, f"/services/{sid}"))); time.sleep(5); settle(sid, 600)
    vals2 = re.findall(r'data-copy="([^"]+)"', svc_page(sid))
    check(f"S10.6.{tpl}", gen and [v for v in vals1 if len(v) > 8 and "." not in v] == [v for v in vals2 if len(v) > 8 and "." not in v], f"{tpl}: generated values {sorted(set(gen))} are created once and unchanged after a redeploy", sev="S2")
    # S10.12 endpoint domain edit
    ef = [f for f in parse_forms(c.get(f"/services/{sid}/settings").text) if f["action"].startswith(f"/services/{sid}/endpoints/")]
    if ef:
        newh = f"t-{tpl}-x.{HOST}.sslip.io"; c.post_form(ef[0], host=newh, tls=False); time.sleep(4)
        code = Client(f"http://{newh}", timeout=30).get("/", follow=False).status
        check(f"S10.12.{tpl}", code < 500 and code != 404, f"{tpl}: changed an endpoint's domain; new name answers {code}", sev="S3")
    # stop / deploy
    c.post(f"/services/{sid}/stop", dict(_csrf=csrf_of(c, f"/services/{sid}"))); time.sleep(10)
    left = shout(f"docker ps -q --filter name=musdash-{sid} | wc -l")
    stopped = state_of(sid)
    if tpl == "minio":
        c.post(f"/services/{sid}/deploy", dict(_csrf=csrf_of(c, f"/services/{sid}"))); settle(sid, 300)
        check(f"S10.10.{tpl}", left.strip() == "0" and stopped in ("stopped", "exited") and state_of(sid) == "running", f"{tpl}: stop removes containers ({left.strip()} left, status {stopped}); deploy brings it back ({state_of(sid)})", sev="S2")
        save_state(minio=sid, minio_hosts=hosts)
        continue
    drop(sid)
    time.sleep(8)
    vol = shout(f"docker volume ls -q | grep -c {sid} || true"); ctr = shout(f"docker ps -aq --filter name=musdash-{sid} | wc -l")
    check(f"S10.10.{tpl}", left.strip() == "0" and stopped in ("stopped", "exited") and ctr.strip() == "0" and vol.strip() in ("0", ""), f"{tpl}: stop removed containers ({left.strip()}); delete with data left {ctr.strip()} containers, {vol.strip()} volumes", sev="S2")
    sh("docker image prune -af >/dev/null 2>&1; docker builder prune -f >/dev/null 2>&1")
