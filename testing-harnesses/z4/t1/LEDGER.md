# C1/T1 completion ledger

State of each mandatory item (implementation, not narrative). `done` means implemented and covered by a test that ran on the committed tree.

| Item | State | Evidence / remaining |
|---|---|---|
| 1A t1run exit-status handling | done | `harness/t1run/negative_test.go` (16 negative controls + judge table + cache + interrupt) |
| 1B qualify.sh verdict | done | `z4/t1/qualify_test.sh` (14 controls), `qual_summary.py` fingerprint = tree without `results/` dirs |
| 1C cache/scheduler audit | done | atomic identity-checked entries, fixtures hashed, skips never reused, disk budget + `-need-disk` |
| 1D conventional-suite equivalence | pending | final run: `go test ./...` and `-race` vs t1run |
| 2 IQ3_S | engine+production done | byte-exact on IQ3_M/IQ3_XS (57 tensors); `zn7iq3s` feature; remaining: throughput work, old-binary refusal test |
| 3 streaming Brotli | engine done (562 oracle scenarios, libbrotli 1.1.0 and 1.2.0), production region regenerated; remaining: Parquet recipe integration |
| 4A-4D economics, planner, parallel replay, compat | pending | |
| 5 G1 equivalence audit | pending | |
| 6 fixtures, G3, runner performance | pending | |
