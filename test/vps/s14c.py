from lib import *
st = state()
out = sh(f"for i in $(seq 1 140); do curl -s -o /dev/null -D - -w 'CODE=%{{http_code}}\\n' -H 'Authorization: Bearer {st['read_token']}' http://127.0.0.1:8000/api/v1/me | grep -E '^CODE|^Retry-After'; done | sort | uniq -c")[1]
codes = dict(re.findall(r"(\d+) CODE=(\d+)", out)[i][::-1] for i in range(len(re.findall(r"(\d+) CODE=(\d+)", out))))
ra = re.findall(r"Retry-After: (\d+)", out)
check("S14.9", "429" in codes and int(codes.get("200", 0)) in range(115, 125) and ra, f"140 rapid calls: {out.strip().replace(chr(10), '; ')}", sev="S2")
# another token for the same user is separate; unauth address limit (600/min) not tested here
time.sleep(61)
check("S14.9b", Client(token=st["read_token"]).get("/api/v1/me").status == 200, "limit window recovers after a minute")
