#!/usr/bin/env bash
# Reproduce Z4-B7 end to end. Every step must succeed (set -e, no pipes into tail/grep).
set -euo pipefail
cd "$(dirname "$0")/../.."          # testing-harnesses/
python3 -I z4/b7/build_series.py . > /tmp/b7_series.json
cmp /tmp/b7_series.json z4/b7/series.json   # committed frontier == regenerated from pinned evidence
cd z3
go vet ./planner2 ./cmd/z4b7
go test -count=1 ./planner2
go build -o /tmp/z4b7 ./cmd/z4b7
/tmp/z4b7 -mode eval -seeds 4 -out ../z4/b7/rows.json.gz     # ~40 min on 4 cores
/tmp/z4b7 -mode summarize > ../z4/b7/summary.txt
/tmp/z4b7 -mode verdict   > ../z4/b7/verdict.txt
/tmp/z4b7 -mode frontier  > ../z4/b7/frontier.txt
/tmp/z4b7 -mode exp       > ../z4/b7/experiments.txt
