from lib import *
import hmac, hashlib
c = owner_client(); st = state(); g = st["git"]; web = st["web"]
REPO = "crccheck/docker-hello-world"; SHA = "a" * 40
def deps(app): return len(set(re.findall(r"/deployments/([a-z2-7]{12})", c.get(f"/apps/{app}/deployments").text)))
def secret():
    return re.findall(r'data-copy="([^"]+)"', c.get(f"/apps/{g}/settings").text)[1]
def settled(t=120):
    return wait_for(lambda: "Running" in re.sub("<[^>]+>", " ", c.get(f"/apps/{g}").text)[:600] or "Failed" in re.sub("<[^>]+>", " ", c.get(f"/apps/{g}").text)[:600], t, 3)
SEC = secret()
def post(path, body, headers):
    return Client().request("POST", path, raw=body, headers=dict({"Content-Type": "application/json"}, **headers))
def gh_sig(body, sec=SEC): return "sha256=" + hmac.new(sec.encode(), body, hashlib.sha256).hexdigest()
def push(branch="master", repo=REPO, after=SHA, ref=None):
    return json.dumps({"ref": ref or f"refs/heads/{branch}", "after": after, "repository": {"full_name": repo}}).encode()
path = f"/webhooks/git/{g}"
# S7.11 GitHub
n0 = deps(g); b = push(); r = post(path, b, {"X-Hub-Signature-256": gh_sig(b), "X-GitHub-Event": "push"})
time.sleep(5); n1 = deps(g)
check("S7.11a", r.status in (200, 202, 204) and n1 == n0 + 1, f"GitHub push with a valid HMAC starts a deployment: {r.status} {r.text[:60]!r}; deployments {n0}->{n1}", sev="S2")
settled()
# indistinguishable refusals
bad = {"bad signature": post(path, b, {"X-Hub-Signature-256": gh_sig(b, "wrong")}), "no signature": post(path, b, {}), "unknown id": post("/webhooks/git/zzzzzzzzzzzz", b, {"X-Hub-Signature-256": gh_sig(b)}),
       "garbage sig": post(path, b, {"X-Hub-Signature-256": "sha256=nothex"}), "sha1 header only": post(path, b, {"X-Hub-Signature": "sha1=" + hmac.new(SEC.encode(), b, hashlib.sha1).hexdigest()}),
       "not json signed": post(path, b"hello", {"X-Hub-Signature-256": gh_sig(b"hello")})}
sig = {k: (v.status, v.text, v.h("content-type")) for k, v in bad.items() if k != "not json signed"}
check("S7.11b", len(set(sig.values())) == 1 and list(sig.values())[0][0] in (401, 403, 404), f"unknown id, missing secret and bad signature are indistinguishable: {sig}", sev="S1")
rec("S7.11c", "INFO", f"signed non-JSON body -> {bad['not json signed'].status} {bad['not json signed'].text[:40]!r}")
check("S7.11d", deps(g) == n1, "none of the refused requests started a deployment", sev="S1")
# S7.12 other hosts
n = deps(g)
gl = json.dumps({"object_kind": "push", "ref": "refs/heads/master", "after": "b" * 40, "project": {"path_with_namespace": REPO}}).encode()
r1 = post(path, gl, {"X-Gitlab-Token": SEC, "X-Gitlab-Event": "Push Hook"}); time.sleep(4); n2 = deps(g)
check("S7.12a", r1.status in (200, 202, 204) and n2 == n + 1, f"GitLab push (secret token header): {r1.status}; deployments {n}->{n2}", sev="S2"); settled()
bb = json.dumps({"push": {"changes": [{"new": {"type": "branch", "name": "master", "target": {"hash": "c" * 40}}}]}, "repository": {"full_name": REPO}}).encode()
r2 = post(path, bb, {"X-Hub-Signature": gh_sig(bb), "X-Event-Key": "repo:push"}); time.sleep(4); n3 = deps(g)
check("S7.12b", r2.status in (200, 202, 204) and n3 == n2 + 1, f"Bitbucket push (X-Hub-Signature): {r2.status}; deployments {n2}->{n3}", sev="S2"); settled()
gt = push(after="d" * 40); r3 = post(path, gt, {"X-Gitea-Signature": hmac.new(SEC.encode(), gt, hashlib.sha256).hexdigest(), "X-Gitea-Event": "push"}); time.sleep(4); n4 = deps(g)
check("S7.12c", r3.status in (200, 202, 204) and n4 == n3 + 1, f"Gitea push (bare-hex X-Gitea-Signature): {r3.status}; deployments {n3}->{n4}", sev="S2"); settled()
glbad = post(path, gl, {"X-Gitlab-Token": "wrong"}); check("S7.12d", glbad.status == bad["bad signature"].status and glbad.text == bad["bad signature"].text, f"GitLab wrong token looks like any other refusal: {glbad.status}", sev="S1")
# S7.13 branch filter and repo filter
n = deps(g); skip = {}
for label, body in {"other branch": push("develop"), "tag push": push(ref="refs/tags/v1"), "branch deleted": push(after="0" * 40), "other repo": push(repo="evil/other"), "no repo": json.dumps({"ref": "refs/heads/master", "after": SHA}).encode()}.items():
    rr = post(path, body, {"X-Hub-Signature-256": gh_sig(body)}); skip[label] = rr.status
time.sleep(4)
check("S7.13", deps(g) == n, f"pushes to another branch, tags, deletions and another repository start nothing ({skip}); deployments {n}->{deps(g)}", sev="S2")
# S7.14 rate limit by address (120/min) from the VPS
out = sh(f"for i in $(seq 1 140); do curl -s -o /dev/null -w '%{{http_code}}\\n' -X POST -H 'Content-Type: application/json' http://127.0.0.1:8000/webhooks/git/zzzzzzzzzzzz -d '{{}}'; done | sort | uniq -c")[1]
check("S7.14", "429" in out, f"webhook endpoint rate-limited by address: {out.strip().replace(chr(10), '; ')}", sev="S2")
# S7.16 regenerate secret
old = SEC; c.post(f"/apps/{g}/webhook-secret", dict(_csrf=csrf_of(c, f"/apps/{g}"))); new = secret()
time.sleep(61)
b2 = push(after="e" * 40); ro = post(path, b2, {"X-Hub-Signature-256": gh_sig(b2, old)}); rn = post(path, b2, {"X-Hub-Signature-256": gh_sig(b2, new)})
check("S7.16", new != old and ro.status in (401, 403, 404) and rn.status in (200, 202, 204), f"after regenerating, the old secret is refused ({ro.status}) and the new one works ({rn.status})", sev="S1")
settled()
# S7.15 deploy token
f = [x for x in parse_forms(c.get(f"/apps/{g}/settings").text) if x["action"] == f"/apps/{g}/deploy-token"][0]
r = c.post_form(f); tok = [t for t in re.findall(r'data-copy="([^"]+)"', r.text) if t.startswith("msd_") or len(t) > 30 and "http" not in t and "curl" not in t]
vals = re.findall(r'data-copy="([^"]+)"', r.text)
rec("S7.15x", "INFO", "deploy token response data-copy values: " + str([v[:14] + "..." for v in vals]))
