from lib import *
c = owner_client(); st = state(); sid = "ttd6yamy3cmb"
# S12.1 local server card vs the real machine
pg = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", c.get("/servers").text))
real = shout("docker --version; docker compose version --short; git --version")
check("S12.1a", "29.8.1" in pg and "Running" in pg, f"server card shows Docker running and its version: {pg[pg.find('localhost'):pg.find('localhost')+120]!r}")
ip = re.search(r'name="ip"[^>]*value="([^"]*)"', c.get("/servers").text); check("S12.1b", ip and ip.group(1) == HOST, f"public IP detected as {ip.group(1) if ip else None}")
# S12.2 server metrics
m = c.get(f"/servers/{sid}/metrics"); now = c.get(f"/servers/{sid}/metrics/now")
body = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", now.text))
real_mem = shout("free -m | awk 'NR==2{print $2, $3}'"); real_disk = shout("df -h / | awk 'NR==2{print $2, $3, $5}'")
rec("S12.2a", "PASS" if now.status == 200 and ("Memory" in body or "memory" in body) else "FAIL", f"metrics/now {now.status}: {body[:360]} | real: mem(total used MB)={real_mem}, disk={real_disk}", "" if now.status == 200 else "S2")
conts = shout("docker ps --format '{{.Names}}' | wc -l"); shown = len(re.findall(r"musdash-[a-z0-9]+", now.text))
rec("S12.2b", "INFO", f"containers on the server: {conts}; shown on the page: {shown} (names with musdash- prefix)")
# S12.3 sampling
pg = c.get(f"/servers/{sid}/metrics").text; f = [x for x in parse_forms(pg) if x["action"] == f"/servers/{sid}/metrics"][0]
r = c.post_form(f, sample="1") if True else None
time.sleep(130)
pg = c.get(f"/servers/{sid}/metrics?range=1h" if False else f"/servers/{sid}/metrics").text
svgs = len(re.findall(r"<svg", pg)); samples_page = re.findall(r"(?i)sampl[a-z]+[^<]{0,80}", re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", pg)))[:2]
check("S12.3a", svgs >= 2, f"after 2+ minutes of sampling the page draws charts ({svgs} svg elements); text: {samples_page}", sev="S3")
for rng in ("1", "6", "24"):
    rr = c.get(f"/servers/{sid}/metrics?hours={rng}"); rec("S12.3b" + rng, "INFO", f"range {rng}h -> {rr.status}, svgs {len(re.findall(r'<svg', rr.text))}")
# off removes the samples
f = [x for x in parse_forms(c.get(f"/servers/{sid}/metrics").text) if x["action"] == f"/servers/{sid}/metrics"][0]
c.post_form(f, sample="0"); time.sleep(3)
pg2 = c.get(f"/servers/{sid}/metrics").text
check("S12.3c", len(re.findall(r"<svg", pg2)) < svgs or "off" in pg2.lower(), f"switching sampling off removes history (svgs {svgs} -> {len(re.findall(r'<svg', pg2))})", sev="S3")
# S12.4 app metrics tab
web = st["web"]; t = c.get(f"/apps/{web}/metrics"); n = c.get(f"/apps/{web}/metrics/now")
body = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", n.text))
check("S12.4a", n.status == 200 and re.search(r"\d", body), f"app metrics/now {n.status}: {body[:200]}", sev="S3")
dc = shout(f"docker stats --no-stream --format '{{{{.MemUsage}}}} {{{{.CPUPerc}}}}' $(docker ps -q --filter name=musdash-{web})")
rec("S12.4b", "INFO", f"docker stats says: {dc}")
# polling caps: the fragment stops polling after maxPolls
polls = re.findall(r'hx-trigger="([^"]+)"', t.text); rec("S12.4c", "INFO", f"polling trigger on metrics page: {polls}")
