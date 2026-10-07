"""Harness for the VPS test plan (docs/testing/vps-full-test-plan.md). stdlib only.

Environment:
  MSD_BASE      dashboard address        (default http://168.235.65.204:8000)
  MSD_HOST      public IP of the VPS      (default 168.235.65.204)
  MSD_SSH       command that runs its args on the VPS (a script that wraps ssh)
"""
import http.client, json, os, re, subprocess, sys, time, urllib.parse
from html.parser import HTMLParser

BASE = os.environ.get("MSD_BASE", "http://168.235.65.204:8000")
HOST = os.environ.get("MSD_HOST", "168.235.65.204")
SSH = os.environ.get("MSD_SSH", "")
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "out")
os.makedirs(OUT, exist_ok=True)
RESULTS = os.path.join(OUT, "results.jsonl")
STATE = os.path.join(OUT, "state.json")


def state():
    try:
        return json.load(open(STATE))
    except Exception:
        return {}


def save_state(**kw):
    s = state(); s.update(kw)
    json.dump(s, open(STATE, "w"), indent=1)
    return s


# ---------- results ----------
_seen = set()


def rec(case, status, note="", sev="", evidence=""):
    """status: PASS FAIL BLOCKED NA. A later record for the same case wins."""
    assert status in ("PASS", "FAIL", "BLOCKED", "NA", "INFO"), status
    row = dict(case=case, status=status, note=note, sev=sev, evidence=str(evidence)[:1500], t=int(time.time()))
    with open(RESULTS, "a") as f:
        f.write(json.dumps(row) + "\n")
    mark = {"PASS": "ok  ", "FAIL": "FAIL", "BLOCKED": "blk ", "NA": "n/a ", "INFO": "info"}[status]
    print(f"[{mark}] {case} {note}" + (f"  [{sev}]" if sev else ""), flush=True)


def check(case, cond, note="", sev="S2", evidence=""):
    rec(case, "PASS" if cond else "FAIL", note, "" if cond else sev, evidence if not cond else "")
    return bool(cond)


# ---------- ssh ----------
def sh(cmd, timeout=300, check_rc=False):
    """Run a shell command on the VPS; returns (rc, stdout+stderr)."""
    assert SSH, "set MSD_SSH"
    p = subprocess.run([SSH, cmd], capture_output=True, text=True, errors="replace", timeout=timeout)
    out = p.stdout + p.stderr
    if check_rc and p.returncode:
        raise RuntimeError(f"ssh failed rc={p.returncode}: {cmd}\n{out}")
    return p.returncode, out


def shout(cmd, **kw):
    return sh(cmd, **kw)[1].strip()


# ---------- addresses ----------
# Since 2026-10-07 the address of everything in an environment says where it
# is: /projects/{p}/env/{e}/app/{id}/… where it was /apps/{id}/…, and
# /projects/{p}/env/{e}/… where it was /projects/{p}/e/{e}/… or
# /environments/{e}/…. The scripts were written against the short addresses
# and still name things that way. The client translates in both directions:
# what a script asks for is sent to the long address, and what comes back
# (a page, a Location) is given to the script with the short ones, so a
# script that reads an id out of "/apps/<id>" or follows a form's action
# works as it did. Every request on the wire is to a long address.
_ID = r"[a-z2-7]{12}"
_KINDS = {"app": "apps", "database": "databases", "service": "services"}
_PLURAL = {v: k for k, v in _KINDS.items()}
_place = {}     # (kind, id) -> "/projects/p/env/e"
_env_of = {}    # environment id -> project id
_first = {}     # project id -> "/projects/p/env/e", its first environment

_LONG_RES = re.compile(r"/projects/(%s)/env/(%s)/(app|database|service)/(%s)(?![a-z2-7])" % (_ID, _ID, _ID))
_LONG_ENV_OWN = re.compile(r"/projects/(%s)/env/(%s)/(variables|delete|switch/resources)(?![a-z])" % (_ID, _ID))
_LONG_ENV_KIND = re.compile(r"/projects/(%s)/env/(%s)/(app|database|service)(?![a-z])" % (_ID, _ID))
_LONG_ENV = re.compile(r"/projects/(%s)/env/(%s)(?![a-z2-7])" % (_ID, _ID))


def short_text(s):
    """A page or a Location as the scripts expect it: with the short addresses."""
    if "/env/" not in s:
        return s

    def res(m):
        p, e, kind, rid = m.groups()
        _place[(kind, rid)] = f"/projects/{p}/env/{e}"; _env_of[e] = p
        return f"/{_KINDS[kind]}/{rid}"

    def own(m):
        p, e, what = m.groups(); _env_of[e] = p
        return f"/environments/{e}/{what}"

    def kind(m):
        p, e, k = m.groups(); _env_of[e] = p
        return f"/projects/{p}/e/{e}/{_KINDS[k]}"

    def env(m):
        p, e = m.groups(); _env_of[e] = p
        return f"/projects/{p}/e/{e}"

    s = _LONG_RES.sub(res, s)
    s = _LONG_ENV_OWN.sub(own, s)
    s = _LONG_ENV_KIND.sub(kind, s)
    return _LONG_ENV.sub(env, s)


def long_path(path, ask=None):
    """The long address of a short one. ask(path) is a GET that does not
    follow, used once for a resource or environment not seen yet: the short
    address of a resource still answers with where the long one is."""
    if not path or not path.startswith("/"):
        return path
    m = re.match(r"^/(apps|databases|services)/(%s)(?=$|[/?#])" % _ID, path)
    if m:
        key = (_PLURAL[m.group(1)], m.group(2))
        if key not in _place and ask:
            ask(f"/{m.group(1)}/{m.group(2)}")  # its Location is read by short_text, which remembers the place
        if key in _place:
            return _place[key] + "/" + key[0] + "/" + key[1] + path[m.end():]
        return path
    m = re.match(r"^/projects/(%s)/e/(%s)(.*)$" % (_ID, _ID), path)
    if m:
        p, e, rest = m.groups()
        rest = re.sub(r"^/(apps|databases|services)(?=$|[/?#])", lambda k: "/" + _PLURAL[k.group(1)], rest)
        return f"/projects/{p}/env/{e}{rest}"
    m = re.match(r"^/environments/(%s)/(.*)$" % _ID, path)
    if m:
        e, rest = m.groups()
        if e not in _env_of and ask:
            ask(f"/environments/{e}/variables")
        if e in _env_of:
            return f"/projects/{_env_of[e]}/env/{e}/{rest}"
    return path


class Resp:
    def __init__(self, status, headers, body, url=""):
        self.status, self.headers, self.body, self.url = status, headers, body, url

    @property
    def text(self):
        return self.body.decode("utf-8", "replace")

    def h(self, name, default=""):
        return self.headers.get(name.lower(), default)

    @property
    def location(self):
        return self.h("location")

    def json(self):
        return json.loads(self.body)


class FormParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.forms = []; self.cur = None; self.sel = None; self.ta = None; self.btn = None

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "form":
            self.cur = dict(action=a.get("action", ""), method=(a.get("method") or "get").lower(), fields=[], attrs=a)
            self.forms.append(self.cur)
        elif self.cur is None:
            return
        elif tag == "input":
            name = a.get("name")
            if not name:
                return
            t = (a.get("type") or "text").lower()
            if t in ("checkbox", "radio"):
                self.cur["fields"].append(dict(name=name, value=a.get("value", "on"), type=t, checked="checked" in a))
            elif t == "submit":
                self.cur["fields"].append(dict(name=name, value=a.get("value", ""), type="submit"))
            else:
                self.cur["fields"].append(dict(name=name, value=a.get("value", ""), type=t))
        elif tag == "select":
            self.sel = dict(name=a.get("name"), value=None, type="select", options=[])
            self.cur["fields"].append(self.sel)
        elif tag == "option" and self.sel is not None:
            v = a.get("value", "")
            self.sel["options"].append(v)
            if "selected" in a or self.sel["value"] is None:
                if "selected" in a or self.sel["value"] is None:
                    self.sel["value"] = v
        elif tag == "textarea":
            self.ta = dict(name=a.get("name"), value="", type="textarea")
            self.cur["fields"].append(self.ta)
        elif tag == "button" and a.get("name"):
            self.cur["fields"].append(dict(name=a["name"], value=a.get("value", ""), type="submit"))

    def handle_data(self, data):
        if self.ta is not None:
            self.ta["value"] += data

    def handle_endtag(self, tag):
        if tag == "form":
            self.cur = None
        elif tag == "select":
            self.sel = None
        elif tag == "textarea":
            self.ta = None


def parse_forms(html):
    p = FormParser(); p.feed(html); return p.forms


def _compat(page, action):
    """The 2026-10 UI redesign moved pages the first run's scripts used; map the old addresses to the new ones."""
    m = re.match(r"^/projects/([a-z2-7]+)/(apps|databases|services)/new\?env=([a-z2-7]+)(.*)$", page)
    if m:
        pid, kind, env, rest = m.groups()
        q = rest.lstrip("&")
        page = f"/projects/{pid}/e/{env}/{kind}/new" + (("?" + q) if q else "")
        if action == f"/projects/{pid}/{kind}":
            action = f"/projects/{pid}/e/{env}/{kind}"
    if action and re.match(r"^/apps/[a-z2-7]+/environment$", action) and page == action:
        page = action + "/edit"
    if action and re.match(r"^/(team|projects/[a-z2-7]+|environments/[a-z2-7]+|servers/[a-z2-7]+)/variables$", action) and page == action:
        page = action + "/edit"
    if action and re.match(r"^/apps/[a-z2-7]+/domains$", action) and page == action[:-len("domains")] + "settings":
        page = action  # an app's domains moved from its Settings to a tab of their own
    m = re.match(r"^/projects/([a-z2-7]+)/settings$", page or "")
    if m and action == f"/projects/{m.group(1)}/environments":
        # Add environment is on the project's own page, the Environments
        # tab. The query keeps the client from serving the first
        # environment's page in its place.
        page = f"/projects/{m.group(1)}?tab=environments"
    m = re.match(r"^/environments/([a-z2-7]+)/delete$", action or "")
    if m and re.match(r"^/projects/[a-z2-7]+/settings$", page or ""):
        page = f"/environments/{m.group(1)}/settings"  # an environment is deleted from its own Settings
    if action == "/projects" and page == "/projects/new":
        page = "/projects"
    if action == "/account/tokens" and page in ("/account", "/keys"):
        page = "/keys/tokens"  # the API Tokens tab of the Keys page
    return page, action


def _compat_fields(action, over):
    """Scripts written for the one-ability token form: "read" is the Read box,
    "deploy" what that ability could do (read, write, deploy)."""
    if action == "/account/tokens" and "token_ability" in over:
        deploy = over.pop("token_ability") == "deploy"
        over.update(perm_read=True, perm_write=deploy, perm_deploy=deploy)
    return over


class Client:
    def __init__(self, base=BASE, token=None, timeout=60):
        u = urllib.parse.urlparse(base)
        self.scheme, self.host, self.port = u.scheme, u.hostname, u.port or (443 if u.scheme == "https" else 80)
        self.cookies = {}; self.token = token; self.timeout = timeout; self.extra = {}

    def conn(self):
        if self.scheme == "https":
            import ssl
            return http.client.HTTPSConnection(self.host, self.port, timeout=self.timeout, context=ssl._create_unverified_context())
        return http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)

    def _ask(self, path):
        self.request("GET", path, translate=False)

    def request(self, method, path, data=None, headers=None, follow=False, raw=None, host=None, ctype=None, translate=True):
        asked = path
        if translate and not self.token:  # the API names a resource by its id alone
            path = long_path(path, self._ask)
            m = re.match(r"^/projects/(%s)$" % _ID, path)
            if method == "GET" and m:
                # What the scripts call a project's page is its first
                # environment's: what runs in it. The project's own page
                # lists the environments in the order they were made.
                if m.group(1) not in _first:
                    r0 = self.request("GET", path, translate=False)
                    e = re.search(r"/projects/%s/e/(%s)" % (m.group(1), _ID), r0.text)
                    if r0.status == 200 and e:
                        _first[m.group(1)] = "/projects/%s/env/%s" % (m.group(1), e.group(1))
                path = _first.get(m.group(1), path)
        h = {"User-Agent": "musdash-vps-test"}
        if self.cookies:
            h["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        if self.token:
            h["Authorization"] = "Bearer " + self.token
        if host:
            h["Host"] = host
        h.update(self.extra); h.update(headers or {})
        body = raw
        if data is not None:
            body = urllib.parse.urlencode(data, doseq=True).encode()
            h["Content-Type"] = "application/x-www-form-urlencoded"
        if ctype:
            h["Content-Type"] = ctype
        c = self.conn()
        try:
            c.request(method, path, body=body, headers=h)
            r = c.getresponse(); b = r.read()
        finally:
            c.close()
        hd = {}
        for k, v in r.getheaders():
            hd[k.lower()] = (hd[k.lower()] + ", " + v) if k.lower() in hd else v
            if k.lower() == "set-cookie":
                ck = v.split(";")[0]; n, _, val = ck.partition("=")
                if "max-age=0" in v.lower() or val == "":
                    self.cookies.pop(n, None)
                else:
                    self.cookies[n] = val
        # The script sees the short addresses, in the page and in where an
        # answer leads; reading them is also how places are learned.
        if "location" in hd:
            hd["location"] = short_text(hd["location"])
        if hd.get("content-type", "").startswith("text/html"):
            b = short_text(b.decode("utf-8", "replace")).encode()
        resp = Resp(r.status, hd, b, asked)
        if follow and r.status in (301, 302, 303, 307, 308) and resp.location:
            loc = resp.location.split("#")[0]
            if loc.startswith("http"):
                loc = urllib.parse.urlparse(loc).path + ("?" + urllib.parse.urlparse(loc).query if urllib.parse.urlparse(loc).query else "")
            return self.request("GET", loc, follow=True)
        return resp

    def get(self, path, **kw):
        return self.request("GET", path, **kw)

    def post(self, path, data=None, **kw):
        return self.request("POST", path, data=data or {}, **kw)

    # --- forms: fill from the page itself ---
    def forms(self, path, **kw):
        r = self.get(path, follow=True, **kw)
        return r, parse_forms(r.text)

    def find_form(self, forms, action=None, has=None):
        if action is not None:  # an exact action wins over a substring match (/account/tokens vs /account/tokens/{id}/delete)
            for f in forms:
                names = {x["name"] for x in f["fields"]}
                if f["action"] == action and (not has or all(n in names for n in (has if isinstance(has, (list, tuple)) else [has]))):
                    return f
        for f in forms:
            if action is not None and action not in f["action"]:
                continue
            names = {x["name"] for x in f["fields"]}
            if has and not all(n in names for n in (has if isinstance(has, (list, tuple)) else [has])):
                continue
            return f
        return None

    def submit(self, page, action=None, has=None, follow=True, submit=None, **over):
        """GET page, find a form, post its fields with overrides. Returns Resp.
        Overrides: name=value. Checkbox: True/False. Use name__ for names with dots/dashes via dict in over['_o']."""
        page, action = _compat(page, action)
        over = _compat_fields(action, over)
        r, forms = self.forms(page)
        f = self.find_form(forms, action, has)
        if f is None:
            raise LookupError(f"no form action~{action!r} has={has} on {page} (status {r.status}); forms={[x['action'] for x in forms]}")
        return self.post_form(f, follow=follow, submit=submit, **over)

    def post_form(self, f, follow=True, submit=None, **over):
        o = dict(over.pop("_o", {})); o.update(over)
        data = []
        seen = set()
        for fld in f["fields"]:
            n = fld["name"]
            if fld["type"] == "submit":
                if submit is not None and n == submit.get("name") and fld["value"] == submit.get("value", fld["value"]):
                    data.append((n, fld["value"]))
                continue
            if fld["type"] in ("checkbox", "radio"):
                if n in o:
                    v = o[n]
                    if v is True or v == fld["value"]:
                        data.append((n, fld["value"]))
                    seen.add(n)
                elif fld["checked"] and n not in seen:
                    data.append((n, fld["value"]))
                continue
            if n in o:
                data.append((n, str(o[n]))); seen.add(n)
            else:
                data.append((n, fld.get("value") or ""))
        for k, v in o.items():
            if k not in {x["name"] for x in f["fields"]}:
                data.append((k, str(v)))
        body = urllib.parse.urlencode(data).encode()
        path = f["action"] or "/"
        r = self.request(f["method"].upper(), path, raw=body, ctype="application/x-www-form-urlencoded")
        if follow and r.status in (301, 302, 303, 307, 308):
            return self.get(r.location.split("#")[0], follow=True)
        return r


def flash(r):
    """Text of toasts/errors on a page, best effort."""
    t = re.sub(r"<script.*?</script>|<style.*?</style>", "", r.text, flags=re.S)
    t = re.sub(r"<[^>]+>", " ", t)
    return re.sub(r"\s+", " ", t)


def wait_for(fn, timeout=60, every=2):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            last = fn()
            if last:
                return last
        except Exception as e:
            last = e
        time.sleep(every)
    return None


def login(email, password, client=None, code=None):
    c = client or Client()
    r = c.submit("/login", action="/login", email=email, password=password)
    st = state()
    if "/login/code" in r.text and (code or (st.get("totp_secret") and email == st.get("owner", {}).get("email"))):
        if not code:
            last = st.get("totp_last", 0)
            if int(time.time() // 30) <= last:  # a code works once: wait for the next step
                time.sleep(30 - time.time() % 30 + 1)
            code = totp(st["totp_secret"]); save_state(totp_last=int(time.time() // 30))
        r = c.submit("/login/code", action="/login/code", code=code)
    return c, r


def owner_client():
    o = state()["owner"]
    c, r = login(o["email"], o["password"])
    assert any("session" in k for k in c.cookies), "owner login failed: " + r.text[:200]
    return c


def dump_forms(c, path):
    r, forms = c.forms(path)
    print(f"== {path} [{r.status}]")
    for f in forms:
        print("  form", f["method"], f["action"], [(x["name"], x["type"] + (":" + ",".join(x["options"][:6]) if x["type"] == "select" else "")) for x in f["fields"]])


import hmac, hashlib, struct, base64


def totp(secret_b32, t=None, step=30, digits=6):
    s = secret_b32.replace(" ", "").upper(); s += "=" * (-len(s) % 8)
    key = base64.b32decode(s)
    n = int((t if t is not None else time.time()) // step)
    h = hmac.new(key, struct.pack(">Q", n), hashlib.sha1).digest()
    o = h[-1] & 15
    return str((struct.unpack(">I", h[o:o + 4])[0] & 0x7fffffff) % 10 ** digits).zfill(digits)


def csrf_of(c, page="/account"):
    r = c.get(page, follow=True)
    m = re.search(r'name="_csrf" value="([^"]+)"', r.text)
    return m.group(1) if m else c.cookies.get("musdash_csrf", "")


def invite(owner, email, role="member"):
    r = owner.submit("/team", action="/team/invitations", email=email, role=role)
    m = re.search(r"/invite/([A-Za-z0-9_\-]+)", r.text)
    return ("/invite/" + m.group(1)) if m else None, r


def accept(link, name, password):
    c = Client()
    r = c.submit(link, action="/invite/", name=name, password=password)
    return c, r


def members(owner):
    r = owner.get("/team")
    out = {}
    for f in parse_forms(r.text):
        m = re.match(r"/team/members/([a-z2-7]+)/role", f["action"])
        if m:
            out[m.group(1)] = f
    return r, out


def ensure_tokens(c=None):
    """(Re)make a read and a deploy API token if the stored ones are dead."""
    st = state(); c = c or owner_client(); out = {}
    for key, ability in (("read_token", "read"), ("deploy_token", "deploy")):
        t = st.get(key)
        if t and Client(token=t).get("/api/v1/me").status == 200:
            out[key] = t; continue
        r = c.submit("/keys", action="/account/tokens", token_name="api-" + ability + str(int(time.time()) % 10000), token_ability=ability, token_expires="never", token_password=st["owner"]["password"])
        out[key] = re.search(r"msd_[A-Za-z0-9_\-]{10,}", r.text).group(0)
    save_state(**out); return out


def dep_wait(c, app, dep, timeout=240):
    """Wait for a deployment page to leave the running states; return 'success'/'failed'/None."""
    def f():
        r = c.get(f"/apps/{app}/deployments/{dep}/status")
        t = re.sub(r"<[^>]+>", " ", r.text).lower()
        if "succeeded" in t: return "success"
        if "failed" in t: return "failed"
        return None
    return wait_for(f, timeout, 3)


def deploy(c, app):
    r = c.post(f"/apps/{app}/deploy", dict(_csrf=csrf_of(c, f"/apps/{app}")))
    m = re.search(r"/deployments/([a-z2-7]{12})", r.location or "")
    return m.group(1) if m else None


def vcurl(url, host=None, extra="", port=80):
    """curl from the VPS itself through the proxy (loopback)."""
    h = f"-H 'Host: {host}'" if host else ""
    return sh(f"curl -s -m 10 -o /tmp/body -w '%{{http_code}}' {h} {extra} {url}; echo; head -c 400 /tmp/body")[1]


class WS:
    """Minimal RFC 6455 client (masked frames out, unmasked in)."""
    def __init__(self, path, cookies, origin=None, host=None, port=8000, ip=None, timeout=15):
        import socket as _s, os as _o, base64 as _b
        path = long_path(path)  # from what the script's earlier requests have seen
        self.s = _s.create_connection((ip or HOST, port), timeout=timeout)
        key = _b.b64encode(_o.urandom(16)).decode()
        h = host or f"{HOST}:{port}"
        lines = [f"GET {path} HTTP/1.1", f"Host: {h}", "Upgrade: websocket", "Connection: Upgrade", f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Version: 13"]
        if origin: lines.append(f"Origin: {origin}")
        if cookies: lines.append("Cookie: " + "; ".join(f"{k}={v}" for k, v in cookies.items()))
        self.s.sendall(("\r\n".join(lines) + "\r\n\r\n").encode())
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = self.s.recv(4096)
            if not d: break
            buf += d
        head, _, rest = buf.partition(b"\r\n\r\n")
        self.status = int(head.split(b" ")[1]) if head else 0
        self.headers = head.decode(errors="replace")
        self.buf = rest
        self.body = rest

    def _read(self, n):
        while len(self.buf) < n:
            d = self.s.recv(65536)
            if not d: raise EOFError("closed")
            self.buf += d
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def recv(self):
        h = self._read(2); op = h[0] & 0xF; ln = h[1] & 0x7F
        if ln == 126: ln = struct.unpack(">H", self._read(2))[0]
        elif ln == 127: ln = struct.unpack(">Q", self._read(8))[0]
        return op, self._read(ln)

    def send(self, op, data):
        import os as _o
        mask = _o.urandom(4); n = len(data)
        hdr = bytes([0x80 | op])
        if n < 126: hdr += bytes([0x80 | n])
        elif n < 65536: hdr += bytes([0x80 | 126]) + struct.pack(">H", n)
        else: hdr += bytes([0x80 | 127]) + struct.pack(">Q", n)
        self.s.sendall(hdr + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def read_until(self, needle, timeout=10):
        end = time.time() + timeout; out = b""; self.s.settimeout(2)
        while time.time() < end:
            try:
                op, d = self.recv()
            except Exception as e:
                if "timed out" in str(e): continue
                break
            if op in (1, 2): out += d
            if op == 8: out += b"<CLOSE %s>" % d[2:]; break
            if needle.encode() in out: break
        return out.decode("utf-8", "replace")

    def close(self):
        try: self.s.close()
        except Exception: pass
