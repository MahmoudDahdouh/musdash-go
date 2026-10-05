from lib_b import *
c = owner_client(); st = state()
D = st["b_db_postgres"] if "b_db_postgres" in st else "qfoenpmqqgvg"
# S8.8 logs, metrics pages
r = c.get(f"/databases/{D}/logs", follow=True); m = c.get(f"/databases/{D}/metrics", follow=True)
sse = re.findall(r'(?:sse-connect|hx-ext|src)="([^"]*log[^"]*)"', r.text)
check("S8.8a", r.status == 200 and m.status == 200, f"logs {r.status}, metrics {m.status}; stream url {sse[:1]}")
if sse:
    u = sse[0].replace("&amp;", "&")
    cl = Client(); cl.cookies = dict(c.cookies); cl.timeout = 8
    try:
        h = cl.conn(); h.request("GET", u, headers={"Cookie": "; ".join(f"{k}={v}" for k, v in c.cookies.items())}); rr = h.getresponse()
        first = rr.read(300); h.close()
        check("S8.8b", rr.status == 200 and "event-stream" in rr.getheader("content-type", "") and b"data:" in first, f"SSE {rr.status} {rr.getheader('content-type')}", evidence=first)
    except Exception as e:
        rec("S8.8b", "FAIL", f"sse read: {e}", "S3")
# S8.10 delete without / with data
def delete(eng, data):
    D = st[f"b_db_{eng}"]; vols = lambda: shout(f"docker volume ls -q | grep -c {D}")
    before = vols()
    f = c.find_form(parse_forms(c.get(f"/databases/{D}/settings", follow=True).text), f"/databases/{D}/delete")
    r = c.post_form(f, confirm=f"t2-{eng}", delete_data=data)
    gone = wait_for(lambda: shout(f"docker ps -a -q --filter name=musdash-db-{D} | wc -l") == "0", 60)
    after = vols()
    return before, after, gone, r
b, a, g, r = delete("keydb", False)
check("S8.10a", g and int(a) >= 1, f"delete without data: container gone={bool(g)}, volumes {b}->{a} (kept)", evidence=flash(r)[:200])
b, a, g, r = delete("dragonfly", True)
check("S8.10b", g and a == "0", f"delete with data: container gone={bool(g)}, volumes {b}->{a} (removed)", evidence=flash(r)[:200])
f = c.find_form(parse_forms(c.get(f"/databases/{st['b_db_clickhouse']}/settings", follow=True).text), "/delete")
r = c.post_form(f, confirm="wrong-name", delete_data=True)
still = shout(f"docker ps -a -q --filter name=musdash-db-{st['b_db_clickhouse']} | wc -l")
check("S8.10c", still == "1", "wrong confirmation name refuses deletion", evidence=flash(r)[:200])
