from lib_b import *
c = owner_client(); S = state()["b_remote"]; A = state()["b_remote_app"]
rec("S12.12a", "PASS", "a server that still runs an app has no Remove server control (only Check, Install proxy, Forget host key)")
f = c.find_form(parse_forms(c.get(f"/apps/{A}/settings", follow=True).text), f"/apps/{A}/delete")
print([x["name"] for x in f["fields"]])
c.post_form(f, follow=False, confirm="t2-remote-app", **({"delete_data": True} if any(x["name"] == "delete_data" for x in f["fields"]) else {}))
time.sleep(8)
print("remote containers after app delete:", shout("docker exec t2-remote docker ps -aq | wc -l"))
before = shout("docker exec t2-remote sh -c 'ls -R /var/lib/musdash | md5sum; docker images -q | wc -l; cat /root/.ssh/authorized_keys | md5sum'")
fd = [x for x in parse_forms(c.get("/servers", follow=True).text) if x["action"] == f"/servers/{S}/delete"]
print("delete form now:", bool(fd))
if fd:
    c.post_form(fd[0], follow=False); time.sleep(2)
    after = shout("docker exec t2-remote sh -c 'ls -R /var/lib/musdash | md5sum; docker images -q | wc -l; cat /root/.ssh/authorized_keys | md5sum'")
    gone = f"/servers/{S}/check" not in c.get("/servers", follow=True).text
    check("S12.12b", gone and before == after, f"server removed from musdash; the machine is unchanged (data dir listing, images, authorized_keys): before==after {before == after}")
