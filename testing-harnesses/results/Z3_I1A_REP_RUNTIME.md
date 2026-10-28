# Z3-I1A — representation runtime hardening

Status: **IN PROGRESS**; base `b050175029f8a484db1e97c6678c863b4d4984dd`.
No merge eligibility is claimed until the complete compatibility gates pass.

## Question and baseline
Reduce maintenance memory and whole-read overhead without changing persistent
bytes, liveness, default off mode, or authoritative CDC transfers.
The graph walker and representation accounting already use digest keys.
The maintenance scan nevertheless builds a temporary hex-keyed length map and
copies it into a second retained length map. Representation closures also retain
hex strings. These are separate costs and must not be conflated.
Root loading currently reads an arbitrarily large file before enforcing its
64 MiB parser limit; source construction itself shares the descriptor map.

`BenchmarkRepReachabilitySQLite` measures a full maintenance scan of a compacted
16 MiB SQLite-shaped fixture (4096 pages, seed 1401), not a natural history.
The benchmark checks liveness and representation discovery on every iteration.
Baseline/candidate measurements and the 1 GiB black-box comparison are pending.

## Gates and remaining work
- Q1-A: measure and reduce duplicate scan state; digest-key conversion audit.
- Q1-B: bound disk reads before parsing; assess descriptor materialization.
- Q1-C: compare whole/range reads before selecting a read policy.
- Q1-D: inspect sync/native replicate/bundle import after runtime work.
- Final: representation suite, targeted race, I0 black-box compatibility,
  levels 1–5, pre-I0 refusal, bundle compatibility, wire goldens, vet/full tests.
- Production LOC/helper audit and final verdict remain pending.
