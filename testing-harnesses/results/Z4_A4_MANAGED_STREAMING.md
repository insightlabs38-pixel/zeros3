# Z4 A4 final architecture review and qualification

Verdict: **MANAGED11_12_PRODUCTION_READY** for the bounded supported subset.
Integration: PR #95, normal merge authorized after the gates below; see its live merge state.
Production source SHA-256: ac29a5646dd32a7c388a7325aa8929f3d5087cf3d362fee205d63b74f6b6f493.
Source repair checkpoints: 169f972, bef0b08, ff23f24, a345c2f; earlier A4 work retained.
No original draft was merged wholesale. No LOC threshold determined this decision.

## Architecture decision

DEX/CLR creation uses ReaderAt windows, compact identity arrays, staged references,
external stable sorting and bounded direct component emission. It retains neither
original + masked file copies nor all output components. Decoder getters use two
bounded passes; reconstruction remains one <=32 MiB image with borrowed CLR heaps.
Prefix construction/lookup is O(N log N) /O(32 log N); Put is constant work.
ClassIndex uses sorted handle lookup, exact occurrence ranks and compact CSR/unique
paths; no duplicate-class scan. Differential legacy oracles preserve collisions,
ambiguous IDs, repeated handles, class order, reference bytes and five frozen roots.
Physical CLR bodies are parsed, sized and token-validated once despite method aliases.
Shared decode budgets now charge node traversal, group reads and both input passes.
No mutable global analyzer state, production subprocess or new external dependency.
The one-file stdlib production layout and mechanically reproducible port are retained.

Creation remains off by default; ordinary SHA-256/CDC and journal authority remain.
Managed kinds 11/12 require cumulative floor 9, including nested roots. Floors 6–8
and kinds 1–10 retain their decoders. Unsupported features and exhausted budgets
fall back to CDC; optional success is not required for PUT success.
Existing caps remain: 32 MiB source/output; 16 MiB/group, 128 MiB aggregate groups;
65,536 groups; bounded heaps/rows/signatures/depth and 1 GiB source inspection.
No supported-input cap was reduced or raised to obtain this qualification.

## Actual repairs from this independent review

| Failure | Repair / durable reproducer |
|---|---|
| Component length sum wrapped into a slice panic | subtraction-before-addition checks; overflow regression |
| CLR child counts silently truncated at 32 bits | checked against remaining child rows before conversion |
| Decoder signature-slot expansion lacked the encoder's aggregate bound | checked sizing before allocation |
| Plain/sorted spools accepted missing complete records as EOF/success | expected record counts checked |
| Mask/hash/cutter reads replaced cancellation with unexpected EOF | original non-EOF I/O errors preserved |
| Shared body slots charged once per method, falsely exceeding 2M slots | one physical-body sizing helper |
| Token validation repeated for every method alias | shared validator caches physical-body validity |
| Legal empty groups bypassed nested replay accounting | shared step/byte budgets cover nodes and both passes |
| Existing real-process fork resume raced an identical in-flight commit | one equivalent-content retry with an atomic precondition |

The fork failure occurred in the first full suite (529.002s), was reproduced without
sleeps, then repaired. Different/later writers and ordinary replication still conflict.
No canonical test was skipped or weakened; failed diagnostics and setup mistakes remain.
The shared-body counterexample has 8,192 aliases and 65,536 physical token references:
the preceding source spent 11.0s and fell back; final encoding is .112s median, exact.

## Final isolated measurements

Three fresh processes/stores per input, warm/unflushed OS cache; compiler/producer
excluded; actual CDC manifest ReaderAt. Values are medians of per-run phase peaks.

| Input MB | Encode RSS /creation RSS MiB | Encode /proof /cold seconds |
|---|---:|---:|
| Newtonsoft 0.72 | 22.88 /25.60 | .229 /.126 /.133 |
| FSharp 5.73 | 43.28 /51.43 | .587 /.397 /.430 |
| CoreLib 12.78 | 81.42 /113.70 | 1.369 /1.008 /.972 |
| Roslyn 18.60 | 107.62 /144.97 | 2.052 /1.428 /1.485 |
| F-Droid three 9.30 MB revisions | 83.47–85.33 /96.50–98.18 | 1.144–1.231 /.626–.647 /.624–.733 |
| Dense 0.53 /2.14 /4.30 MB | 20.27 /22.80; 51.69 /51.69; 80.59 /86.43 | raw phases in evidence |
| 32 MiB overlay on real Newtonsoft | 29.43 /77.34 | .333 /.189 /.181 |
| 0.49 MB shared-body control | 23.41 /23.77 | .112 /.049 /.047 |

Roslyn prior A4: 107.57 /149.14 MiB; earlier negative creation ~195 MiB retained.
Fresh preceding-checkpoint recheck: 110.61 /149.06 MiB, 2.054s encode /1.540s proof.
Paired timing checks explain initial variation; final results show no material regression.
Largest process RSS observed across all 36 final trials: 161.86 MiB (includes cold read).
Go heap per phase and process high-water marks are retained, not inferred from RSS.
Separate 10ms temp monitoring: max managed staging 41.48 MiB /all staging 44.24 MiB;
normal/failure cleanup passes. Sampling can miss short peaks; monitored times are not latency claims.
All 36 full/cold SHA/byte proofs pass. Eleven existing input roots are unchanged;
the new formerly rejected shared-body root is deterministic. CDC growth stays additive.

## Validation, decision and remaining debt

Final root `go test ./...`: 324.344s PASS, including all added tests; analysis suite PASS.
Both vets, formatting, one-production-file and stdlib dependency checks pass.
Focused root race: 10.981s; analysis CLR/index/DEX races: 3.530 /3.960 /2.116s PASS.
Final managed-root fuzz 10,611 runs; CLR decode/parser 19,150 /16,774; unchanged DEX 56,106 PASS.
Each native CLR/DEX matrix: 87 checks +18 snapshot checks; five old generations each
produce 79 floor refusals +1 separate unsupported CLI case with before/after digests.
Portable full/delta, replication, restart/full/range reads, GC, repack, pruning, doctor,
compact and tier operations pass. Mixed kinds, nested floor 9 and dependency closure pass.
Ten ordinary PUT interruption/retry cases, ten publication SIGKILL cases and two actual
mid-spool kills pass; CDC survives, old floor remains 1 before publication, GC removes spools.
Missing/corrupt/truncated dependencies, short writers, temporary I/O and cancellation pass.
No material architecture/correctness gate remains unverified. Proceed with normal #95 merge.
Post-B8 debt: opt-in synchronous CPU/I/O, metadata RSS, <=32 MiB full reconstruction,
private-range amplification and additive storage. These are bounded tradeoffs, not new DEFER/KILL decisions.
Evidence: ../z4/a4-integration-evidence.json.gz; failures/history: a4-final-review-evidence.json.gz.
Reproduction: ../z4/scripts/a4_review_{corpus,measure,summarize,midkill}.py plus existing A4 probes/matrices.
B1 similarity/delta CAS is the next recommended research item; it was not started.
