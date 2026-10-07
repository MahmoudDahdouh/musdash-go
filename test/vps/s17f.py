from lib_b import *
c = owner_client(); A = state()["b_app"]; H = f"t2-app.{HOST}.sslip.io"
f = [x for x in parse_forms(c.get(f"/apps/{A}/domains", follow=True).text) if x["action"] == f"/apps/{A}/domains"][0]
c.post_form(f, follow=False, host=H, tls=False); time.sleep(4)
get = lambda: (lambda r: r.status)(Client(f"http://{HOST}", timeout=5).get("/", headers={"Host": H}))
print("before:", get())
srv0 = shout("systemctl show -p MainPID --value musdash-server"); px0 = shout("systemctl show -p MainPID --value musdash-proxy")
shout(f"kill -9 {px0}")
t0 = time.time(); res = []
while time.time() - t0 < 12:
    try: res.append((round(time.time() - t0, 1), get()))
    except Exception as e: res.append((round(time.time() - t0, 1), "err"))
    time.sleep(0.5)
back = [t for t, s in res if s == 200]
px1 = shout("systemctl show -p MainPID --value musdash-proxy"); srv1 = shout("systemctl show -p MainPID --value musdash-server")
print(res[:6], "...", res[-2:])
check("S17.3", back and px1 != px0 and srv1 == srv0 and Client().get("/healthz").status == 200, f"kill -9 proxy: systemd restarted it (pid {px0}->{px1}), app answers again after {back[0] if back else None}s, routes restored from routes.json, server untouched (pid {srv0}=={srv1})")
