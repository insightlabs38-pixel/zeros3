# Z3-36 — lineage/admission planner

Status: COMPLETE research; base `4d2f9593317804a1a8deaab5ca34f5124fb4d735`.
Decision: PLANNER_NEEDS_MORE_EVIDENCE. Production diff empty; no new persistent
planner primitive, global mutable IDs, wire/default or I4 integration decision.

## Durable physical frontier
Version1 harness-only schema stores SHA/actual raw+packed bytes for23887 physical
objects and36 cases:12 histories,12 one-offs,12 missing-hint controls. Every
candidate root is serialized/reconstructed to complete input bytes/size/SHA;
Reachable and Account reject missing dependencies or hidden writes before costs
are saved. Root/index/table/component bytes and64 B/new-object overhead charged.
Policy replay uses object-set union, so a base declined earlier costs its entire
missing closure at late bootstrap. No free base, external-base wire or ROOTS.
Samples are at most64 sorted unique component SHA IDs, including structural
components; no native-ID namespace mixing. Fields/counts/strings/reads/JSON and
lookup/search sizes bounded; foreign sample IDs and omitted root cost rejected.
Artifacts: `testdata/q6-admission/frontier.json.gz`, provenance/results gzip JSON.
They replay costs independently of parsers. Proofs ran during capture; the cost
snapshot alone is not a fresh reconstruction proof without the pinned inputs.

## Corpus, provenance and reuse
REAL: Pythia14M Safetensors step1000/2000/3000; F-Droid2.0.0/2.0.1/2.0.2
classes.dex; Newtonsoft12.0.3,13.0.1/2/3/4 DLLs; public CARv1/v2 packaging;
BusyBox1.36.1/1.37.0 gzip layers with native digest/DiffID verification from Q3.
REAL_REEXPORT: cached hiredis full/reordered/delta packs and libzstd compression
of those exact BusyBox plaintext layers. No killed PyTorch target resurrected.
PRODUCER_WORKLOAD: SQLite integrity-checked updates/VACUUM, RocksDB9.8.4 and
Pebble1.1.5 native SST chain snapshots, committed PyArrow layout variants.
SST operation generator shell-copied from Z3-30, base counts scaled1/100;
other operations unchanged.11 tables/RocksDB; actual snapshots/metadata pinned.
Merkle deep control uses existing native DAG fixture, SYNTHETIC_FIXTURE; no
natural Ethereum history claim. Small FP32 model training is not the BF16
fine-tune admission debt of the larger Q29 corpus. No multi-GiB regeneration.
Every public archive/native member, cached producer output and Git input SHA
is checked; no corpus downloaded by tests. Native packages are producers only.
Existing repz3 kind adapters/econx/CDC/packing/model encoders reused. USTAR SST
snapshot adapter shell-extracted from EncodeTarLayer, changing only child hook.

## Policies and observed storage
Fixed before measurement: greedy current cost;10% premium cap; late bootstrap
(25% current premium cap,25% previous-sketch overlap); expected value with
fixed0.25/0.65 continuation prior, bounded sketches; one-object buffered lookahead;
width8/depth2 beam; exact small-case B&B hindsight (100000-state cap).
Streaming policies never consult future rows; lookahead sees full buffered
candidate costs and is explicitly a batch/delay upper bound, not prediction.
All252 policy results have exact small-case hindsight; B&B matches brute force
on50 random controls. Primary12 histories, oracle115918665 B:

| policy | total B | regret vs oracle | bad / missed lineages |
|---|---:|---:|---:|
| greedy |131470043|13.42%|0 /6|
| premium cap |129524441|11.74%|0 /5|
| late bootstrap |133901577|15.51%|1 /5|
| expected value |128456102|10.82%|2 /2|
| one step |115977587|0.051%|0 /1|
| beam |115926317|0.0066%|0 /0|
| hindsight |115918665|0|0 /0|

Expected value captures only19.4% of greedy's available saving, and loses on
CLR/Git. One-step/beam capture99.6/99.95% but require future buffering.
Expected-value one-off SQLite admission pays5.05% premium; aggregate one-off
regret0.10%. Without caller key hints, expected-value regret12.64%,misses4.
Bad admission = any rep selected but whole lineage costs more than all-CDC;
miss = all-CDC selected despite a profitable hindsight lineage. Not per-object
vulnerability or correctness metrics. Overhead0/256 B sensitivity retained:
at256 B expected-value132516961 exceeds greedy132500473; no robust policy win.

## CPU, reads and state
All current candidates dry-run; shared-cache capture cost totals42.325s for
primary histories. Policy comparisons charge those recorded analysis calls;
these are counterfactual estimates, not production request latency. No cheap
sample-only analyzer claimed. Expected-value bootstrap full-prior-redo scenario
adds17.531s estimate and20.19 MiB selected-source replay reads; actual bootstrap
cache/CPU depends on retained recipes. Storage closure cost is exact regardless.
Whole-replay extra logical bytes vs CDC: greedy7.90 MiB,expected14.57,
one-step22.84,beam23.22. Not packed I/O or random-range performance.
Lookahead buffers26.84/53.68 MiB raw inputs and prefetches future analyses;
retained buffers avoid duplicate eventual source reads but delay admission.
Membership bitset2992 B and at most6432 B sketch digest payload per case;
Go metadata/string allocations and buffered candidate bytes are additional.

## Decision and validation
No production integration design recommended yet: streaming policy misses most
hindsight gain, full candidate analysis is costly, one-offs admit badly, and
lookahead requires upload batching/latency semantics. Need broader BF16/fine-tune,
natural Merkle and full-scale SST histories plus bounded bootstrap analysis.
Carry physical frontier and first-sight costs to Q7 convergence; no promotion.
Canonical captured cases, hidden-write/root/sample rejection, charged bootstrap,
all-prefix non-future decisions, brute-force oracle and bounded-cap tests pass.
Initial/final schema fuzz79942/78166 cases,25s each. Full lab/root suites+vet,
whole-tree gofmt, native capture and CLI replay pass; no failures removed.
Reproduce: q6_fetch_frontier_inputs.py/q6_producer_inputs.py, then
`go run ./cmd/z3admission -mode capture ...`; `-mode replay -in frontier.json.gz`.
