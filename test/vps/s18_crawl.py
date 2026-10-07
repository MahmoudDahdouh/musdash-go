"""HTTP-level page sweep: follow every same-site link from Home with a signed-in session; no 5xx, no inline script, security headers present."""
from lib import *
c = owner_client(); seen = {}; todo = ["/", "/projects", "/tags", "/servers", "/sources", "/team", "/keys", "/settings", "/settings/storages", "/settings/notifications", "/team/variables", "/account"]
skip = re.compile(r"/(logout|stream|download|webhook-secret|values|status|live|switch)|^/static|\?hide")
bad = {}; inline = []; hdrs_missing = set()
while todo and len(seen) < 220:
    p = todo.pop(0)
    if p in seen or skip.search(p): continue
    r = c.get(p, follow=False); seen[p] = r.status
    if r.status >= 500 or r.status == 0: bad[p] = r.status
    if r.status == 200 and "text/html" in r.h("content-type"):
        if re.search(r"<script(?![^>]*\bsrc=)[^>]*>[^<]", r.text) or re.search(r'\sstyle="', r.text) or re.search(r'\son[a-z]+="', r.text): inline.append(p)
        for h in ("content-security-policy", "x-content-type-options", "x-frame-options", "referrer-policy"):
            if not r.h(h): hdrs_missing.add(h)
        for l in re.findall(r'href="(/[^"#]*)"', r.text):
            l = l.replace("&amp;", "&")
            if l not in seen and not skip.search(l): todo.append(l)
bad404 = {p: s for p, s in seen.items() if s == 404}
check("S18.1 crawl: no server errors", not bad, f"{len(seen)} pages fetched signed-in, statuses {dict(__import__('collections').Counter(seen.values()))}; 5xx: {bad}", "S2")
check("S18.2 crawl: no dead links", not bad404, f"links that answer 404: {list(bad404)[:8]}", "S3")
check("S18.3 crawl: no inline script/style/handlers (CSP-clean)", not inline, f"pages with inline script, style attribute or on* handler: {inline[:6]}", "S2")
check("S18.4 crawl: security headers on every page", not hdrs_missing, f"missing: {sorted(hdrs_missing)}", "S3")
