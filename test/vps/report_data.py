"""Collapse out/results.jsonl to the latest record per case id and print a summary by suite."""
import json, re, collections
latest = collections.OrderedDict()
for line in open("out/results.jsonl"):
    try: r = json.loads(line)
    except Exception: continue
    latest[r["case"]] = r
def suite(c):
    m = re.match(r"S(\d+)", c); return int(m.group(1)) if m else 99
by = collections.defaultdict(list)
for c, r in latest.items(): by[suite(c)].append(r)
tot = collections.Counter(r["status"] for r in latest.values())
print(dict(tot), len(latest))
for s in sorted(by):
    cnt = collections.Counter(r["status"] for r in by[s]); print(f"S{s}: {dict(cnt)}")
print("--- FAIL")
for c, r in latest.items():
    if r["status"] == "FAIL": print(c, r["sev"], r["note"][:160])
