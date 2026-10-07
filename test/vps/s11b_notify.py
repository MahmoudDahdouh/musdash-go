from lib import *
c = owner_client(); st = state(); pid = st["proj"]; env = st["env"]
r = c.submit(f"/projects/{pid}/apps/new?env={env}", action=f"/projects/{pid}/apps", name="t-sink", image="mendhak/http-https-echo:latest", port="8080", domain=f"t-sink.{HOST}.sslip.io", deploy=True)
sink = re.search(r"/apps/([a-z2-7]{12})", r.url).group(1); dep = re.search(r"/deployments/([a-z2-7]{12})", r.url).group(1)
save_state(sink=sink)
check("S11.0", dep_wait(c, sink, dep, 400) == "success", "webhook sink app deployed (mendhak/http-https-echo)")
time.sleep(3)
SINK = f"http://t-sink.{HOST}.sslip.io/hook"
def sinklog(): return shout(f"docker logs --since 5m $(docker ps -q --filter name=musdash-{sink}) 2>&1 | tail -40")
# channel form
pg = c.get("/settings/notifications"); print([ (f["action"], [(x["name"], x["type"], x.get("options", "")) for x in f["fields"] if x["name"] != "_csrf"]) for f in parse_forms(pg.text) if "logout" not in f["action"]])
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", pg.text)); print(t[150:1600])
print(re.findall(r'href="([^"]*kind=[^"]*)"', pg.text))
