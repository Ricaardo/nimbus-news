#!/usr/bin/env python3
import datetime
import json
import urllib.request

base = "https://home.treasury.gov"
request = urllib.request.Request(base + f"/news-data/press-releases/search/{datetime.datetime.now(datetime.timezone.utc).year}.json", headers={"User-Agent": "nimbus-news/1.0"})
with urllib.request.urlopen(request, timeout=25) as response:
    data = json.load(response)
items = data.get("items", [])[:3]
if not items:
    raise SystemExit("Treasury press releases not found")
print("🏛 Treasury 最新公告")
for item in items:
    print(f"- {item.get('title', '').strip()}\n  {base}{item.get('url', '')}")
