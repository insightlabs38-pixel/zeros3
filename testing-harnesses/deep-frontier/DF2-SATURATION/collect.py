"""Collect successful signed replay cases and their native allocation evidence."""
import json
from pathlib import Path
import sys

for name in sys.argv[1:]:
    path = Path(name)
    lines = path.read_text().splitlines()
    assert "PASS" in lines and not any("--- SKIP:" in s or "--- FAIL:" in s for s in lines), path
    prep = [json.loads(s[8:]) for s in lines if s.startswith("DF0PREP ")]
    rows = [json.loads(s.split(" ", 1)[1]) for s in lines if s.startswith(("DF2SAT ", "DF1SIGNED "))]
    assert len(prep) == len(rows) == 1 and prep[0]["status"] == "measured", path
    row = rows[0]
    row["experiment"] = path.stem
    if "allocated_bytes" not in row:
        row["allocated_bytes"] = prep[0]["allocated_bytes"]
        row["pack_allocated_bytes"] = prep[0]["pack_allocated_bytes"]
    print(json.dumps(row, sort_keys=True))
