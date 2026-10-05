from lib import *
c = owner_client()
def channels():
    pg = c.get("/settings/notifications").text; return list(dict.fromkeys(re.findall(r"/settings/notifications/([a-z2-7]{12})/test", pg)))
CASES = {"discord": dict(url="https://discord.com/api/webhooks/123456789012345678/fake-token-for-test"), "slack": dict(url="https://hooks.slack.com/services/T000/B000/fake"), "mattermost": dict(url="https://mattermost.example.com/hooks/fake"),
         "telegram": dict(token="123456:FAKE-TOKEN", chat="-100123"), "pushover": dict(token="faketoken", user="fakeuser") , "email": dict(host="smtp.example.com", port="587", username="u", password="p", **{"from": "a@example.com"}, to="b@example.com")}
for kind, cfg in CASES.items():
    before = set(channels())
    r, forms = c.forms(f"/settings/notifications?kind={kind}"); f = c.find_form(forms, "/settings/notifications", has="name")
    fields = {x["name"] for x in f["fields"]}
    kw = {("cfg_" + k): v for k, v in cfg.items() if ("cfg_" + k) in fields}
    r = c.post_form(f, name="t-" + kind, **kw)
    new = [x for x in channels() if x not in before]
    msg = None
    if new:
        t = c.post(f"/settings/notifications/{new[0]}/test", dict(_csrf=csrf_of(c, "/settings/notifications")), follow=True); msg = flash(t)[-130:]
        c.post(f"/settings/notifications/{new[0]}/delete", dict(_csrf=csrf_of(c, "/settings/notifications")))
    check("S11.5." + kind, bool(new) and msg and "Internal" not in msg, f"{kind}: channel saved with its fields {sorted(fields - {'_csrf','kind','name'})}; Test with fake credentials fails cleanly: {msg!r}", sev="S3")
