#!/usr/bin/env python3
import json
import urllib.request

series = {"CPI": "CUUR0000SA0", "失业率": "LNS14000000", "非农就业": "CES0000000001", "平均时薪": "CES0500000003"}
payload = json.dumps({"seriesid": list(series.values()), "latest": True}).encode()
req = urllib.request.Request("https://api.bls.gov/publicAPI/v2/timeseries/data/", data=payload,
                             headers={"Content-Type": "application/json", "User-Agent": "nimbus-news/1.0"}, method="POST")
with urllib.request.urlopen(req, timeout=25) as response:
    data = json.load(response)
if data.get("status") != "REQUEST_SUCCEEDED":
    raise SystemExit("BLS API failed")
by_id = {item["seriesID"]: item.get("data", [])[:1] for item in data["Results"]["series"]}
lines = ["📊 BLS 官方指标快照"]
for label, series_id in series.items():
    items = by_id.get(series_id, [])
    if items:
        item = items[0]
        lines.append(f"{label}: {item.get('value')} ({item.get('year')}-{item.get('period')})")
print("\n".join(lines))
