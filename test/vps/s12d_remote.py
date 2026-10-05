from lib import *
c = owner_client(); st = state()
print(sh("docker exec t-remote sh -c 'docker version --format {{.Server.Version}}; pgrep -a sshd | head -2'")[1])
r = c.submit("/servers", action="/servers", name="t-remote", host=HOST, port="2222", ssh_user="root", key="new", data_dir="")
t = re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", r.text)); print(r.status, t[150:700])
pub = re.findall(r"ssh-ed25519 AAAA[A-Za-z0-9+/=]+(?: [^\s<]+)?", r.text); print(pub[:2])
sid = re.search(r"/servers/([a-z2-7]{12})", r.url or "") or re.search(r"/servers/([a-z2-7]{12})/check", r.text)
print(r.url, sid.group(1) if sid else None)
