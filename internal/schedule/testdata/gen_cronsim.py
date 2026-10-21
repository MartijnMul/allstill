# SPDX-License-Identifier: AGPL-3.0-only
"""Writes cronsim.json: what Healthchecks' cronsim (BSD-3-Clause, by Pēteris Caune)

expects for a set of expressions, zones and start times around daylight-saving
changes. Run it with cronsim installed in a virtual environment:

    python -I gen_cronsim.py cronsim.json
"""
import json
import sys
from datetime import datetime, timezone
from zoneinfo import ZoneInfo
from cronsim import CronSim

exprs = ["0 3 * * *","30 2 * * *","*/30 * * * *","15 * * * *","0 */2 * * *","0 2 * * *","0 2-3 * * *","*/7 2 * * *",
 "30 1 * * *","0 0 L * *","0 0 * * 5L","0 0 * * fri#2","0 0 1 * mon","0 0 */10 * mon","10/20 * * * *","0 9 * jan-mar 7",
 "0 0 1,15 * *","45 23 * * 0","0 12 * * 1-5","5 4 * * sun","0 22 * * 1-5","0 0 29 2 *","*/5 1-3 * * *","0 1,2,3 * * *",
 "59 1 * * *","0 3 L * *","30 2 */2 * *","0 2 * * 0L","1-59/15 2 * * *","* 2 * * *"]
zones = ["UTC","Europe/Amsterdam","America/New_York","Australia/Lord_Howe","America/Sao_Paulo","Asia/Tehran","Pacific/Chatham"]
starts = ["2026-03-28T12:00:00","2026-03-29T00:20:00","2026-10-24T22:00:00","2026-10-25T00:20:00","2026-11-01T04:00:00","2026-03-08T05:00:00","2026-04-04T14:00:00","2026-10-03T15:00:00","2026-01-15T10:00:00"]
out=[]
for e in exprs:
  for z in zones:
    for s in starts:
      st = datetime.fromisoformat(s).replace(tzinfo=timezone.utc).astimezone(ZoneInfo(z))
      try:
        it = CronSim(e, st)
        runs=[next(it).astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") for _ in range(4)]
      except Exception as ex:
        runs=["ERR "+type(ex).__name__]
      out.append({"expr":e,"zone":z,"after":s+"Z","want":runs})
with open(sys.argv[1], "w") as f:
  f.write("[\n" + ",\n".join(json.dumps(x, separators=(",", ":")) for x in out) + "\n]\n")
