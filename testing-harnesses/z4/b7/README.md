# Z4-B7 planner evidence
- `protocol.json` preregistered folds/grids/price books/promotion rule (committed before any held-out evaluation).
- `build_series.py` + `series.json`: B1/B3/B4/B5/B6 per-stage candidate costs extracted from pinned repo evidence (every scheme tagged measured/modeled).
- `b2_reference.json`: compact copy of B2 draft #97 measured physical rows (isolated reference, B2 not merged).
- `rows.json.gz`: all held-out rows; `summary.txt`, `verdict.txt`, `frontier.txt`, `experiments.txt|json`: outputs.
- Code: `testing-harnesses/z3/planner2`, driver `testing-harnesses/z3/cmd/z4b7`. Reproduce with `run_all.sh`.
